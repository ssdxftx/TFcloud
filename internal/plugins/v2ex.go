package plugins

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"tfcloud/internal/sign"
)

const v2exBaseURL = "https://www.v2ex.com"

type V2EX struct{ http *sign.HTTP }

func NewV2EX(h *sign.HTTP) *V2EX { return &V2EX{http: h} }

func (p *V2EX) Metadata() sign.Metadata {
	return sign.Metadata{
		Code: "v2ex", Name: "V2EX", Version: "0.1.0",
		Description:     "V2EX 每日登录奖励领取、连续登录与账户余额查询",
		CredentialTypes: []string{"cookie"},
		Status:          "ready",
	}
}

func (p *V2EX) CredentialRules() []sign.CredentialRule {
	return []sign.CredentialRule{{Key: "cookie", Required: true}}
}

func (p *V2EX) SupportedActions() []string { return []string{"daily_sign"} }

func (p *V2EX) ValidateAccount(creds map[string]any) (*sign.Profile, error) {
	c := newV2EXClient(p.http, creds)
	if c.cookie == "" {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据：cookie", 422)
	}
	_, _, _, text, err := c.mission(context.Background())
	if err != nil {
		return nil, err
	}
	member := ""
	if match := v2exMemberPattern.FindStringSubmatch(text); len(match) > 1 {
		member = match[1]
	}
	name := "V2EX 账号"
	if member != "" {
		name = member
	}
	return &sign.Profile{ExternalID: member, DisplayName: name}, nil
}

func (p *V2EX) Execute(ctx context.Context, c *sign.Context) (*sign.Result, error) {
	if c.Action != "daily_sign" {
		return nil, sign.Fail("PLUGIN_ACTION_NOT_SUPPORTED", "V2EX 插件不支持该动作", 400)
	}
	client := newV2EXClient(p.http, c.Credentials)
	if client.cookie == "" {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据：cookie", 422)
	}

	once, claimed, streak, _, err := client.mission(ctx)
	if err != nil {
		return nil, err
	}

	record := sign.Record{
		Key: "v2ex:daily_sign", Action: "daily_sign", Status: "succeeded",
		TargetType: "account", TargetName: "V2EX", Message: "签到成功",
	}
	switch {
	case claimed:
		record.Status = "already_done"
		record.Message = "今日登录奖励已领取"
	case once != "":
		reward, redeemErr := client.redeem(ctx, once)
		if redeemErr != nil {
			record.Status = "failed"
			record.Code = "V2EX_REDEEM_FAILED"
			record.Message = redeemErr.Error()
		} else if reward.Coins > 0 {
			record.Message = fmt.Sprintf("签到成功，获得 %d 铜币", reward.Coins)
		} else if reward.AlreadyClaimed {
			record.Status = "already_done"
			record.Message = "今日登录奖励已领取"
		} else {
			record.Status = "failed"
			record.Code = "V2EX_SIGN_FAILED"
			record.Message = "签到失败，未能从页面获取奖励信息"
		}
	default:
		record.Status = "failed"
		record.Code = "V2EX_RESPONSE_INVALID"
		record.Message = "无法获取 V2EX 签到参数"
	}

	balance, todayReward, balanceErr := client.balance(ctx)
	if balanceErr == nil {
		if todayReward != "" && record.Status == "failed" {
			record.Status = "succeeded"
			record.Code = ""
			record.Message = "签到成功"
		}
		if todayReward != "" {
			parts := []string{}
			if record.Message != "" {
				parts = append(parts, record.Message)
			}
			parts = append(parts, "今日奖励 "+todayReward)
			record.Message = strings.Join(parts, " · ")
		}
		if balance != "" {
			record.Rewards = map[string]any{"balance": balance}
		}
	}
	if record.Status == "failed" && todayReward == "" {
		_, nowClaimed, _, _, err := client.mission(ctx)
		if err == nil && nowClaimed {
			record.Status = "already_done"
			record.Code = ""
			record.Message = "今日登录奖励已领取"
		}
	}
	if streak != "" {
		if record.Rewards == nil {
			record.Rewards = map[string]any{}
		}
		record.Rewards["streak_days"] = streak
	}
	if todayReward != "" {
		if record.Rewards == nil {
			record.Rewards = map[string]any{}
		}
		record.Rewards["today"] = todayReward
	}
	c.Emit(record)

	result := &sign.Result{Status: "succeeded", Message: record.Message, Records: []sign.Record{record}}
	if record.Status == "failed" {
		result.Status = "failed"
	}
	result.Summary = map[string]any{"balance": balance, "today_reward": todayReward, "streak_days": streak}
	return result, nil
}

// ---------- 客户端 ----------

type v2exClient struct {
	http   *sign.HTTP
	cookie string
}

func newV2EXClient(h *sign.HTTP, creds map[string]any) *v2exClient {
	return &v2exClient{http: h, cookie: normalizeV2EXCookie(strings.TrimSpace(sign.ValueString(creds["cookie"])))}
}

var v2exCookieKeys = []string{"PB3_SESSION", "A2", "V2EX_LANG", "V2EX_TAB"}

func normalizeV2EXCookie(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ";")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		for _, key := range v2exCookieKeys {
			if strings.HasPrefix(part, key+"=") {
				kept = append(kept, part)
				break
			}
		}
	}
	if len(kept) == 0 {
		return raw
	}
	return strings.Join(kept, "; ")
}

func (c *v2exClient) get(ctx context.Context, path string) (*sign.Response, error) {
	resp, err := c.http.Get(ctx, v2exBaseURL+path, sign.RequestOptions{
		Cookie:  c.cookie,
		Referer: v2exBaseURL + "/",
		Headers: map[string]string{
			"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36",
		},
	})
	if err != nil {
		return nil, err
	}
	if resp.Status == 401 || resp.Status == 403 {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "V2EX 登录状态已经失效", 422)
	}
	if resp.Status == 429 || resp.Status >= 500 {
		return nil, sign.Fail("UPSTREAM_REQUEST_FAILED", "V2EX 服务暂时不可用", 502)
	}
	return resp, nil
}

func (c *v2exClient) mission(ctx context.Context) (string, bool, string, string, error) {
	resp, err := c.get(ctx, "/mission/daily")
	if err != nil {
		return "", false, "", "", err
	}
	text := string(resp.Body)
	if strings.Contains(text, "你要查看的页面需要先登录") {
		return "", false, "", text, sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "V2EX 登录状态已经失效", 422)
	}
	if strings.Contains(text, "每日登录奖励已领取") {
		streak := ""
		if match := v2exStreakPattern.FindStringSubmatch(text); len(match) > 1 {
			streak = match[1]
		}
		return "", true, streak, text, nil
	}
	if match := v2exOncePattern.FindStringSubmatch(text); len(match) > 1 {
		return match[1], false, "", text, nil
	}
	return "", false, "", text, sign.Fail("V2EX_RESPONSE_INVALID", "无法获取 V2EX 登录奖励参数，页面结构可能已变更", 502)
}

type v2exRewardResult struct {
	Coins          int
	AlreadyClaimed bool
}

func (c *v2exClient) redeem(ctx context.Context, once string) (v2exRewardResult, error) {
	resp, err := c.get(ctx, "/mission/daily/redeem?once="+once)
	if err != nil {
		return v2exRewardResult{}, err
	}
	text := string(resp.Body)
	if match := v2exRewardPattern.FindStringSubmatch(text); len(match) > 1 {
		return v2exRewardResult{Coins: sign.ValueInt(match[1])}, nil
	}
	if strings.Contains(text, "每日登录奖励已领取") {
		return v2exRewardResult{AlreadyClaimed: true}, nil
	}
	return v2exRewardResult{}, nil
}

func (c *v2exClient) balance(ctx context.Context) (string, string, error) {
	resp, err := c.get(ctx, "/balance")
	if err != nil {
		return "", "", err
	}
	text := string(resp.Body)
	today := time.Now().In(time.FixedZone("CST", 8*3600)).Format("20060102")
	reward := ""
	if match := v2exTodayRewardPattern.FindStringSubmatch(text); len(match) > 2 && match[1] == today {
		reward = match[2] + " 铜币"
		if idx := strings.Index(text, match[0]); idx >= 0 {
			rowStart := strings.LastIndex(text[:idx], "<tr>")
			rowEnd := idx + len(match[0]) + 200
			if rowEnd > len(text) {
				rowEnd = len(text)
			}
			if rowStart >= 0 {
				if stamp := v2exTimestampPattern.FindString(text[rowStart:rowEnd]); stamp != "" {
					reward += "（" + stamp + "）"
				}
			}
		}
	}
	return v2exBalance(text), reward, nil
}

func v2exBalance(text string) string {
	match := v2exBalancePattern.FindStringSubmatch(text)
	if len(match) < 2 {
		return ""
	}
	plain := v2exTagPattern.ReplaceAllString(match[1], " ")
	values := []string{}
	for _, item := range strings.Fields(plain) {
		if isDigits(item) {
			values = append(values, item)
		}
	}
	if len(values) == 2 {
		values = append([]string{"0"}, values...)
	}
	if len(values) < 3 {
		return ""
	}
	values = values[len(values)-3:]
	return fmt.Sprintf("%s 金币，%s 银币，%s 铜币", values[0], values[1], values[2])
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

var (
	v2exOncePattern        = regexp.MustCompile(`once=(\d+)`)
	v2exStreakPattern      = regexp.MustCompile(`已连续登录 (\d+) 天`)
	v2exRewardPattern      = regexp.MustCompile(`已成功领取每日登录奖励 (\d+) 铜币`)
	v2exTodayRewardPattern = regexp.MustCompile(`(\d{8}) 的每日登录奖励 (\d+) 铜币`)
	v2exTimestampPattern   = regexp.MustCompile(`\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} [+-]\d{2}:\d{2}`)
	v2exBalancePattern     = regexp.MustCompile(`(?s)<div class="balance_area bigger">(.*?)</div>`)
	v2exTagPattern         = regexp.MustCompile(`<[^>]*>`)
	v2exMemberPattern      = regexp.MustCompile(`href="/member/([^"/?#]+)"`)
)
