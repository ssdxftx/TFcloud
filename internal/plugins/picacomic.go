package plugins

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tfcloud/internal/sign"
)

const (
	picaBaseURL      = "https://picaapi.picacomic.com/"
	picaAPIKey       = "C69BAF41DA5ABD1FFEDC6D2FEA56B"
	picaSignatureKey = "~d}$Q7$eIni=V)9\\RK/P.RM4;9[7|@/CA}b~OW!3?EV`:<>M7pddUBL5n|0/*Cn"
	picaAppVersion   = "2.2.1.2.3.4"
	picaBuildVersion = "45"
)

type Picacomic struct{ http *sign.HTTP }

func NewPicacomic(h *sign.HTTP) *Picacomic { return &Picacomic{http: h} }

func (p *Picacomic) Metadata() sign.Metadata {
	return sign.Metadata{
		Code: "picacomic", Name: "哔咔漫画", Version: "0.1.0",
		Description:     "哔咔漫画账号登录与每日签到",
		CredentialTypes: []string{"account_password"},
		Status:          "ready",
	}
}

func (p *Picacomic) CredentialRules() []sign.CredentialRule {
	return []sign.CredentialRule{{Key: "username", Required: true}, {Key: "password", Required: true}}
}

func (p *Picacomic) SupportedActions() []string { return []string{"daily_sign"} }

func (p *Picacomic) ValidateAccount(creds map[string]any) (*sign.Profile, error) {
	c := &picaClient{http: p.http, creds: cloneMap(creds)}
	session, err := c.authenticate(context.Background())
	if err != nil {
		return nil, err
	}
	profile := sign.Map(session["profile"])
	return &sign.Profile{
		ExternalID:  c.accountIdentifier(session),
		DisplayName: firstText(profile["name"], "哔咔漫画账号"),
		Extra:       map[string]any{"level": sign.ValueInt(profile["level"])},
	}, nil
}

func (p *Picacomic) Execute(ctx context.Context, c *sign.Context) (*sign.Result, error) {
	client := &picaClient{http: p.http, creds: cloneMap(c.Credentials)}
	rec, err := client.dailySign(ctx)
	if err != nil {
		return nil, err
	}
	status := "succeeded"
	if rec.Status == "failed" {
		status = "failed"
	}
	result := &sign.Result{Status: status, Message: rec.Message, Records: []sign.Record{rec},
		Summary: map[string]any{"experience": rec.Rewards["experience"]}}
	if refreshed := client.refreshed(); refreshed != nil {
		result.Credentials = refreshed
	}
	return result, nil
}

type picaClient struct {
	http    *sign.HTTP
	creds   map[string]any
	session map[string]any
	changed bool
}

func (c *picaClient) refreshed() map[string]any {
	if !c.changed {
		return nil
	}
	return c.creds
}

func (c *picaClient) signHeaders(relative, method, token string) map[string]string {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := randHex(16)
	payload := strings.ToLower(relative + timestamp + nonce + strings.ToUpper(method) + picaAPIKey)
	mac := hmac.New(sha256.New, []byte(picaSignatureKey))
	mac.Write([]byte(payload))
	headers := map[string]string{
		"api-key":           picaAPIKey,
		"accept":            "application/vnd.picacomic.com.v1+json",
		"signature":         hex.EncodeToString(mac.Sum(nil)),
		"app-channel":       "1",
		"time":              timestamp,
		"app-version":       picaAppVersion,
		"app-build-version": picaBuildVersion,
		"nonce":             nonce,
		"app-platform":      "android",
		"app-uuid":          "defaultUuid",
		"User-Agent":        "okhttp/3.8.1",
		"Host":              "picaapi.picacomic.com",
		"image-quality":     "original",
	}
	if token != "" {
		headers["authorization"] = token
	}
	return headers
}

func (c *picaClient) apiRequest(ctx context.Context, method, relative string, body any, token string, loginRequest bool) (map[string]any, error) {
	opts := sign.RequestOptions{Headers: c.signHeaders(relative, method, token)}
	if method == "POST" {
		opts.Headers["Content-Type"] = "application/json; charset=UTF-8"
		if body != nil {
			opts.JSONBody = body
		} else {
			opts.RawBody = []byte(`{}`)
		}
	}
	resp, err := c.http.Do(ctx, method, picaBaseURL+relative, opts)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.Status == 429:
		return nil, sign.Fail("PICACOMIC_RATE_LIMITED", "哔咔请求过于频繁，请稍后再试", 429)
	case resp.Status == 401 || resp.Status == 403:
		return nil, sign.Fail("PICACOMIC_UNAUTHORIZED", "哔咔登录令牌无效", 401)
	case resp.Status == 0 || resp.Status >= 500:
		return nil, sign.Fail("UPSTREAM_HTTP_ERROR", "哔咔服务暂时不可用", 502)
	}
	if resp.JSON == nil {
		return nil, sign.Fail("PICACOMIC_RESPONSE_INVALID", "哔咔返回格式错误", 502)
	}
	success := sign.ValueInt(resp.JSON["code"]) == 200 &&
		(sign.ValueInt(resp.JSON["error"]) == 0 || resp.JSON["error"] == nil) &&
		firstText(resp.JSON["message"]) == "success"
	if !success {
		message := firstText(resp.JSON["message"], resp.JSON["detail"], "请求失败")
		if loginRequest {
			return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "哔咔账号或密码不正确："+message, 422)
		}
		lower := strings.ToLower(message)
		if strings.Contains(lower, "token") || strings.Contains(lower, "unauthorized") || strings.Contains(message, "登录") {
			return nil, sign.Fail("PICACOMIC_UNAUTHORIZED", "哔咔登录令牌无效", 401)
		}
		return nil, sign.Fail("PICACOMIC_API_ERROR", "哔咔请求失败："+message, 502)
	}
	out := sign.Map(resp.JSON["data"])
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func (c *picaClient) authenticate(ctx context.Context) (map[string]any, error) {
	if c.session != nil {
		return c.session, nil
	}
	if strings.TrimSpace(sign.ValueString(c.creds["username"])) == "" || sign.ValueString(c.creds["password"]) == "" {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "哔咔账号和密码不能为空", 422)
	}
	if token := strings.TrimSpace(sign.ValueString(c.creds["token"])); token != "" {
		if profile, err := c.profile(ctx, token); err == nil {
			c.session = map[string]any{"token": token, "profile": profile}
			return c.session, nil
		} else if e, ok := err.(*sign.Error); !ok || e.Code != "PLUGIN_CREDENTIAL_EXPIRED" {
			return nil, err
		}
	}

	login, err := c.apiRequest(ctx, "POST", "auth/sign-in", map[string]any{
		"email":    strings.TrimSpace(sign.ValueString(c.creds["username"])),
		"password": sign.ValueString(c.creds["password"]),
	}, "", true)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(sign.ValueString(login["token"]))
	if token == "" {
		return nil, sign.Fail("PICACOMIC_LOGIN_FAILED", "哔咔登录未返回有效令牌", 502)
	}
	profile, err := c.profile(ctx, token)
	if err != nil {
		return nil, err
	}
	c.creds["token"] = token
	c.creds["user_id"] = sign.ValueString(profile["_id"])
	c.creds["display_name"] = sign.ValueString(profile["name"])
	c.creds["token_updated_at"] = timeNowISO()
	c.changed = true
	c.session = map[string]any{"token": token, "profile": profile}
	return c.session, nil
}

func (c *picaClient) profile(ctx context.Context, token string) (map[string]any, error) {
	data, err := c.apiRequest(ctx, "GET", "users/profile", nil, token, false)
	if err != nil {
		if e, ok := err.(*sign.Error); ok && (e.Code == "PICACOMIC_UNAUTHORIZED" || e.Code == "PICACOMIC_API_ERROR") {
			return nil, sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "哔咔登录状态已经失效", 422)
		}
		return nil, err
	}
	profile := sign.Map(data["user"])
	if profile == nil || sign.ValueString(profile["_id"]) == "" {
		return nil, sign.Fail("PICACOMIC_PROFILE_INVALID", "哔咔用户资料返回格式错误", 502)
	}
	return profile, nil
}

func (c *picaClient) accountIdentifier(session map[string]any) string {
	profile := sign.Map(session["profile"])
	if id := strings.TrimSpace(sign.ValueString(profile["_id"])); id != "" {
		return id
	}
	return strings.TrimSpace(sign.ValueString(c.creds["user_id"]))
}

func (c *picaClient) dailySign(ctx context.Context) (sign.Record, error) {
	session, err := c.authenticate(ctx)
	if err != nil {
		return sign.Record{}, err
	}
	token := sign.ValueString(session["token"])
	profile := sign.Map(session["profile"])
	if profile == nil {
		if profile, err = c.profile(ctx, token); err != nil {
			return sign.Record{}, err
		}
	}
	userID := c.accountIdentifier(session)
	if sign.ValueBool(profile["isPunched"]) {
		return picaRecord("already_done", userID, profile, 0, "哔咔今日已签到"), nil
	}
	beforeExp := sign.ValueInt(profile["exp"])
	if beforeExp < 0 {
		beforeExp = 0
	}

	if _, err := c.apiRequest(ctx, "POST", "users/punch-in", map[string]any{}, token, false); err != nil {
		return sign.Record{}, err
	}
	after, err := c.profile(ctx, token)
	if err != nil {
		return sign.Record{}, err
	}
	if !sign.ValueBool(after["isPunched"]) {
		return sign.Record{}, sign.Fail("PICACOMIC_SIGN_FAILED", "哔咔签到失败", 502)
	}
	experience := sign.ValueInt(after["exp"]) - beforeExp
	if experience < 0 {
		experience = 0
	}
	return picaRecord("succeeded", userID, after, experience, "哔咔签到成功"), nil
}

func picaRecord(status, userID string, profile map[string]any, experience int, message string) sign.Record {
	return sign.Record{
		Key: "picacomic:daily_sign:" + dateToday(), Action: "daily_sign", Status: status,
		TargetType: "account", TargetID: userID, TargetName: firstText(profile["name"], "哔咔漫画账号"),
		Code: "0", Message: message,
		Rewards: map[string]any{"experience": experience},
		Metrics: map[string]any{
			"level":      sign.ValueInt(profile["level"]),
			"experience": sign.ValueInt(profile["exp"]),
		},
	}
}

var _ = url.Values{}
