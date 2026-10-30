package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tfcloud/internal/httpapi/respond"
)

var pushHTTPClient = &http.Client{Timeout: 15 * time.Second}

func sendPush(channel, token, title, body string) error {
	channel = strings.TrimSpace(channel)
	token = strings.TrimSpace(token)
	if channel == "" || channel == "none" || token == "" {
		return fmt.Errorf("推送渠道或令牌未配置")
	}
	title = truncate(title, 120)
	body = truncate(body, 2000)

	var req *http.Request
	var err error
	switch channel {
	case "wecom":
		endpoint := "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=" + url.QueryEscape(token)
		req, err = newJSONRequest(endpoint, map[string]any{
			"msgtype": "text", "text": map[string]any{"content": title + "\n" + body},
		})
	case "serverchan":
		form := url.Values{"title": {title}, "desp": {body}}
		req, err = http.NewRequest(http.MethodPost, "https://sctapi.ftqq.com/"+url.PathEscape(token)+".send",
			strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	case "pushplus":
		req, err = newJSONRequest("https://www.pushplus.plus/send", map[string]any{
			"token": token, "title": title, "content": body, "template": "txt",
		})
	case "bark":
		endpoint, barkErr := barkEndpoint(token, title, body)
		if barkErr != nil {
			return barkErr
		}
		req, err = http.NewRequest(http.MethodGet, endpoint, nil)
	default:
		return fmt.Errorf("不支持的推送渠道")
	}
	if err != nil {
		return fmt.Errorf("推送请求创建失败")
	}
	resp, err := pushHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("推送服务暂时不可用")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("推送服务返回 HTTP %d", resp.StatusCode)
	}
	return pushResultError(channel, raw)
}

func pushResultError(channel string, raw []byte) error {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil
	}
	code := func(key string) int {
		switch value := body[key].(type) {
		case float64:
			return int(value)
		case string:
			n := 0
			_, _ = fmt.Sscanf(value, "%d", &n)
			return n
		default:
			return 0
		}
	}
	switch channel {
	case "wecom":
		if code("errcode") != 0 {
			return fmt.Errorf("企业微信推送失败")
		}
	case "pushplus":
		if code("code") != 0 && code("code") != 200 {
			return fmt.Errorf("PushPlus 推送失败")
		}
	case "serverchan":
		if code("code") != 0 {
			return fmt.Errorf("Server酱推送失败")
		}
	case "bark":
		if codeValue, ok := body["code"]; ok {
			if n := code("code"); n != 0 && n != 200 {
				return fmt.Errorf("bark 推送失败: %v", codeValue)
			}
		}
	}
	return nil
}

func newJSONRequest(endpoint string, payload any) (*http.Request, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(raw))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, err
}

func barkEndpoint(token, title, body string) (string, error) {
	if strings.HasPrefix(token, "http://") || strings.HasPrefix(token, "https://") {
		parsed, err := url.Parse(token)
		if err != nil || parsed.Scheme != "https" || parsed.Host != "api.day.app" ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", fmt.Errorf("bark 完整地址必须使用 https://api.day.app/")
		}
		base := strings.TrimRight(parsed.String(), "/")
		return base + "/" + url.PathEscape(title) + "/" + url.PathEscape(body), nil
	}
	return "https://api.day.app/" + url.PathEscape(token) + "/" + url.PathEscape(title) + "/" + url.PathEscape(body), nil
}

func (s *AuthService) TestPush(userID int64) error {
	var row struct {
		Channel string  `db:"push_channel"`
		Token   *string `db:"push_token"`
	}
	if err := s.store.Read.Get(&row, `SELECT push_channel, push_token FROM TF_users WHERE id=?`, userID); err != nil {
		return respond.NotFound("用户不存在")
	}
	token := ""
	if row.Token != nil {
		token = *row.Token
	}
	return sendPush(row.Channel, token, "推送测试", "这是一条来自天方云签的测试推送，收到即表示渠道配置正常。")
}
