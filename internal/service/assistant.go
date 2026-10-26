package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"tfcloud/internal/config"
	"tfcloud/internal/database"
	"tfcloud/internal/httpapi/respond"
	"tfcloud/internal/secure"
)

type AssistantService struct {
	store    *database.Store
	cfg      *config.Config
	settings *SettingsService
	client   *http.Client
}

func NewAssistant(store *database.Store, cfg *config.Config, settings *SettingsService) *AssistantService {
	return &AssistantService{store: store, cfg: cfg, settings: settings,
		client: &http.Client{Timeout: 120 * time.Second}}
}

func (s *AssistantService) config() (enabled bool, baseURL, apiKey, configID, botName, welcome string, maxLen int, timeout int) {
	group, err := s.settings.GroupWithSecrets("astrbot")
	if err != nil {
		group = s.settings.Group("astrbot")
	}
	enabled = truthy(group["enabled"])
	baseURL = strings.TrimRight(strings.TrimSpace(str(group["base_url"])), "/")
	apiKey = strings.TrimSpace(str(group["api_key"]))
	configID = strings.TrimSpace(str(group["config_id"]))
	botName = firstNonEmpty(strings.TrimSpace(str(group["bot_name"])), "天方助手")
	welcome = str(group["welcome_message"])
	maxLen = asInt(group["max_message_length"])
	if maxLen <= 0 {
		maxLen = 4000
	}
	timeout = asInt(group["request_timeout_seconds"])
	if timeout <= 0 {
		timeout = 90
	}
	return
}

func (s *AssistantService) Status() map[string]any {
	enabled, baseURL, apiKey, _, botName, welcome, maxLen, _ := s.config()
	ready := enabled && baseURL != "" && apiKey != ""
	return map[string]any{
		"enabled":            enabled,
		"ready":              ready,
		"name":               botName,
		"welcome_message":    welcome,
		"max_message_length": maxLen,
	}
}

func (s *AssistantService) conversation(userID int64) (map[string]any, error) {
	var conv struct {
		ID               int64  `db:"id"`
		ConversationNo   string `db:"conversation_no"`
		AstrbotSessionID string `db:"astrbot_session_id"`
	}
	err := s.store.Read.Get(&conv, `SELECT id, conversation_no, astrbot_session_id FROM TF_astrbot_conversations WHERE user_id=?`, userID)
	if err == nil {
		return map[string]any{"id": conv.ID, "conversation_no": conv.ConversationNo, "session_id": conv.AstrbotSessionID}, nil
	}
	now := database.Now()
	sessionID := secure.RandomHex(16)
	res, err := s.store.Write.Exec(`INSERT INTO TF_astrbot_conversations
		(conversation_no, user_id, astrbot_session_id, last_message_at, created_at, updated_at)
		VALUES(?,?,?,NULL,?,?)`, secure.RandomHex(16), userID, sessionID, now, now)
	if err != nil {
		return nil, respond.Internal("创建会话失败")
	}
	id, _ := res.LastInsertId()
	return map[string]any{"id": id, "session_id": sessionID}, nil
}

func (s *AssistantService) History(userID int64, filters map[string]any) (map[string]any, error) {
	page, perPage, offset := paginate(filters, 50)
	conv, err := s.conversation(userID)
	if err != nil {
		return nil, err
	}
	convID := conv["id"].(int64)
	var total int
	_ = s.store.Read.Get(&total, `SELECT COUNT(1) FROM TF_astrbot_messages WHERE conversation_id=?`, convID)
	items, err := s.store.Maps(`SELECT id, role, content, status, error_code, error_message, created_at, updated_at
		FROM TF_astrbot_messages WHERE conversation_id=? ORDER BY id DESC LIMIT ? OFFSET ?`, convID, perPage, offset)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		item["error"] = nil
		if item["error_code"] != nil {
			item["error"] = map[string]any{"code": item["error_code"], "message": item["error_message"]}
		}
	}
	return pageResult(items, total, page, perPage), nil
}

func (s *AssistantService) Reset(userID int64) error {
	conv, err := s.conversation(userID)
	if err != nil {
		return err
	}
	convID := conv["id"].(int64)
	tx, err := s.store.Write.Beginx()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM TF_astrbot_messages WHERE conversation_id=?`, convID); err != nil {
		return err
	}
	newSession := secure.RandomHex(16)
	if _, err := tx.Exec(`UPDATE TF_astrbot_conversations SET astrbot_session_id=?, last_message_at=NULL, updated_at=?
		WHERE id=?`, newSession, database.Now(), convID); err != nil {
		return err
	}
	return tx.Commit()
}

type assistantMessage struct {
	ID        int64  `json:"id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	Error     any    `json:"error"`
}

func (s *AssistantService) persist(convID, userID int64, role, content, status string) (*assistantMessage, error) {
	now := database.Now()
	res, err := s.store.Write.Exec(`INSERT INTO TF_astrbot_messages
		(conversation_id, user_id, role, content, status, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?)`, convID, userID, role, content, status, now, now)
	if err != nil {
		return nil, respond.Internal("保存消息失败")
	}
	id, _ := res.LastInsertId()
	_, _ = s.store.Write.Exec(`UPDATE TF_astrbot_conversations SET last_message_at=?, updated_at=? WHERE id=?`, now, now, convID)
	return &assistantMessage{ID: id, Role: role, Content: content, Status: status, CreatedAt: now}, nil
}

func (s *AssistantService) update(id int64, content, status string, errCode, errMessage string) {
	_, _ = s.store.Write.Exec(`UPDATE TF_astrbot_messages SET content=?, status=?, error_code=?, error_message=?, updated_at=?
		WHERE id=?`, content, status, nullableString(errCode), nullableString(errMessage), database.Now(), id)
}

func (s *AssistantService) begin(userID int64, input map[string]any) (map[string]any, *assistantMessage, *assistantMessage, error) {
	enabled, baseURL, apiKey, _, _, _, maxLen, _ := s.config()
	if !enabled || baseURL == "" || apiKey == "" {
		return nil, nil, nil, respond.NewError("ASSISTANT_DISABLED", "智能助手尚未启用", 409)
	}
	content := strings.TrimSpace(str(input["message"]))
	if content == "" {
		return nil, nil, nil, respond.NewError("VALIDATION_FAILED", "消息内容不能为空", 422)
	}
	if len([]rune(content)) > maxLen {
		return nil, nil, nil, respond.NewError("ASSISTANT_MESSAGE_TOO_LONG",
			fmt.Sprintf("单条消息不能超过 %d 个字符", maxLen), 422)
	}
	conv, err := s.conversation(userID)
	if err != nil {
		return nil, nil, nil, err
	}
	userMsg, err := s.persist(conv["id"].(int64), userID, "user", content, "completed")
	if err != nil {
		return nil, nil, nil, err
	}
	pending, err := s.persist(conv["id"].(int64), userID, "assistant", "", "pending")
	if err != nil {
		return nil, nil, nil, err
	}
	return conv, userMsg, pending, nil
}

func (s *AssistantService) Send(userID int64, input map[string]any) (map[string]any, error) {
	conv, userMsg, pending, err := s.begin(userID, input)
	if err != nil {
		return nil, err
	}
	_, baseURL, apiKey, configID, _, _, _, timeout := s.config()
	text, err := s.chatOnce(baseURL, apiKey, configID, str(conv["session_id"]), strings.TrimSpace(str(input["message"])), timeout)
	if err != nil {
		s.update(pending.ID, "", "failed", "ASSISTANT_UPSTREAM_FAILED", err.Error())
		return nil, respond.NewError("ASSISTANT_UPSTREAM_FAILED", err.Error(), 502)
	}
	s.update(pending.ID, text, "completed", "", "")
	pending.Content = text
	pending.Status = "completed"
	return map[string]any{"user_message": userMsg, "assistant_message": pending}, nil
}

// Stream 逐 token 输出，事件格式与前端约定一致：started / delta / replace / done / error。
func (s *AssistantService) Stream(ctx context.Context, userID int64, input map[string]any, emit func(event string, data any)) error {
	conv, userMsg, pending, err := s.begin(userID, input)
	if err != nil {
		return err
	}
	emit("started", map[string]any{"user_message": userMsg, "assistant_message": pending})

	_, baseURL, apiKey, configID, _, _, _, timeout := s.config()
	var builder strings.Builder
	streamErr := s.chatStream(ctx, baseURL, apiKey, configID, str(conv["session_id"]),
		strings.TrimSpace(str(input["message"])), timeout, func(chunk string) {
			builder.WriteString(chunk)
			emit("delta", map[string]any{"content": chunk})
		})
	if streamErr != nil {
		s.update(pending.ID, builder.String(), "failed", "ASSISTANT_UPSTREAM_FAILED", streamErr.Error())
		emit("error", map[string]any{"code": "ASSISTANT_UPSTREAM_FAILED", "message": streamErr.Error()})
		return nil
	}
	final := builder.String()
	s.update(pending.ID, final, "completed", "", "")
	emit("done", map[string]any{
		"user_message":      userMsg,
		"assistant_message": &assistantMessage{ID: pending.ID, Role: "assistant", Content: final, Status: "completed", CreatedAt: pending.CreatedAt},
	})
	return nil
}

func (s *AssistantService) TestConnection() (map[string]any, error) {
	enabled, baseURL, apiKey, configID, _, _, _, timeout := s.config()
	if !enabled || baseURL == "" || apiKey == "" {
		return nil, respond.NewError("ASSISTANT_DISABLED", "请先启用并配置智能助手", 409)
	}
	text, err := s.chatOnce(baseURL, apiKey, configID, "tfsign_connection_test", "连接测试，请回复 ok", timeout)
	if err != nil {
		return nil, respond.NewError("ASSISTANT_UPSTREAM_FAILED", err.Error(), 502)
	}
	return map[string]any{"ok": true, "reply": text}, nil
}

// ---------- AstrBot 协议 ----------

func (s *AssistantService) chatURL(baseURL string) string {
	if strings.HasSuffix(baseURL, "/api/v1/chat") {
		return baseURL
	}
	if strings.HasSuffix(baseURL, "/api/v1") {
		return baseURL + "/chat"
	}
	return baseURL + "/api/v1/chat"
}

func (s *AssistantService) payload(apiKey, configID, sessionID, message string) map[string]any {
	body := map[string]any{
		"message":    message,
		"session_id": sessionID,
		"username":   sessionID,
	}
	if configID != "" {
		body["config_id"] = configID
	}
	return body
}

func (s *AssistantService) chatOnce(baseURL, apiKey, configID, sessionID, message string, timeoutSeconds int) (string, error) {
	body, _ := marshalNoEscape(s.payload(apiKey, configID, sessionID, message))
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.chatURL(baseURL), strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("访问智能助手失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("智能助手返回错误（HTTP %d）：%s", resp.StatusCode, truncate(strings.TrimSpace(string(raw)), 200))
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "event-stream") {
		return parseSSEText(raw), nil
	}
	return extractReply(raw), nil
}

func (s *AssistantService) chatStream(ctx context.Context, baseURL, apiKey, configID, sessionID, message string, timeoutSeconds int,
	onChunk func(string)) error {
	body, _ := marshalNoEscape(s.payload(apiKey, configID, sessionID, message))
	streamCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, s.chatURL(baseURL), strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("访问智能助手失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return fmt.Errorf("智能助手返回错误（HTTP %d）：%s", resp.StatusCode, truncate(strings.TrimSpace(string(raw)), 200))
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "event-stream") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		text := extractReply(raw)
		if text != "" {
			onChunk(text)
		}
		return nil
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if chunk := s.extractChunk(payload); chunk != "" {
			onChunk(chunk)
		}
	}
	return scanner.Err()
}

func (s *AssistantService) extractChunk(payload string) string {
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return payload
	}
	for _, key := range []string{"delta", "content", "text", "message", "data", "reply"} {
		if v, ok := obj[key]; ok {
			switch t := v.(type) {
			case string:
				return t
			case map[string]any:
				if s := firstText(t["content"], t["text"], t["delta"]); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func parseSSEText(raw []byte) string {
	var b strings.Builder
	for _, line := range bytes.Split(raw, []byte("\n")) {
		trimmed := strings.TrimSpace(string(line))
		if !strings.HasPrefix(trimmed, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err == nil {
			b.WriteString(firstText(obj["delta"], obj["content"], obj["text"], obj["message"], obj["reply"]))
			continue
		}
		b.WriteString(payload)
	}
	return b.String()
}

func extractReply(raw []byte) string {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return strings.TrimSpace(string(raw))
	}
	if data := signMap(obj["data"]); data != nil {
		if s := firstText(data["reply"], data["content"], data["text"], data["message"]); s != "" {
			return s
		}
	}
	return firstText(obj["reply"], obj["content"], obj["text"], obj["message"])
}

func signMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}
