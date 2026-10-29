package plugins

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"tfcloud/internal/sign"
)

const (
	umaBaseURL         = "https://l11-activity-web-hk.komoejoy.com"
	umaActivityGroupID = "1755595320481626"
	umaActivityID      = "10000244"
	umaDrawActivityID  = "10000385"
	umaAppID           = "6829"
	umaAppKey          = "7200bfa761c94eae9ceb168bf4b129d0"
	umaSecret          = "+RIq/MaHJf9h23eOdjyXB6lkXL0LjcTGuPiNRyTtZX4="
)

type Uma struct{ http *sign.HTTP }

func NewUma(h *sign.HTTP) *Uma { return &Uma{http: h} }

func (p *Uma) Metadata() sign.Metadata {
	return sign.Metadata{
		Code: "uma", Name: "赛马娘", Version: "0.1.0",
		Description:     "赛马娘每日签到，查询本月签到、奖励与积分余额",
		CredentialTypes: []string{"cookie"},
		Status:          "ready",
	}
}

func (p *Uma) CredentialRules() []sign.CredentialRule {
	return []sign.CredentialRule{{Key: "cookie", Required: true}}
}

func (p *Uma) SupportedActions() []string { return []string{"daily_sign"} }

func (p *Uma) ValidateAccount(creds map[string]any) (*sign.Profile, error) {
	c := newUmaClient(p.http, creds)
	if c.cookie == "" {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据：cookie", 422)
	}
	_, code, message, err := c.status(context.Background())
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, sign.Fail("UMA_API_ERROR", firstText(message, "赛马娘账号验证失败"), 502)
	}
	uid := umaCookieValue(c.cookie, "DedeUserID")
	name := "赛马娘账号"
	if uid != "" {
		name = "赛马娘 · " + uid
	}
	return &sign.Profile{ExternalID: uid, DisplayName: name}, nil
}

func (p *Uma) Execute(ctx context.Context, c *sign.Context) (*sign.Result, error) {
	if c.Action != "daily_sign" {
		return nil, sign.Fail("PLUGIN_ACTION_NOT_SUPPORTED", "赛马娘插件不支持该动作", 400)
	}
	client := newUmaClient(p.http, c.Credentials)
	if client.cookie == "" {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据：cookie", 422)
	}

	status, statusCode, _, err := client.status(ctx)
	if err != nil {
		return nil, err
	}
	record := sign.Record{
		Key: "uma:daily_sign", Action: "daily_sign", Status: "succeeded",
		TargetType: "account", TargetName: "赛马娘", Message: "签到成功",
	}
	switch {
	case statusCode == 0 && status == 1:
		record.Status = "already_done"
		record.Message = "今日已签到"
	default:
		result, err := client.checkIn(ctx)
		if err != nil {
			return nil, err
		}
		if code := sign.ValueInt(result["code"]); code != 0 {
			record.Status = "failed"
			record.Code = "UMA_SIGN_FAILED"
			record.Message = firstText(result["message"], result["msg"], "赛马娘签到失败")
		}
	}

	monthCount, latest := client.record(ctx)
	points := client.points(ctx)
	daily, milestone := client.todayReward(ctx, monthCount)

	rewards := map[string]any{}
	if daily != "" {
		rewards["daily"] = daily
	}
	if milestone != "" {
		rewards["milestone"] = milestone
	}
	if len(rewards) > 0 {
		record.Rewards = rewards
	}
	var summaryParts []string
	if monthCount > 0 {
		summaryParts = append(summaryParts, fmt.Sprintf("本月已签到 %d 天", monthCount))
	}
	if latest != "" {
		summaryParts = append(summaryParts, "最近 "+latest)
	}
	if points != nil {
		summaryParts = append(summaryParts, fmt.Sprintf("积分 %d/%d", sign.ValueInt(points["available"]), sign.ValueInt(points["total"])))
	}
	if len(summaryParts) > 0 && record.Message != "" {
		record.Message += " · " + strings.Join(summaryParts, " · ")
	}
	c.Emit(record)

	result := &sign.Result{Status: record.Status, Message: record.Message, Records: []sign.Record{record}}
	if result.Status == "already_done" {
		result.Status = "succeeded"
	}
	result.Summary = map[string]any{"month_count": monthCount, "latest_sign_time": latest}
	if points != nil {
		result.Summary["points_available"] = sign.ValueInt(points["available"])
		result.Summary["points_total"] = sign.ValueInt(points["total"])
	}
	if len(rewards) > 0 {
		result.Summary["rewards"] = rewards
	}
	if record.Status == "failed" {
		result.Status = "failed"
	}
	return result, nil
}

// ---------- 客户端 ----------

type umaClient struct {
	http   *sign.HTTP
	cookie string
}

func newUmaClient(h *sign.HTTP, creds map[string]any) *umaClient {
	return &umaClient{http: h, cookie: normalizeUmaCookie(strings.TrimSpace(sign.ValueString(creds["cookie"])))}
}

var umaCookieKeys = []string{"site", "joy_jct", "DedeUserID", "SESSDATA"}

func normalizeUmaCookie(raw string) string {
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
		for _, key := range umaCookieKeys {
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

func umaCookieValue(cookie, key string) string {
	for _, part := range strings.Split(cookie, ";") {
		part = strings.TrimSpace(part)
		prefix := key + "="
		if strings.HasPrefix(part, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(part, prefix))
		}
	}
	return ""
}

func (c *umaClient) call(ctx context.Context, method, path string, extra map[string]string) (*sign.Response, error) {
	params := umaBuildParams(extra)
	opts := sign.RequestOptions{
		Cookie:  c.cookie,
		Referer: "https://uma.komoejoy.com/",
		Headers: map[string]string{
			"Origin":     "https://uma.komoejoy.com",
			"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36",
		},
	}
	values := url.Values{}
	for k, v := range params {
		values.Set(k, v)
	}
	if method == "POST" {
		opts.Form = values
	} else {
		opts.Query = values
	}
	resp, err := c.http.Do(ctx, method, umaBaseURL+path, opts)
	if err != nil {
		return nil, err
	}
	if resp.Status == 401 || resp.Status == 403 {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "赛马娘登录状态已经失效", 422)
	}
	if resp.Status == 429 || resp.Status >= 500 {
		return nil, sign.Fail("UPSTREAM_REQUEST_FAILED", "赛马娘服务暂时不可用", 502)
	}
	if resp.JSON == nil {
		return nil, sign.Fail("UMA_RESPONSE_INVALID", "赛马娘返回了无法识别的响应", 502)
	}
	return resp, nil
}

func umaBuildParams(extra map[string]string) map[string]string {
	params := map[string]string{
		"activity_group_id": umaActivityGroupID,
		"activity_id":       umaActivityID,
		"lang":              "zh",
		"app_id":            umaAppID,
		"nonce":             umaNonce(),
		"ts":                strconv.FormatInt(nowMillis(), 10),
		"appkey":            umaAppKey,
	}
	for k, v := range extra {
		params[k] = v
	}
	params["sign"] = umaSign(params)
	return params
}

func umaNonce() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(nowMillis(), 10) + "0000000000"
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return strconv.FormatInt(nowMillis(), 10) + string(buf)
}

func umaSign(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		if key == "sign" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+params[key])
	}
	sum := md5.Sum([]byte(strings.Join(pairs, "&") + "&secret=" + umaSecret))
	return hex.EncodeToString(sum[:])
}

func (c *umaClient) status(ctx context.Context) (int, int, string, error) {
	resp, err := c.call(ctx, "GET", "/activity/sign/v2/status", nil)
	if err != nil {
		return -1, -1, "", err
	}
	code := sign.ValueInt(resp.JSON["code"])
	message := firstText(resp.JSON["message"], resp.JSON["msg"])
	if code != 0 {
		return -1, code, message, nil
	}
	return sign.ValueInt(resp.JSON["data"]), 0, "", nil
}

func (c *umaClient) checkIn(ctx context.Context) (map[string]any, error) {
	resp, err := c.call(ctx, "POST", "/activity/sign/v2/in", nil)
	if err != nil {
		return nil, err
	}
	return resp.JSON, nil
}

func (c *umaClient) record(ctx context.Context) (int, string) {
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 1, 0).Add(-time.Millisecond)
	resp, err := c.call(ctx, "GET", "/activity/sign/v2/record", map[string]string{
		"start_time": strconv.FormatInt(start.UnixMilli(), 10),
		"end_time":   strconv.FormatInt(end.UnixMilli(), 10),
		"page_num":   "1",
		"page_size":  "31",
	})
	if err != nil || sign.ValueInt(resp.JSON["code"]) != 0 {
		return 0, ""
	}
	data := sign.Map(resp.JSON["data"])
	if data == nil {
		return 0, ""
	}
	total := sign.ValueInt(data["total"])
	records := sign.Slice(data["data"])
	latest := ""
	for _, raw := range records {
		item := sign.Map(raw)
		if item == nil {
			continue
		}
		if value := strings.TrimSpace(sign.ValueString(item["signDateTimeString"])); value != "" {
			latest = value
		}
	}
	return total, latest
}

func (c *umaClient) todayReward(ctx context.Context, monthCount int) (string, string) {
	resp, err := c.call(ctx, "GET", "/activity/sign/config", nil)
	if err != nil || sign.ValueInt(resp.JSON["code"]) != 0 {
		return "", ""
	}
	data := sign.Map(resp.JSON["data"])
	if data == nil {
		return "", ""
	}
	key := strconv.Itoa(time.Now().Day())
	if monthCount > 0 {
		key = strconv.Itoa(monthCount)
	}
	daily := umaPrize(sign.Map(sign.Map(data["daily_sign_prize_list"])[key]))
	milestone := umaPrize(sign.Map(sign.Map(data["total_sign_prize_list"])[key]))
	return daily, milestone
}

func umaPrize(item map[string]any) string {
	if item == nil {
		return ""
	}
	name := strings.TrimSpace(sign.ValueString(item["prize_name"]))
	number := sign.ValueInt(item["prize_num"])
	point := sign.ValueInt(item["add_point"])
	parts := []string{}
	if name != "" {
		parts = append(parts, fmt.Sprintf("%s x%d", name, number))
	}
	if point > 0 {
		parts = append(parts, fmt.Sprintf("+%d 积分", point))
	}
	return strings.Join(parts, " ")
}

func (c *umaClient) points(ctx context.Context) map[string]any {
	resp, err := c.call(ctx, "GET", "/activity/uma/sign/draw_assert", map[string]string{"activity_id": umaDrawActivityID})
	if err != nil || sign.ValueInt(resp.JSON["code"]) != 0 {
		return nil
	}
	return sign.Map(resp.JSON["data"])
}
