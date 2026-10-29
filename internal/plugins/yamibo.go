package plugins

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"tfcloud/internal/sign"
)

const yamiboBaseURL = "https://bbs.yamibo.com"

type Yamibo struct{ http *sign.HTTP }

func NewYamibo(h *sign.HTTP) *Yamibo { return &Yamibo{http: h} }

func (p *Yamibo) Metadata() sign.Metadata {
	return sign.Metadata{
		Code: "yamibo", Name: "Yamibo（百合会 / 300）", Version: "0.1.0",
		Description:     "Yamibo 论坛每日打卡，查询签到统计与账户积分",
		CredentialTypes: []string{"cookie", "account_password"},
		Status:          "ready",
	}
}

func (p *Yamibo) CredentialRules() []sign.CredentialRule {
	return []sign.CredentialRule{{Key: "cookie", Required: true}}
}

func (p *Yamibo) SupportedActions() []string { return []string{"daily_sign"} }

func (p *Yamibo) ValidateAccount(creds map[string]any) (*sign.Profile, error) {
	profile, _, err := p.ValidateAccountWithRefresh(creds)
	return profile, err
}

func (p *Yamibo) ValidateCredentialShape(creds map[string]any) error {
	if strings.TrimSpace(sign.ValueString(creds["cookie"])) != "" {
		return nil
	}
	username := strings.TrimSpace(sign.ValueString(creds["username"]))
	password := sign.ValueString(creds["password"])
	if username != "" && password != "" {
		return nil
	}
	return sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据：Cookie 或账号密码", 422)
}

func (p *Yamibo) ValidateAccountWithRefresh(creds map[string]any) (*sign.Profile, map[string]any, error) {
	c := newYamiboClient(p.http, creds)
	var refreshed map[string]any
	if c.username != "" && c.password != "" {
		cookie, err := c.login(context.Background(), c.username, c.password)
		if err != nil {
			return nil, nil, err
		}
		c.cookie = cookie
		refreshed = map[string]any{"cookie": cookie}
	}
	if c.cookie == "" {
		return nil, nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据：cookie", 422)
	}
	page, err := c.signPage(context.Background())
	if err != nil {
		return nil, nil, err
	}
	uid := yamiboCookieValue(c.cookie, "discuz_uid")
	if uid == "" {
		uid = yamiboCookieValue(c.cookie, "uid")
	}
	name := "Yamibo 账号"
	if uid != "" {
		name = "Yamibo · " + uid
	}
	return &sign.Profile{ExternalID: uid, DisplayName: name, Extra: map[string]any{"signed_today": page.alreadySigned}}, refreshed, nil
}

func (p *Yamibo) Execute(ctx context.Context, c *sign.Context) (*sign.Result, error) {
	if c.Action != "daily_sign" {
		return nil, sign.Fail("PLUGIN_ACTION_NOT_SUPPORTED", "Yamibo 插件不支持该动作", 400)
	}
	client := newYamiboClient(p.http, c.Credentials)
	refreshed := map[string]any{}
	if client.cookie == "" {
		if client.username == "" || client.password == "" {
			return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据：Cookie 或账号密码", 422)
		}
		cookie, err := client.login(ctx, client.username, client.password)
		if err != nil {
			return nil, err
		}
		client.cookie = cookie
		refreshed["cookie"] = cookie
	}

	page, err := client.signPage(ctx)
	if err != nil && isYamiboCredentialExpired(err) && client.username != "" && client.password != "" {
		cookie, loginErr := client.login(ctx, client.username, client.password)
		if loginErr != nil {
			return nil, loginErr
		}
		client.cookie = cookie
		refreshed["cookie"] = cookie
		page, err = client.signPage(ctx)
	}
	if err != nil {
		return nil, err
	}
	record := sign.Record{
		Key: "yamibo:daily_sign", Action: "daily_sign", Status: "succeeded",
		TargetType: "account", TargetName: "Yamibo", Message: "打卡成功",
	}
	pageText := page.text
	switch {
	case page.alreadySigned:
		record.Status = "already_done"
		record.Message = "今日已打卡"
	case page.signHash != "":
		result, err := client.checkIn(ctx, page.signHash)
		if err != nil {
			return nil, err
		}
		record.Status = result.status
		record.Code = result.code
		record.Message = result.message
		if result.refresh {
			if latest, err := client.signPage(ctx); err == nil {
				page = latest
				pageText = latest.text
			}
		}
	default:
		record.Status = "failed"
		record.Code = "YAMIBO_SIGN_HASH_MISSING"
		record.Message = "未能从签到页面获取打卡参数"
	}

	stats := yamiboStats(pageText)
	credit, creditErr := client.credit(ctx)
	rewards := map[string]any{}
	for key, value := range stats {
		rewards[key] = value
	}
	if credit != "" {
		rewards["credit"] = credit
	}
	if len(rewards) > 0 {
		record.Rewards = rewards
	}
	if len(stats) > 0 || credit != "" {
		parts := []string{record.Message}
		if value := stats["本月打卡"]; value != "" {
			parts = append(parts, "本月 "+value)
		}
		if value := stats["连续打卡"]; value != "" {
			parts = append(parts, "连续 "+value)
		}
		if credit != "" {
			parts = append(parts, "积分 "+credit)
		}
		record.Message = strings.Join(parts, " · ")
	}
	if creditErr != nil && credit == "" && record.Status == "succeeded" {
		record.Message += " · 积分查询失败"
	}
	c.Emit(record)

	result := &sign.Result{Status: "succeeded", Message: record.Message, Records: []sign.Record{record}}
	if record.Status == "failed" {
		result.Status = "failed"
	}
	result.Summary = map[string]any{"stats": stats, "credit": credit}
	if len(refreshed) > 0 {
		result.Credentials = refreshed
	}
	return result, nil
}

// ---------- 客户端 ----------

type yamiboClient struct {
	http     *sign.HTTP
	cookie   string
	username string
	password string
}

func newYamiboClient(h *sign.HTTP, creds map[string]any) *yamiboClient {
	return &yamiboClient{
		http:     h,
		cookie:   strings.TrimSpace(sign.ValueString(creds["cookie"])),
		username: strings.TrimSpace(sign.ValueString(creds["username"])),
		password: sign.ValueString(creds["password"]),
	}
}

func yamiboCookieValue(cookie, key string) string {
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		prefix := key + "="
		if strings.HasPrefix(part, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(part, prefix))
		}
	}
	return ""
}

func (c *yamiboClient) login(ctx context.Context, username, password string) (string, error) {
	if username == "" || password == "" {
		return "", sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少 Yamibo 用户名或密码", 422)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return "", sign.Fail("INTERNAL_ERROR", "初始化登录会话失败", 500)
	}
	client := &http.Client{Timeout: 25 * time.Second, Jar: jar}
	base, err := url.Parse(yamiboBaseURL)
	if err != nil {
		return "", sign.Fail("INTERNAL_ERROR", "Yamibo 地址无效", 500)
	}
	headers := yamiboRequestHeaders()
	do := func(req *http.Request) (int, []byte, error) {
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, sign.Fail("UPSTREAM_REQUEST_FAILED", "访问 Yamibo 失败", 502)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return 0, nil, sign.Fail("UPSTREAM_REQUEST_FAILED", "读取 Yamibo 响应失败", 502)
		}
		return resp.StatusCode, body, nil
	}
	checkStatus := func(status int) error {
		switch {
		case status == 403 || status == 405 || status == 412:
			return sign.Fail("YAMIBO_WAF_BLOCKED", "Yamibo 触发了 WAF 验证，服务器无法执行浏览器挑战，请改用 Cookie 导入", 502)
		case status == 429 || status >= 500:
			return sign.Fail("UPSTREAM_REQUEST_FAILED", "Yamibo 服务暂时不可用", 502)
		}
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, yamiboBaseURL+"/forum.php", nil)
	if err != nil {
		return "", sign.Fail("UPSTREAM_REQUEST_FAILED", "创建登录请求失败", 502)
	}
	status, _, err := do(req)
	if err != nil {
		return "", err
	}
	if err := checkStatus(status); err != nil {
		return "", err
	}

	loginQuery := url.Values{
		"mod":         {"logging"},
		"action":      {"login"},
		"infloat":     {"yes"},
		"frommessage": {""},
		"inajax":      {"1"},
		"ajaxtarget":  {"messagelogin"},
	}
	loginURL := yamiboBaseURL + "/member.php?" + loginQuery.Encode()
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return "", sign.Fail("UPSTREAM_REQUEST_FAILED", "创建登录请求失败", 502)
	}
	status, body, err := do(req)
	if err != nil {
		return "", err
	}
	if err := checkStatus(status); err != nil {
		return "", err
	}
	text := string(body)
	formhash := ""
	if match := yamiboFormHashPattern.FindStringSubmatch(text); len(match) > 1 {
		formhash = match[1]
	}
	if formhash == "" {
		return "", sign.Fail("YAMIBO_LOGIN_FAILED", "无法获取登录参数，站点可能触发了 WAF 验证", 502)
	}
	loginhash := ""
	if match := yamiboLoginHashPattern.FindStringSubmatch(text); len(match) > 1 {
		loginhash = match[1]
	}

	postQuery := url.Values{
		"mod":         {"logging"},
		"action":      {"login"},
		"loginsubmit": {"yes"},
		"frommessage": {""},
		"loginhash":   {loginhash},
		"inajax":      {"1"},
	}
	form := url.Values{
		"formhash":   {formhash},
		"referer":    {yamiboBaseURL + "/forum.php"},
		"username":   {username},
		"password":   {password},
		"questionid": {"0"},
		"answer":     {""},
		"cookietime": {"2592000"},
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, yamiboBaseURL+"/member.php?"+postQuery.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", sign.Fail("UPSTREAM_REQUEST_FAILED", "创建登录请求失败", 502)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	status, body, err = do(req)
	if err != nil {
		return "", err
	}
	if err := checkStatus(status); err != nil {
		return "", err
	}
	text = string(body)
	if strings.Contains(text, "succeedhandle") || strings.Contains(text, "succeed") {
		cookie := yamiboJarCookie(jar, base)
		if cookie == "" {
			return "", sign.Fail("YAMIBO_LOGIN_FAILED", "登录成功但未获取到 Cookie", 502)
		}
		return cookie, nil
	}
	if strings.Contains(text, "登录失败") {
		message := "Yamibo 用户名或密码错误"
		if match := yamiboErrorMessagePattern.FindStringSubmatch(text); len(match) > 1 {
			if value := strings.TrimSpace(html.UnescapeString(match[1])); value != "" {
				message = value
			}
		}
		return "", sign.Fail("YAMIBO_LOGIN_FAILED", message, 422)
	}
	return "", sign.Fail("YAMIBO_LOGIN_FAILED", "Yamibo 登录失败，页面结构可能已变更", 502)
}

func yamiboRequestHeaders() map[string]string {
	return map[string]string{
		"Accept":           "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
		"Accept-Language":  "zh-CN,zh;q=0.9,en;q=0.8",
		"Origin":           yamiboBaseURL,
		"Referer":          yamiboBaseURL + "/",
		"User-Agent":       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36",
		"X-Requested-With": "XMLHttpRequest",
	}
}

func yamiboJarCookie(jar http.CookieJar, base *url.URL) string {
	parts := []string{}
	for _, cookie := range jar.Cookies(base) {
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

func isYamiboCredentialExpired(err error) bool {
	var target *sign.Error
	return errors.As(err, &target) && target.Code == "PLUGIN_CREDENTIAL_EXPIRED"
}

func (c *yamiboClient) get(ctx context.Context, path, referer string) (*sign.Response, error) {
	if referer == "" {
		referer = yamiboBaseURL + "/"
	}
	resp, err := c.http.Get(ctx, yamiboBaseURL+path, sign.RequestOptions{
		Cookie:  c.cookie,
		Referer: referer,
		Headers: map[string]string{
			"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36",
		},
	})
	if err != nil {
		return nil, err
	}
	if resp.Status == 403 || resp.Status == 405 || resp.Status == 412 {
		return nil, sign.Fail("YAMIBO_WAF_BLOCKED", "Yamibo 触发了 WAF 验证，请在浏览器登录并通过验证后重新导入 Cookie", 502)
	}
	if resp.Status == 429 || resp.Status >= 500 {
		return nil, sign.Fail("UPSTREAM_REQUEST_FAILED", "Yamibo 服务暂时不可用", 502)
	}
	return resp, nil
}

type yamiboPage struct {
	signHash      string
	alreadySigned bool
	text          string
}

func (c *yamiboClient) signPage(ctx context.Context) (*yamiboPage, error) {
	resp, err := c.get(ctx, "/plugin.php?id=zqlj_sign", "")
	if err != nil {
		return nil, err
	}
	text := string(resp.Body)
	if strings.Contains(text, "需要先登录") {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "Yamibo 登录状态已经失效", 422)
	}
	button := ""
	if match := yamiboButtonPattern.FindStringSubmatch(text); len(match) > 1 {
		button = strings.TrimSpace(html.UnescapeString(match[1]))
	}
	if button == "" && !strings.Contains(text, "我的打卡动态") {
		return nil, sign.Fail("YAMIBO_PAGE_BLOCKED", "Yamibo 签到页面被拦截，请更新 Cookie 后重试", 502)
	}
	page := &yamiboPage{
		alreadySigned: strings.Contains(button, "今日已打卡") && !strings.Contains(button, "点击打卡"),
		text:          text,
	}
	if match := yamiboSignPattern.FindStringSubmatch(text); len(match) > 1 {
		page.signHash = match[1]
	}
	return page, nil
}

type yamiboSignResult struct {
	status  string
	code    string
	message string
	refresh bool
}

func (c *yamiboClient) checkIn(ctx context.Context, hash string) (yamiboSignResult, error) {
	resp, err := c.get(ctx, "/plugin.php?id=zqlj_sign&sign="+hash, yamiboBaseURL+"/plugin.php?id=zqlj_sign")
	if err != nil {
		return yamiboSignResult{}, err
	}
	text := string(resp.Body)
	switch {
	case strings.Contains(text, "打卡成功"):
		return yamiboSignResult{status: "succeeded", message: "打卡成功", refresh: true}, nil
	case strings.Contains(text, "打过卡"):
		return yamiboSignResult{status: "already_done", message: "今日已打卡", refresh: true}, nil
	case strings.Contains(text, "需要先登录"):
		return yamiboSignResult{}, sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "Yamibo 登录状态已经失效", 422)
	default:
		return yamiboSignResult{status: "failed", code: "YAMIBO_SIGN_FAILED", message: "打卡失败，未能从页面获取结果"}, nil
	}
}

func (c *yamiboClient) credit(ctx context.Context) (string, error) {
	resp, err := c.get(ctx, "/home.php?mod=spacecp&ac=credit", yamiboBaseURL+"/plugin.php?id=zqlj_sign")
	if err != nil {
		return "", err
	}
	block := yamiboCreditBlock.FindStringSubmatch(string(resp.Body))
	if len(block) < 2 {
		return "", nil
	}
	seen := map[string]bool{}
	parts := []string{}
	for _, item := range yamiboCreditItem.FindAllStringSubmatch(block[1], -1) {
		text := strings.TrimSpace(html.UnescapeString(yamiboTagPattern.ReplaceAllString(item[1], "")))
		idx := strings.IndexAny(text, ":：")
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(text[:idx])
		value := strings.TrimSpace(strings.TrimLeft(text[idx:], ":："))
		if name == "" || value == "" || seen[name] {
			continue
		}
		seen[name] = true
		parts = append(parts, fmt.Sprintf("%s %s", name, value))
	}
	return strings.Join(parts, "，"), nil
}

func yamiboStats(text string) map[string]string {
	out := map[string]string{}
	for _, item := range yamiboStatsPattern.FindAllStringSubmatch(text, -1) {
		out[item[1]] = strings.TrimSpace(html.UnescapeString(item[2]))
	}
	return out
}

var (
	yamiboSignPattern         = regexp.MustCompile(`sign=([a-f0-9]+)`)
	yamiboButtonPattern       = regexp.MustCompile(`class="btna"[^>]*>([^<]+)<`)
	yamiboCreditBlock         = regexp.MustCompile(`(?s)<ul class="creditl mtm bbda cl">(.*?)</ul>`)
	yamiboCreditItem          = regexp.MustCompile(`(?s)<li[^>]*>(.*?)</li>`)
	yamiboTagPattern          = regexp.MustCompile(`<[^>]*>`)
	yamiboStatsPattern        = regexp.MustCompile(`(最近打卡|本月打卡|连续打卡|累计打卡|最近奖励)：([^<]+)`)
	yamiboFormHashPattern     = regexp.MustCompile(`name="formhash"\s+value="([a-f0-9]+)"`)
	yamiboLoginHashPattern    = regexp.MustCompile(`loginhash=([a-zA-Z0-9]+)`)
	yamiboErrorMessagePattern = regexp.MustCompile(`<p[^>]*>([^<]+)</p>`)
)
