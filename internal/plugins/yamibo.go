package plugins

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"

	"tfcloud/internal/sign"
)

const yamiboBaseURL = "https://bbs.yamibo.com"

type Yamibo struct{ http *sign.HTTP }

func NewYamibo(h *sign.HTTP) *Yamibo { return &Yamibo{http: h} }

func (p *Yamibo) Metadata() sign.Metadata {
	return sign.Metadata{
		Code: "yamibo", Name: "Yamibo（百合会 / 300）", Version: "0.1.0",
		Description:     "Yamibo 论坛每日打卡，查询签到统计与账户积分",
		CredentialTypes: []string{"cookie"},
		Status:          "ready",
	}
}

func (p *Yamibo) CredentialRules() []sign.CredentialRule {
	return []sign.CredentialRule{{Key: "cookie", Required: true}}
}

func (p *Yamibo) SupportedActions() []string { return []string{"daily_sign"} }

func (p *Yamibo) ValidateAccount(creds map[string]any) (*sign.Profile, error) {
	c := newYamiboClient(p.http, creds)
	if c.cookie == "" {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据：cookie", 422)
	}
	page, err := c.signPage(context.Background())
	if err != nil {
		return nil, err
	}
	account := yamiboAccount(page.text, c.cookie)
	if account.Username == "" {
		if text, err := c.creditPage(context.Background()); err == nil {
			account = mergeYamiboAccount(account, yamiboAccount(text, c.cookie))
		}
	}
	name := account.Username
	if name == "" {
		name = "Yamibo 账号"
		if account.UID != "" {
			name = "Yamibo · " + account.UID
		}
	}
	return &sign.Profile{
		ExternalID:  account.UID,
		DisplayName: name,
		Extra: map[string]any{
			"username":     account.Username,
			"uid":          account.UID,
			"signed_today": page.alreadySigned,
		},
	}, nil
}

func (p *Yamibo) Execute(ctx context.Context, c *sign.Context) (*sign.Result, error) {
	if c.Action != "daily_sign" {
		return nil, sign.Fail("PLUGIN_ACTION_NOT_SUPPORTED", "Yamibo 插件不支持该动作", 400)
	}
	client := newYamiboClient(p.http, c.Credentials)
	if client.cookie == "" {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据：cookie", 422)
	}

	page, err := client.signPage(ctx)
	if err != nil {
		return nil, err
	}
	account := yamiboAccount(page.text, client.cookie)
	record := sign.Record{
		Key: "yamibo:daily_sign", Action: "daily_sign", Status: "succeeded",
		TargetType: "account", TargetName: firstText(account.Username, "Yamibo"), Message: "打卡成功",
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
				account = mergeYamiboAccount(account, yamiboAccount(latest.text, client.cookie))
			}
		}
	default:
		record.Status = "failed"
		record.Code = "YAMIBO_SIGN_HASH_MISSING"
		record.Message = "未能从签到页面获取打卡参数"
	}

	stats := yamiboStats(pageText)
	creditText, creditErr := client.creditPage(ctx)
	credit := yamiboCredit(creditText)
	if account.Username == "" {
		account = mergeYamiboAccount(account, yamiboAccount(creditText, client.cookie))
	}
	if account.Username != "" {
		record.TargetName = account.Username
	}
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
	result.Summary = map[string]any{"stats": stats, "credit": credit, "username": account.Username, "uid": account.UID}
	if account.Username != "" || account.UID != "" {
		result.ProfilePatch = map[string]any{"username": account.Username, "uid": account.UID, "display_name": account.Username}
	}
	return result, nil
}

// ---------- 客户端 ----------

type yamiboClient struct {
	http   *sign.HTTP
	cookie string
}

func newYamiboClient(h *sign.HTTP, creds map[string]any) *yamiboClient {
	return &yamiboClient{http: h, cookie: strings.TrimSpace(sign.ValueString(creds["cookie"]))}
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

func (c *yamiboClient) creditPage(ctx context.Context) (string, error) {
	resp, err := c.get(ctx, "/home.php?mod=spacecp&ac=credit", yamiboBaseURL+"/plugin.php?id=zqlj_sign")
	if err != nil {
		return "", err
	}
	return string(resp.Body), nil
}

func yamiboCredit(text string) string {
	block := yamiboCreditBlock.FindStringSubmatch(text)
	if len(block) < 2 {
		return ""
	}
	seen := map[string]bool{}
	parts := []string{}
	for _, item := range yamiboCreditItem.FindAllStringSubmatch(block[1], -1) {
		value := strings.TrimSpace(html.UnescapeString(yamiboTagPattern.ReplaceAllString(item[1], "")))
		idx := strings.IndexAny(value, ":：")
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(value[:idx])
		amount := strings.TrimSpace(strings.TrimLeft(value[idx:], ":："))
		if name == "" || amount == "" || seen[name] {
			continue
		}
		seen[name] = true
		parts = append(parts, fmt.Sprintf("%s %s", name, amount))
	}
	return strings.Join(parts, "，")
}

func yamiboStats(text string) map[string]string {
	out := map[string]string{}
	for _, item := range yamiboStatsPattern.FindAllStringSubmatch(text, -1) {
		out[item[1]] = strings.TrimSpace(html.UnescapeString(item[2]))
	}
	return out
}

type yamiboAccountInfo struct {
	UID      string
	Username string
}

func yamiboAccount(text, cookie string) yamiboAccountInfo {
	info := yamiboAccountInfo{
		UID: yamiboCookieValue(cookie, "discuz_uid"),
	}
	if info.UID == "" {
		info.UID = yamiboCookieValue(cookie, "uid")
	}
	if match := yamiboProfileNamePattern.FindStringSubmatch(text); len(match) > 1 {
		info.Username = strings.TrimSpace(html.UnescapeString(match[1]))
	}
	for _, match := range yamiboProfileLinkPattern.FindAllStringSubmatch(text, -1) {
		uid := strings.TrimSpace(match[1])
		name := strings.TrimSpace(html.UnescapeString(match[2]))
		if info.Username == "" && name != "" && name != "匿名" {
			info.Username = name
			if info.UID == "" {
				info.UID = uid
			}
		} else if info.UID == "" && info.Username != "" && name == info.Username {
			// 只在链接名称与 vwmy 用户名一致时补 UID，避免把页面里其他用户当成当前账号。
			info.UID = uid
		}
		if info.UID != "" && info.Username != "" {
			break
		}
	}
	return info
}

func mergeYamiboAccount(base, patch yamiboAccountInfo) yamiboAccountInfo {
	if base.UID == "" {
		base.UID = patch.UID
	}
	if base.Username == "" {
		base.Username = patch.Username
	}
	return base
}

var (
	yamiboSignPattern        = regexp.MustCompile(`sign=([a-f0-9]+)`)
	yamiboButtonPattern      = regexp.MustCompile(`class="btna"[^>]*>([^<]+)<`)
	yamiboCreditBlock        = regexp.MustCompile(`(?s)<ul class="creditl mtm bbda cl">(.*?)</ul>`)
	yamiboCreditItem         = regexp.MustCompile(`(?s)<li[^>]*>(.*?)</li>`)
	yamiboTagPattern         = regexp.MustCompile(`<[^>]*>`)
	yamiboStatsPattern       = regexp.MustCompile(`(最近打卡|本月打卡|连续打卡|累计打卡|最近奖励)：([^<]+)`)
	yamiboProfileLinkPattern = regexp.MustCompile(`(?s)<a[^>]+href="(?:home\.php\?mod=space&(?:amp;)?uid=|space-uid-)(\d+)[^"]*"[^>]*>([^<]+)</a>`)
	yamiboProfileNamePattern = regexp.MustCompile(`(?s)class="vwmy"[^>]*>\s*<a[^>]*>([^<]+)</a>`)
)
