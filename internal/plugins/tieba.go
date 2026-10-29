package plugins

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"tfcloud/internal/sign"
)

const tiebaClientVersion = "8.1.0.4"
const tiebaSignKey = "tiebaclient!!!"

type Tieba struct{ http *sign.HTTP }

func NewTieba(h *sign.HTTP) *Tieba { return &Tieba{http: h} }

func (p *Tieba) Metadata() sign.Metadata {
	return sign.Metadata{
		Code: "tieba", Name: "百度贴吧", Version: "0.1.0",
		Description:     "自动获取全部关注贴吧并签到，支持执行明细、完整重跑和第三方请求失败自动补签",
		CredentialTypes: []string{"cookie"},
		Status:          "ready",
	}
}

func (p *Tieba) CredentialRules() []sign.CredentialRule {
	return []sign.CredentialRule{{Key: "bduss", Required: true}, {Key: "stoken"}}
}

func (p *Tieba) SupportedActions() []string { return []string{"daily_sign"} }

func (p *Tieba) ValidateAccount(creds map[string]any) (*sign.Profile, error) {
	c := newTiebaClient(p.http, creds)
	return c.profile(context.Background())
}

func (p *Tieba) Execute(ctx context.Context, c *sign.Context) (*sign.Result, error) {
	if c.Action != "daily_sign" {
		return nil, sign.Fail("PLUGIN_ACTION_NOT_SUPPORTED", "贴吧插件不支持该动作", 400)
	}
	client := newTiebaClient(p.http, c.Credentials)
	if client.bduss == "" {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据：bduss", 422)
	}
	profile := sign.Map(c.Options["profile"])
	var cache map[string]any
	if profile != nil {
		cache = sign.Map(profile["liked_forums"])
	}
	var targetIDs []string
	if raw := sign.Slice(c.Options["retry_forum_ids"]); raw != nil {
		for _, v := range raw {
			if s := strings.TrimSpace(sign.ValueString(v)); s != "" {
				targetIDs = append(targetIDs, s)
			}
		}
	}
	force := sign.ValueBool(c.Options["force_refresh_forums"])

	details, state, err := client.signAll(ctx, cache, targetIDs, force, c.Emit)
	if err != nil {
		return nil, err
	}

	records := make([]sign.Record, 0, len(details))
	var failed, succeeded, already int
	requestFailed := []string{}
	for _, d := range details {
		records = append(records, d.record)
		switch d.record.Status {
		case "failed":
			failed++
			if d.record.Code == "UPSTREAM_REQUEST_FAILED" {
				requestFailed = append(requestFailed, d.fid)
			}
		case "succeeded":
			succeeded++
		case "already_done":
			already++
		}
	}

	status := "succeeded"
	message := "贴吧签到完成"
	if failed > 0 {
		if succeeded+already > 0 {
			status = "partial"
		} else {
			status = "failed"
		}
		message = "部分贴吧签到失败"
	}
	cacheSize := 0
	if items, ok := state["items"].([]map[string]any); ok {
		cacheSize = len(items)
	}
	summary := map[string]any{
		"total":                    len(records),
		"succeeded":                succeeded,
		"already_done":             already,
		"failed":                   failed,
		"request_failed_forum_ids": requestFailed,
		"forum_sync_mode":          state["sync_mode"],
		"forum_cache_size":         cacheSize,
	}
	if v, ok := state["request_failed_date"]; ok {
		summary["request_failed_date"] = v
	}

	result := &sign.Result{Status: status, Message: message, Records: records, Summary: summary}
	if items, ok := state["items"].([]map[string]any); ok {
		result.ProfilePatch = map[string]any{
			"liked_forums": map[string]any{
				"synced_at":   state["synced_at"],
				"fingerprint": state["fingerprint"],
				"items":       items,
			},
		}
	}
	return result, nil
}

// ---------- 客户端 ----------

type tiebaClient struct {
	http     *sign.HTTP
	bduss    string
	stoken   string
	tbsCache string
	state    map[string]any
}

type tiebaDetail struct {
	fid    string
	record sign.Record
}

func newTiebaClient(h *sign.HTTP, creds map[string]any) *tiebaClient {
	return &tiebaClient{
		http:   h,
		bduss:  strings.TrimSpace(sign.ValueString(creds["bduss"])),
		stoken: strings.TrimSpace(sign.ValueString(creds["stoken"])),
	}
}

var tiebaPortraitPattern = regexp.MustCompile(`/item/([^/?#]+)`)

func (c *tiebaClient) mobileHeaders() map[string]string {
	return map[string]string{
		"Charset":         "UTF-8",
		"net":             "3",
		"User-Agent":      "bdtb for Android 8.4.0.1",
		"Accept-Encoding": "gzip",
	}
}

func (c *tiebaClient) webCookie() string {
	cookie := "BDUSS=" + c.bduss
	if c.stoken != "" {
		cookie += "; STOKEN=" + c.stoken
	}
	return cookie
}

func (c *tiebaClient) profile(ctx context.Context) (*sign.Profile, error) {
	if _, err := c.tbs(ctx); err != nil {
		return nil, err
	}
	user, err := c.fetchUser(ctx)
	if err != nil {
		return nil, err
	}
	externalID := firstText(user["id"], user["user_id"])
	name := firstText(user["name_show"], user["name"], user["user_name"])
	portrait := normalizePortrait(sign.ValueString(user["portrait"]))
	avatar := ""
	if portrait != "" {
		avatar = "https://himg.bdimg.com/sys/portrait/item/" + portrait
	}
	return &sign.Profile{
		ExternalID:  externalID,
		DisplayName: name,
		Extra: map[string]any{
			"login_verified_by": "tbs",
			"portrait":          portrait,
			"avatar":            avatar,
			"name":              firstText(user["name"]),
			"name_show":         firstText(user["name_show"]),
		},
	}, nil
}

func (c *tiebaClient) tbs(ctx context.Context) (string, error) {
	if c.tbsCache != "" {
		return c.tbsCache, nil
	}
	resp, err := c.http.Get(ctx, "https://tieba.baidu.com/dc/common/tbs", sign.RequestOptions{
		Cookie:  c.webCookie(),
		Referer: "https://tieba.baidu.com/",
		Headers: map[string]string{"X-Requested-With": "XMLHttpRequest"},
	})
	if err != nil {
		return "", err
	}
	if resp.Status == 0 || resp.Status == 429 || resp.Status >= 500 {
		return "", sign.Fail("UPSTREAM_REQUEST_FAILED", "贴吧服务暂时不可用", 502)
	}
	if resp.JSON == nil {
		return "", sign.Fail("TIEBA_RESPONSE_INVALID", "贴吧返回了无法识别的响应", 502)
	}
	if sign.ValueInt(resp.JSON["is_login"]) != 1 {
		return "", sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "贴吧登录状态已经失效", 422)
	}
	tbs := strings.TrimSpace(sign.ValueString(resp.JSON["tbs"]))
	if tbs == "" {
		return "", sign.Fail("TIEBA_TBS_FAILED", "获取贴吧签名参数失败", 502)
	}
	c.tbsCache = tbs
	return tbs, nil
}

func (c *tiebaClient) fetchUser(ctx context.Context) (map[string]any, error) {
	params := map[string]string{
		"bdusstoken":      c.bduss,
		"_client_type":    "4",
		"_client_version": tiebaClientVersion,
	}
	params["sign"] = strings.ToUpper(md5Hex(sign.SignedParams(params, tiebaSignKey)))
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	resp, err := c.http.PostForm(ctx, "https://c.tieba.baidu.com/c/s/login", sign.RequestOptions{
		Form:    form,
		Cookie:  "BDUSS=" + c.bduss,
		Headers: c.mobileHeaders(),
	})
	if err != nil {
		return nil, err
	}
	user := sign.Map(resp.JSON["user"])
	if user == nil {
		msg := firstText(resp.JSON["error_msg"], "获取贴吧账号资料失败")
		return nil, sign.Fail("TIEBA_PROFILE_FAILED", msg, 502)
	}
	return user, nil
}

func (c *tiebaClient) forumPage(ctx context.Context, page int) (map[string]any, error) {
	signText := "BDUSS=" + c.bduss +
		"_client_version=" + tiebaClientVersion +
		"page_no=" + itoa(page) +
		"page_size=100" + tiebaSignKey
	form := url.Values{}
	form.Set("BDUSS", c.bduss)
	form.Set("_client_version", tiebaClientVersion)
	form.Set("page_no", itoa(page))
	form.Set("page_size", "100")
	form.Set("sign", md5Hex(signText))
	resp, err := c.http.PostForm(ctx, "https://c.tieba.baidu.com/c/f/forum/like", sign.RequestOptions{
		Form:    form,
		Cookie:  "ca=open",
		Headers: c.mobileHeaders(),
	})
	if err != nil {
		return nil, err
	}
	if resp.JSON == nil {
		return nil, sign.Fail("TIEBA_FORUM_LIST_FAILED", "获取贴吧列表失败：响应格式错误", 502)
	}
	if code := sign.ValueString(resp.JSON["error_code"]); code != "0" && code != "" {
		return nil, sign.Fail("TIEBA_FORUM_LIST_FAILED", firstText(resp.JSON["error_msg"], "获取贴吧列表失败"), 502)
	}
	if page == 1 {
		if _, ok := resp.JSON["forum_list"]; !ok {
			return nil, sign.Fail("TIEBA_FORUM_LIST_FAILED", "获取贴吧列表失败：响应格式错误", 502)
		}
	}
	return resp.JSON, nil
}

type tiebaForum struct {
	FID  string
	Name string
}

func extractForums(data map[string]any) []tiebaForum {
	list := sign.Map(data["forum_list"])
	if list == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []tiebaForum
	for _, group := range []string{"gconforum", "non-gconforum"} {
		for _, item := range sign.Slice(list[group]) {
			f := sign.Map(item)
			if f == nil {
				continue
			}
			fid := sign.ValueString(f["id"])
			name := sign.ValueString(f["name"])
			if fid == "" || name == "" || seen[fid] {
				continue
			}
			seen[fid] = true
			out = append(out, tiebaForum{FID: fid, Name: name})
		}
	}
	return out
}

func (c *tiebaClient) signAll(ctx context.Context, cache map[string]any, targetIDs []string, force bool, emit func(sign.Record)) ([]tiebaDetail, map[string]any, error) {
	tbs, err := c.tbs(ctx)
	if err != nil {
		return nil, nil, err
	}

	cached := normalizeCachedItems(cache)
	var forums []tiebaForum
	fingerprint := ""
	syncMode := ""

	if !force && targetIDs != nil && len(cached) > 0 {
		forums = cached
		fingerprint = sign.ValueString(cache["fingerprint"])
		syncMode = "retry_cache"
	} else {
		first, err := c.forumPage(ctx, 1)
		if err != nil {
			return nil, nil, err
		}
		page1 := extractForums(first)
		hasMore := sign.ValueString(first["has_more"]) == "1"
		fingerprint = tiebaFingerprint(page1, hasMore)
		forums = page1
		page := 1
		seen := map[string]bool{}
		for _, f := range page1 {
			seen[f.FID] = true
		}
		for hasMore {
			page++
			next, err := c.forumPage(ctx, page)
			if err != nil {
				return nil, nil, err
			}
			added := 0
			for _, f := range extractForums(next) {
				if seen[f.FID] {
					continue
				}
				seen[f.FID] = true
				forums = append(forums, f)
				added++
			}
			if added == 0 {
				return nil, nil, sign.Fail("TIEBA_FORUM_LIST_STALLED", "贴吧关注列表分页未继续前进，已停止本次任务以避免重复签到", 502)
			}
			hasMore = sign.ValueString(next["has_more"]) == "1"
		}
		syncMode = "full_sync"
	}
	if force {
		targetIDs = nil
		syncMode = "forced_full_sync"
	}

	kept := map[string]tiebaForum{}
	for _, f := range forums {
		kept[f.FID] = f
	}

	signForums := forums
	if targetIDs != nil {
		targetSet := map[string]bool{}
		for _, id := range targetIDs {
			if id = strings.TrimSpace(id); id != "" {
				targetSet[id] = true
			}
		}
		signForums = signForums[:0:0]
		for _, f := range forums {
			if targetSet[f.FID] {
				signForums = append(signForums, f)
			}
		}
	}

	var details []tiebaDetail
	for _, forum := range signForums {
		d := c.signForum(ctx, forum, tbs)
		details = append(details, d)
		if emit != nil {
			emit(d.record)
		}
		if d.record.Code == "340012" || strings.Contains(d.record.Message, "未关注") || strings.Contains(d.record.Message, "还未关注") {
			delete(kept, forum.FID)
		}
	}

	items := make([]map[string]any, 0, len(kept))
	keys := make([]string, 0, len(kept))
	for k := range kept {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		items = append(items, map[string]any{"fid": kept[k].FID, "name": kept[k].Name})
	}
	c.state = map[string]any{
		"synced_at":           timeNowISO(),
		"fingerprint":         fingerprint,
		"items":               items,
		"sync_mode":           syncMode,
		"request_failed_date": dateToday(),
	}
	return details, c.state, nil
}

func (c *tiebaClient) signForum(ctx context.Context, forum tiebaForum, tbs string) tiebaDetail {
	signText := "BDUSS=" + c.bduss + "fid=" + forum.FID + "kw=" + forum.Name + "tbs=" + tbs + tiebaSignKey
	form := url.Values{}
	form.Set("BDUSS", c.bduss)
	form.Set("fid", forum.FID)
	form.Set("kw", forum.Name)
	form.Set("sign", md5Hex(signText))
	form.Set("tbs", tbs)

	resp, err := c.http.PostForm(ctx, "https://c.tieba.baidu.com/c/c/forum/sign", sign.RequestOptions{
		Form:    form,
		Cookie:  "ca=open",
		Headers: c.mobileHeaders(),
	})
	detail := tiebaDetail{fid: forum.FID}
	if err != nil {
		code := "PLUGIN_EXECUTION_FAILED"
		if e, ok := err.(*sign.Error); ok {
			code = e.Code
		}
		detail.record = sign.Record{
			Key: "forum:" + forum.FID, Action: "forum_sign", Status: "failed",
			TargetType: "forum", TargetID: forum.FID, TargetName: forum.Name,
			Code: code, Message: truncate(err.Error(), 500),
		}
		return detail
	}

	if resp.JSON == nil {
		detail.record = sign.Record{
			Key: "forum:" + forum.FID, Action: "forum_sign", Status: "failed",
			TargetType: "forum", TargetID: forum.FID, TargetName: forum.Name,
			Code: "UPSTREAM_REQUEST_FAILED", Message: "贴吧返回了无法识别的响应",
		}
		return detail
	}
	code := sign.ValueString(resp.JSON["error_code"])
	status := "failed"
	message := firstText(resp.JSON["error_msg"], "签到失败")
	switch code {
	case "0":
		status = "succeeded"
		if resp.JSON["error_msg"] == nil {
			message = "签到成功"
		}
	case "160002":
		status = "already_done"
	}
	userInfo := sign.Map(resp.JSON["user_info"])
	detail.record = sign.Record{
		Key: "forum:" + forum.FID, Action: "forum_sign", Status: status,
		TargetType: "forum", TargetID: forum.FID, TargetName: forum.Name,
		Code: code, Message: message,
		Rewards: map[string]any{"bonus_points": sign.ValueInt(userInfo["sign_bonus_point"])},
		Metrics: map[string]any{"sign_rank": sign.ValueInt(userInfo["user_sign_rank"])},
	}
	return detail
}

func normalizeCachedItems(cache map[string]any) []tiebaForum {
	if cache == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []tiebaForum
	for _, item := range sign.Slice(cache["items"]) {
		m := sign.Map(item)
		if m == nil {
			continue
		}
		fid := strings.TrimSpace(sign.ValueString(m["fid"]))
		name := strings.TrimSpace(sign.ValueString(m["name"]))
		if fid == "" || name == "" || seen[fid] {
			continue
		}
		seen[fid] = true
		out = append(out, tiebaForum{FID: fid, Name: name})
	}
	return out
}

func tiebaFingerprint(forums []tiebaForum, hasMore bool) string {
	fids := make([]string, 0, len(forums))
	for _, f := range forums {
		fids = append(fids, f.FID)
	}
	sort.Strings(fids)
	flag := "0"
	if hasMore {
		flag = "1"
	}
	sum := sha256.Sum256([]byte(strings.Join(fids, ",") + "|" + flag + "|" + itoa(len(forums))))
	return hex.EncodeToString(sum[:])
}

func normalizePortrait(portrait string) string {
	portrait = strings.TrimSpace(portrait)
	if portrait == "" {
		return ""
	}
	if strings.Contains(portrait, "://") {
		if m := tiebaPortraitPattern.FindStringSubmatch(portrait); len(m) == 2 {
			if decoded, err := url.QueryUnescape(m[1]); err == nil {
				return decoded
			}
			return m[1]
		}
		return ""
	}
	if idx := strings.Index(portrait, "?"); idx >= 0 {
		portrait = portrait[:idx]
	}
	return strings.TrimSpace(portrait)
}

func md5Hex(v string) string {
	sum := md5.Sum([]byte(v))
	return hex.EncodeToString(sum[:])
}
