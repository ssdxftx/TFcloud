package service

import (
	"fmt"
	"log/slog"
	"net/mail"
	"strconv"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"

	"tfcloud/internal/config"
	"tfcloud/internal/database"
	"tfcloud/internal/httpapi/respond"
	"tfcloud/internal/secure"
)

type MailService struct {
	store    *database.Store
	cfg      *config.Config
	settings *SettingsService
	logger   *slog.Logger
}

func NewMail(store *database.Store, cfg *config.Config, settings *SettingsService, logger *slog.Logger) *MailService {
	return &MailService{store: store, cfg: cfg, settings: settings, logger: logger}
}

type smtpConfig struct {
	Enabled    bool
	Host       string
	Port       int
	Encryption string
	Username   string
	Password   string
	FromEmail  string
	FromName   string
	ReplyTo    string
	BatchLimit int
}

func (m *MailService) config() (smtpConfig, error) {
	group, err := m.settings.GroupWithSecrets("mail")
	if err != nil {
		return smtpConfig{}, err
	}
	cfg := smtpConfig{
		Enabled:    truthy(group["enabled"]),
		Host:       strings.TrimSpace(str(group["smtp_host"])),
		Port:       asInt(group["smtp_port"]),
		Encryption: str(group["smtp_encryption"]),
		Username:   strings.TrimSpace(str(group["smtp_username"])),
		Password:   str(group["smtp_password"]),
		FromEmail:  strings.TrimSpace(str(group["from_email"])),
		FromName:   firstNonEmpty(strings.TrimSpace(str(group["from_name"])), "天方云签"),
		ReplyTo:    strings.TrimSpace(str(group["reply_to"])),
		BatchLimit: asInt(group["batch_limit"]),
	}
	if cfg.BatchLimit <= 0 {
		cfg.BatchLimit = 100
	}
	if cfg.Port == 0 {
		cfg.Port = 465
	}
	return cfg, nil
}

func (m *MailService) sendOne(cfg smtpConfig, recipient, subject, html, text string) (string, error) {
	opts := []gomail.Option{
		gomail.WithPort(cfg.Port),
		gomail.WithTimeout(30 * time.Second),
	}
	if cfg.Username != "" {
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthPlain), gomail.WithUsername(cfg.Username), gomail.WithPassword(cfg.Password))
	}
	switch cfg.Encryption {
	case "ssl":
		opts = append(opts, gomail.WithSSL())
	case "tls":
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	default:
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	}
	client, err := gomail.NewClient(cfg.Host, opts...)
	if err != nil {
		return "", fmt.Errorf("SMTP 客户端初始化失败: %w", err)
	}

	msg := gomail.NewMsg()
	from := cfg.FromEmail
	if from == "" {
		from = cfg.Username
	}
	if err := msg.FromFormat(cfg.FromName, from); err != nil {
		return "", fmt.Errorf("发件人地址无效: %w", err)
	}
	if err := msg.To(recipient); err != nil {
		return "", fmt.Errorf("收件人地址无效: %w", err)
	}
	if cfg.ReplyTo != "" {
		_ = msg.ReplyTo(cfg.ReplyTo)
	}
	msg.Subject(subject)
	if text != "" {
		msg.SetBodyString(gomail.TypeTextPlain, text)
	}
	if html != "" {
		msg.AddAlternativeString(gomail.TypeTextHTML, html)
	}
	if err := client.DialAndSend(msg); err != nil {
		return "", fmt.Errorf("SMTP 发送失败: %w", err)
	}
	return msg.GetMessageID(), nil
}

// SendTransactional 同步发送并落审计记录，语义与原实现一致。
func (m *MailService) SendTransactional(recipient, subject, html, text, templateCode string, userID int64) error {
	if _, err := mail.ParseAddress(recipient); err != nil {
		return respond.NewError("VALIDATION_FAILED", "收件邮箱格式不正确", 422)
	}
	cfg, err := m.config()
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return respond.NewError("MAIL_DISABLED", "邮件服务尚未启用，请联系管理员", 409)
	}
	now := database.Now()
	taskNo := secure.RandomHex(16)
	messageNo := secure.RandomHex(16)
	lower := strings.ToLower(recipient)
	res, err := m.store.Write.Exec(`INSERT INTO TF_mail_tasks
		(task_no, user_id, template_code, subject, status, priority, total_count, success_count, failed_count,
		 scheduled_at, started_at, created_at, updated_at)
		VALUES(?,?,?,?, 'running', 20, 1, 0, 0, ?,?,?,?)`,
		taskNo, nullableID(userID), truncate(templateCode, 64), truncate(subject, 255), now, now, now, now)
	if err != nil {
		return respond.Internal("写入邮件任务失败")
	}
	taskID, _ := res.LastInsertId()
	rec, err := m.store.Write.Exec(`INSERT INTO TF_mail_records
		(task_id, message_no, recipient, recipient_hash, subject, body_html, status, attempts, max_attempts, available_at, created_at, updated_at)
		VALUES(?,?,?,?,?,?, 'sending', 1, 1, ?,?,?)`,
		taskID, messageNo, lower, secure.SHA256Hex(lower), truncate(subject, 255), html, now, now, now)
	if err != nil {
		return respond.Internal("写入邮件记录失败")
	}
	recordID, _ := rec.LastInsertId()

	messageID, sendErr := m.sendOne(cfg, recipient, subject, html, text)
	if sendErr != nil {
		_, _ = m.store.Write.Exec(`UPDATE TF_mail_records SET status='failed', last_error_code='SMTP_SEND_FAILED',
			last_error_message=?, updated_at=? WHERE id=?`, truncate(sendErr.Error(), 500), database.Now(), recordID)
		_, _ = m.store.Write.Exec(`UPDATE TF_mail_tasks SET status='failed', failed_count=1, finished_at=?, updated_at=? WHERE id=?`,
			database.Now(), database.Now(), taskID)
		m.logger.Error("smtp send failed", slog.String("error", sendErr.Error()), slog.String("template", templateCode))
		return respond.NewError("MAIL_SEND_FAILED", "邮件发送失败："+sendErr.Error(), 502)
	}
	now2 := database.Now()
	_, _ = m.store.Write.Exec(`UPDATE TF_mail_records SET status='sent', provider_message_id=?, sent_at=?, updated_at=? WHERE id=?`,
		messageID, now2, now2, recordID)
	_, _ = m.store.Write.Exec(`UPDATE TF_mail_tasks SET status='succeeded', success_count=1, finished_at=?, updated_at=? WHERE id=?`,
		now2, now2, taskID)
	return nil
}

func (m *MailService) Test(recipient string) error {
	if _, err := mail.ParseAddress(recipient); err != nil {
		return respond.NewError("VALIDATION_FAILED", "请输入有效的收件邮箱", 422)
	}
	html, text := RenderNotification(NotificationData{
		Title:     "邮件服务连接成功",
		Preheader: "SMTP 配置已生效",
		Greeting:  "管理员",
		Message:   "这是一封测试邮件。如果你收到了它，说明站点的 SMTP 配置可以正常发信。",
		Facts: [][2]string{
			{"发送时间", time.Now().Format("2006-01-02 15:04:05")},
			{"站点", str(m.settings.Group("site")["name"])},
		},
		Accent: "#6366f1",
	})
	return m.SendTransactional(recipient, "邮件服务连接成功", html, text, "mail_test", 0)
}

func (m *MailService) Summary() (map[string]any, error) {
	var pending, sending, sent, failed, tasks int
	_ = m.store.Read.Get(&pending, `SELECT COUNT(1) FROM TF_mail_records WHERE status='pending'`)
	_ = m.store.Read.Get(&sending, `SELECT COUNT(1) FROM TF_mail_records WHERE status='sending'`)
	_ = m.store.Read.Get(&sent, `SELECT COUNT(1) FROM TF_mail_records WHERE status='sent'`)
	_ = m.store.Read.Get(&failed, `SELECT COUNT(1) FROM TF_mail_records WHERE status='failed'`)
	_ = m.store.Read.Get(&tasks, `SELECT COUNT(1) FROM TF_mail_tasks`)
	cfg, _ := m.config()
	return map[string]any{
		"enabled":     cfg.Enabled,
		"pending":     pending,
		"sending":     sending,
		"sent":        sent,
		"failed":      failed,
		"tasks":       tasks,
		"batch_limit": cfg.BatchLimit,
	}, nil
}

func (m *MailService) Tasks(limit, offset int) (map[string]any, error) {
	rows, err := m.store.Maps(`SELECT id, task_no, user_id, template_code, subject, status, priority,
		total_count, success_count, failed_count, scheduled_at, started_at, finished_at, created_at, updated_at
		FROM TF_mail_tasks ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	var total int
	_ = m.store.Read.Get(&total, `SELECT COUNT(1) FROM TF_mail_tasks`)
	return map[string]any{"items": rows, "total": total, "limit": limit, "offset": offset}, nil
}

func (m *MailService) Records(taskNo string) (map[string]any, error) {
	var taskID int64
	if err := m.store.Read.Get(&taskID, `SELECT id FROM TF_mail_tasks WHERE task_no = ?`, taskNo); err != nil {
		return nil, respond.NotFound("邮件任务不存在")
	}
	items, err := m.store.Maps(`SELECT id, task_id, message_no, recipient, status, attempts, max_attempts,
		provider_message_id, last_error_code, last_error_message, sent_at, created_at
		FROM TF_mail_records WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_no": taskNo, "items": items}, nil
}

func (m *MailService) AllRecords(keyword, status, templateCode string, limit, offset int) (map[string]any, error) {
	where := []string{"1=1"}
	args := []any{}
	if keyword != "" {
		where = append(where, "(r.recipient LIKE ? OR r.message_no LIKE ? OR r.subject LIKE ?)")
		like := "%" + keyword + "%"
		args = append(args, like, like, like)
	}
	if status != "" {
		where = append(where, "r.status = ?")
		args = append(args, status)
	}
	if templateCode != "" {
		where = append(where, "t.template_code = ?")
		args = append(args, templateCode)
	}
	clause := strings.Join(where, " AND ")
	countArgs := append([]any{}, args...)
	args = append(args, limit, offset)
	items, err := m.store.Maps(`SELECT r.id, r.task_id, r.message_no, r.recipient, r.subject, r.status, r.attempts,
		r.provider_message_id, r.last_error_code, r.last_error_message, r.sent_at, r.created_at
		FROM TF_mail_records r LEFT JOIN TF_mail_tasks t ON t.id = r.task_id
		WHERE `+clause+` ORDER BY r.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	var total int
	_ = m.store.Read.Get(&total, `SELECT COUNT(1) FROM TF_mail_records r
		LEFT JOIN TF_mail_tasks t ON t.id = r.task_id WHERE `+clause, countArgs...)
	return map[string]any{"items": items, "total": total, "limit": limit, "offset": offset}, nil
}

func (m *MailService) DeleteRecord(id int64) error {
	_, err := m.store.Write.Exec(`DELETE FROM TF_mail_records WHERE id = ?`, id)
	return err
}

func (m *MailService) Compose(input map[string]any, adminID int64) (map[string]any, error) {
	subject := truncate(strings.TrimSpace(str(input["subject"])), 255)
	html := strings.TrimSpace(str(input["html_body"]))
	text := strings.TrimSpace(str(input["text_body"]))
	if subject == "" || (html == "" && text == "") {
		return nil, respond.NewError("VALIDATION_FAILED", "邮件主题和正文不能为空", 422)
	}
	cfg, err := m.config()
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, respond.NewError("MAIL_DISABLED", "请先启用邮件服务", 409)
	}
	recipients, err := m.resolveRecipients(input, cfg.BatchLimit)
	if err != nil {
		return nil, err
	}
	if len(recipients) == 0 {
		return nil, respond.NewError("MAIL_RECIPIENT_EMPTY", "没有有效收件人", 422)
	}

	taskNo := secure.RandomHex(16)
	now := database.Now()
	tx, err := m.store.Write.Beginx()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`INSERT INTO TF_mail_tasks
		(task_no, user_id, template_code, subject, context_json, status, priority, total_count, success_count,
		 failed_count, scheduled_at, created_at, updated_at)
		VALUES(?,?, 'admin_custom', ?,?, 'pending', 0, ?, 0, 0, ?,?,?)`,
		taskNo, adminID, subject, mustJSON(map[string]any{"html_body": html, "text_body": text}),
		len(recipients), now, now, now)
	if err != nil {
		return nil, err
	}
	taskID, _ := res.LastInsertId()
	for _, recipient := range recipients {
		lower := strings.ToLower(recipient)
		if _, err := tx.Exec(`INSERT INTO TF_mail_records
			(task_id, message_no, recipient, recipient_hash, subject, body_html, status, attempts, max_attempts, available_at, created_at, updated_at)
			VALUES(?,?,?,?,?,?, 'pending', 0, 3, ?,?,?)`,
			taskID, secure.RandomHex(16), lower, secure.SHA256Hex(lower), subject, html, now, now, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	task, err := m.store.Map(`SELECT * FROM TF_mail_tasks WHERE id = ?`, taskID)
	if err != nil {
		return nil, err
	}
	return task, nil
}

func (m *MailService) resolveRecipients(input map[string]any, limit int) ([]string, error) {
	kind := firstNonEmpty(strings.TrimSpace(str(input["recipient_type"])), "emails")
	var emails []string
	switch kind {
	case "all_active":
		_ = m.store.Read.Select(&emails, `SELECT email FROM TF_users
			WHERE status='active' AND email IS NOT NULL AND email <> '' LIMIT ?`, limit+1)
	case "user_ids":
		ids := toIntSlice(input["user_ids"])
		if len(ids) > 0 {
			query, args, err := sqlxIn(`SELECT email FROM TF_users WHERE email IS NOT NULL AND email <> '' AND id IN `, ids)
			if err != nil {
				return nil, err
			}
			_ = m.store.Read.Select(&emails, query, args...)
		}
	default:
		var raw []string
		if arr, ok := input["emails"].([]any); ok {
			for _, v := range arr {
				raw = append(raw, str(v))
			}
		} else {
			raw = strings.FieldsFunc(str(input["emails"]), func(r rune) bool {
				return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
			})
		}
		for _, v := range raw {
			if v = strings.TrimSpace(v); v != "" {
				if _, err := mail.ParseAddress(v); err == nil {
					emails = append(emails, v)
				}
			}
		}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(emails))
	for _, e := range emails {
		lower := strings.ToLower(strings.TrimSpace(e))
		if lower == "" || seen[lower] {
			continue
		}
		seen[lower] = true
		out = append(out, lower)
	}
	if len(out) > limit {
		return nil, respond.NewError("MAIL_BATCH_LIMIT_EXCEEDED", fmt.Sprintf("单次最多发送 %d 封邮件", limit), 422)
	}
	return out, nil
}

// ProcessPending 由 mail worker 周期调用。
func (m *MailService) ProcessPending(batch int) int {
	if batch <= 0 {
		batch = 20
	}
	cfg, err := m.config()
	if err != nil || !cfg.Enabled {
		return 0
	}
	type record struct {
		ID        int64   `db:"id"`
		TaskID    *int64  `db:"task_id"`
		Recipient string  `db:"recipient"`
		Subject   string  `db:"subject"`
		BodyHTML  *string `db:"body_html"`
		Attempts  int     `db:"attempts"`
	}
	var rows []record
	now := database.Now()
	if err := m.store.Read.Select(&rows, `SELECT id, task_id, recipient, subject, COALESCE(body_html,'') AS body_html, attempts
		FROM TF_mail_records WHERE status='pending' AND (available_at IS NULL OR available_at <= ?)
		ORDER BY id LIMIT ?`, now, batch); err != nil {
		return 0
	}
	sent := 0
	for _, row := range rows {
		res, err := m.store.Write.Exec(`UPDATE TF_mail_records SET status='sending', attempts=attempts+1, updated_at=?
			WHERE id=? AND status='pending'`, database.Now(), row.ID)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		body := ""
		if row.BodyHTML != nil {
			body = *row.BodyHTML
		}
		messageID, sendErr := m.sendOne(cfg, row.Recipient, row.Subject, body, stripTags(body))
		ts := database.Now()
		if sendErr != nil {
			attempts := row.Attempts + 1
			if attempts >= 3 {
				_, _ = m.store.Write.Exec(`UPDATE TF_mail_records SET status='failed', last_error_code='SMTP_SEND_FAILED',
					last_error_message=?, updated_at=? WHERE id=?`, truncate(sendErr.Error(), 500), ts, row.ID)
			} else {
				next := database.Ts(time.Now().Add(time.Duration(attempts*attempts) * time.Minute))
				_, _ = m.store.Write.Exec(`UPDATE TF_mail_records SET status='pending', last_error_code='SMTP_SEND_FAILED',
					last_error_message=?, available_at=?, updated_at=? WHERE id=?`,
					truncate(sendErr.Error(), 500), next, ts, row.ID)
			}
			m.logger.Warn("mail delivery failed", slog.Int64("record", row.ID), slog.String("error", sendErr.Error()))
			continue
		}
		_, _ = m.store.Write.Exec(`UPDATE TF_mail_records SET status='sent', provider_message_id=?, sent_at=?, updated_at=? WHERE id=?`,
			messageID, ts, ts, row.ID)
		sent++
	}
	if len(rows) > 0 {
		m.refreshTaskStats()
	}
	return sent
}

func (m *MailService) refreshTaskStats() {
	_, _ = m.store.Write.Exec(`UPDATE TF_mail_tasks SET
		success_count = (SELECT COUNT(1) FROM TF_mail_records r WHERE r.task_id = TF_mail_tasks.id AND r.status='sent'),
		failed_count  = (SELECT COUNT(1) FROM TF_mail_records r WHERE r.task_id = TF_mail_tasks.id AND r.status='failed'),
		status = CASE
			WHEN (SELECT COUNT(1) FROM TF_mail_records r WHERE r.task_id = TF_mail_tasks.id AND r.status IN ('pending','sending')) > 0 THEN 'running'
			WHEN (SELECT COUNT(1) FROM TF_mail_records r WHERE r.task_id = TF_mail_tasks.id AND r.status='failed') > 0 THEN 'partial'
			ELSE 'succeeded' END,
		finished_at = CASE
			WHEN (SELECT COUNT(1) FROM TF_mail_records r WHERE r.task_id = TF_mail_tasks.id AND r.status IN ('pending','sending')) > 0 THEN NULL
			ELSE ? END,
		updated_at = ?
		WHERE status IN ('pending','running','partial')`, database.Now(), database.Now())
}

func (m *MailService) Retry(taskNo string) (map[string]any, error) {
	var taskID int64
	if err := m.store.Read.Get(&taskID, `SELECT id FROM TF_mail_tasks WHERE task_no = ?`, taskNo); err != nil {
		return nil, respond.NotFound("邮件任务不存在")
	}
	res, err := m.store.Write.Exec(`UPDATE TF_mail_records SET status='pending', attempts=0, available_at=?, updated_at=?
		WHERE task_id=? AND status='failed'`, database.Now(), database.Now(), taskID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	_, _ = m.store.Write.Exec(`UPDATE TF_mail_tasks SET status='pending', updated_at=? WHERE id=?`, database.Now(), taskID)
	return map[string]any{"task_no": taskNo, "requeued": n}, nil
}

// ---------- 模板渲染 ----------

type NotificationData struct {
	Title     string
	Preheader string
	Greeting  string
	Message   string
	Facts     [][2]string
	Accent    string
	Action    string
}

func RenderNotification(d NotificationData) (string, string) {
	accent := firstNonEmpty(d.Accent, "#6366f1")
	var facts strings.Builder
	for _, kv := range d.Facts {
		facts.WriteString(fmt.Sprintf(
			`<tr><td style="padding:10px 0;border-bottom:1px solid #eceefa;color:#8b90a6;font-size:13px;">%s</td>`+
				`<td style="padding:10px 0;border-bottom:1px solid #eceefa;color:#2b2f3d;font-size:14px;font-weight:600;text-align:right;">%s</td></tr>`,
			escapeHTML(kv[0]), escapeHTML(kv[1])))
	}
	action := ""
	if d.Action != "" {
		action = fmt.Sprintf(`<p style="margin:22px 0 0;color:#8b90a6;font-size:13px;">%s</p>`, escapeHTML(d.Action))
	}
	html := fmt.Sprintf(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head>
<body style="margin:0;padding:36px 16px;background:#f4f5fb;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;">
<div style="display:none;max-height:0;overflow:hidden;">%s</div>
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="max-width:620px;margin:0 auto;background:#ffffff;border-radius:24px;overflow:hidden;box-shadow:0 18px 50px rgba(32,35,52,.10);">
<tr><td style="padding:34px 38px 16px;">
  <div style="display:inline-block;padding:7px 12px;border-radius:999px;background:#f0f1ff;color:%s;font-size:12px;font-weight:700;letter-spacing:.04em;">天方云签</div>
  <h1 style="margin:18px 0 0;font-size:22px;line-height:1.35;color:#1f2333;">%s</h1>
</td></tr>
<tr><td style="padding:0 38px 36px;">
  <div style="margin-top:14px;padding:20px 22px;border:1px solid #eceefa;border-radius:16px;background:#fafaff;color:#54596d;font-size:14px;line-height:1.9;">
    <p style="margin:0 0 6px;font-weight:600;color:#2b2f3d;">%s，你好</p>
    <p style="margin:0;">%s</p>
  </div>
  <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="margin-top:18px;">%s</table>
  %s
  <p style="margin:26px 0 0;padding-top:18px;border-top:1px solid #f0f1f8;color:#a6abbd;font-size:12px;line-height:1.8;">
    此邮件由系统自动发送，请勿直接回复。<br>如果不是本人操作，请忽略本邮件。
  </p>
</td></tr>
</table>
</body></html>`, escapeHTML(d.Preheader), accent, escapeHTML(d.Title), escapeHTML(d.Greeting), escapeHTML(d.Message), facts.String(), action)

	var text strings.Builder
	text.WriteString(d.Title + "\n\n")
	text.WriteString(d.Greeting + "，你好\n")
	text.WriteString(d.Message + "\n\n")
	for _, kv := range d.Facts {
		text.WriteString(kv[0] + "：" + kv[1] + "\n")
	}
	if d.Action != "" {
		text.WriteString("\n" + d.Action + "\n")
	}
	text.WriteString("\n此邮件由系统自动发送，请勿直接回复。\n")
	return html, text.String()
}

func escapeHTML(v string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(v)
}

func stripTags(v string) string {
	var b strings.Builder
	inTag := false
	for _, r := range v {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func truncate(v string, max int) string {
	runes := []rune(v)
	if len(runes) <= max {
		return v
	}
	return string(runes[:max])
}

func nullableID(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func mustJSON(v any) string {
	b, err := marshalNoEscape(v)
	if err != nil {
		return "{}"
	}
	return b
}

func toIntSlice(v any) []int64 {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]int64, 0, len(arr))
	for _, item := range arr {
		switch t := item.(type) {
		case float64:
			out = append(out, int64(t))
		case string:
			if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
				out = append(out, n)
			}
		case int:
			out = append(out, int64(t))
		}
	}
	return out
}

func sqlxIn(prefix string, ids []int64) (string, []any, error) {
	if len(ids) == 0 {
		return "", nil, fmt.Errorf("empty ids")
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return prefix + "(" + strings.Join(placeholders, ",") + ")", args, nil
}
