package plugins

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"hash"
	"net/url"
	"regexp"
	"strings"

	"tfcloud/internal/sign"
)

var coinTargetBVIDs = []string{"BV1fF411275R", "BV1P841167pD", "BV18u411N78X"}

type Bilibili struct{ http *sign.HTTP }

func NewBilibili(h *sign.HTTP) *Bilibili { return &Bilibili{http: h} }

func (p *Bilibili) Metadata() sign.Metadata {
	return sign.Metadata{
		Code: "bilibili", Name: "哔哩哔哩", Version: "0.4.0",
		Description:     "哔哩哔哩主站、直播每日任务与粉丝牌挂机",
		CredentialTypes: []string{"cookie"},
		Status:          "ready",
	}
}

func (p *Bilibili) CredentialRules() []sign.CredentialRule {
	return []sign.CredentialRule{
		{Key: "sessdata", Required: true},
		{Key: "bili_jct", Required: true},
		{Key: "dede_user_id", Required: true},
		{Key: "dede_user_id_ckmd5"},
		{Key: "live_buvid"},
		{Key: "buvid3"},
		{Key: "buvid4"},
	}
}

func (p *Bilibili) SupportedActions() []string {
	return []string{
		"daily_tasks", "watch_video", "share_video", "give_coin",
		"live_sign", "live_daily_bag", "live_heartbeat", "live_fans_medal",
		"capsule_open", "comic_sign",
	}
}

func (p *Bilibili) ValidateAccount(creds map[string]any) (*sign.Profile, error) {
	c := newBilibiliClient(p.http, creds)
	return c.profile(context.Background())
}

func (p *Bilibili) Execute(ctx context.Context, c *sign.Context) (*sign.Result, error) {
	creds := c.Credentials
	if c.Credential("sessdata") == "" || c.Credential("bili_jct") == "" || c.Credential("dede_user_id") == "" {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据：sessdata、bili_jct、dede_user_id", 422)
	}
	if c.Action == "live_fans_medal" {
		return nil, sign.Fail("PLUGIN_ACTION_NOT_SUPPORTED", "粉丝牌挂机任务由独立的直播 Worker 执行", 400)
	}
	client := newBilibiliClient(p.http, creds)
	actions := []string{c.Action}
	if c.Action == "daily_tasks" {
		actions = dailyActions(c.Settings)
	}

	var records []sign.Record
	for _, action := range actions {
		switch action {
		case "give_coin":
			if !c.BoolSetting("allow_coin_spend", false) {
				rec := sign.Record{Key: "video:give_coin", Action: "give_coin", Status: "skipped",
					TargetType: "video", Code: "COIN_SPEND_NOT_ALLOWED", Message: "未显式允许消耗硬币，已跳过投币"}
				records = append(records, rec)
				c.Emit(rec)
				continue
			}
		case "capsule_open":
			if !c.BoolSetting("allow_capsule_spend", false) {
				rec := sign.Record{Key: "live:capsule_open", Action: "capsule_open", Status: "skipped",
					TargetType: "live", TargetName: "哔哩哔哩直播扭蛋",
					Code: "CAPSULE_SPEND_NOT_ALLOWED", Message: "未显式允许使用扭蛋币，已跳过扭蛋任务"}
				records = append(records, rec)
				c.Emit(rec)
				continue
			}
		}
		rec := normalizeBilibiliRecord(invokeBilibili(ctx, client, action, c))
		records = append(records, rec)
		c.Emit(rec)
	}

	failed, completed := 0, 0
	for _, r := range records {
		switch r.Status {
		case "failed":
			failed++
		case "succeeded", "already_done":
			completed++
		}
	}
	status := "succeeded"
	message := "哔哩哔哩任务完成"
	if failed > 0 {
		status = "partial"
		message = "部分哔哩哔哩任务失败"
		if completed == 0 {
			status = "failed"
		}
	}
	return &sign.Result{
		Status: status, Message: message, Records: records,
		Summary: map[string]any{"total": len(records), "completed": completed, "failed": failed},
	}, nil
}

func dailyActions(settings map[string]any) []string {
	boolOf := func(key string, def bool) bool {
		if settings == nil {
			return def
		}
		if v, ok := settings[key].(bool); ok {
			return v
		}
		return def
	}
	order := []struct {
		key     string
		enabled bool
	}{
		{"watch_video", boolOf("watch_enabled", true)},
		{"share_video", boolOf("share_enabled", true)},
		{"give_coin", boolOf("coin_enabled", false)},
		{"live_sign", boolOf("live_sign_enabled", true)},
		{"live_daily_bag", boolOf("live_daily_bag_enabled", false)},
		{"live_heartbeat", boolOf("live_heartbeat_enabled", false)},
		{"capsule_open", boolOf("capsule_enabled", false)},
		{"comic_sign", boolOf("comic_sign_enabled", false)},
	}
	var out []string
	for _, item := range order {
		if item.enabled {
			out = append(out, item.key)
		}
	}
	return out
}

func invokeBilibili(ctx context.Context, c *bilibiliClient, action string, sc *sign.Context) sign.Record {
	switch action {
	case "watch_video":
		return c.watchVideo(ctx)
	case "share_video":
		return c.shareVideo(ctx)
	case "give_coin":
		return c.giveCoin(ctx, sc.IntSetting("coin_count", 1))
	case "live_sign":
		return c.liveSign(ctx)
	case "live_daily_bag":
		return c.liveDailyBag(ctx)
	case "live_heartbeat":
		return c.liveHeartbeat(ctx, sign.ValueString(sc.Setting("live_room_id", "")))
	case "capsule_open":
		return c.openCapsule(ctx, sc.IntSetting("capsule_count", 1))
	case "comic_sign":
		return c.comicSign(ctx)
	default:
		return sign.Record{Key: "action:" + action, Action: action, Status: "failed",
			Code: "PLUGIN_ACTION_NOT_SUPPORTED", Message: "哔哩哔哩插件不支持该动作"}
	}
}

// ---------- 客户端 ----------

type bilibiliClient struct {
	http  *sign.HTTP
	creds map[string]any
	video map[string]any
	coin  map[string]any
}

func newBilibiliClient(h *sign.HTTP, creds map[string]any) *bilibiliClient {
	return &bilibiliClient{http: h, creds: creds}
}

func (c *bilibiliClient) cookie() string {
	parts := []string{
		"DedeUserID=" + sign.ValueString(c.creds["dede_user_id"]),
		"SESSDATA=" + sign.ValueString(c.creds["sessdata"]),
		"bili_jct=" + sign.ValueString(c.creds["bili_jct"]),
	}
	if v := strings.TrimSpace(sign.ValueString(c.creds["dede_user_id_ckmd5"])); v != "" {
		parts = append(parts, "DedeUserID__ckMd5="+v)
	}
	if v := strings.TrimSpace(sign.ValueString(c.creds["buvid3"])); v != "" {
		parts = append(parts, "buvid3="+v)
	}
	if v := strings.TrimSpace(sign.ValueString(c.creds["buvid4"])); v != "" {
		parts = append(parts, "buvid4="+v)
	}
	return strings.Join(parts, "; ")
}

func (c *bilibiliClient) get(ctx context.Context, target, referer string, query url.Values) (*sign.Response, error) {
	if referer == "" {
		referer = "https://www.bilibili.com/"
	}
	return c.http.Get(ctx, target, sign.RequestOptions{Query: query, Cookie: c.cookie(), Referer: referer})
}

func (c *bilibiliClient) post(ctx context.Context, target string, form url.Values, referer string) (*sign.Response, error) {
	if referer == "" {
		referer = "https://www.bilibili.com/"
	}
	return c.http.PostForm(ctx, target, sign.RequestOptions{Form: form, Cookie: c.cookie(), Referer: referer})
}

func (c *bilibiliClient) profile(ctx context.Context) (*sign.Profile, error) {
	resp, err := c.get(ctx, "https://api.bilibili.com/x/web-interface/nav", "", nil)
	if err != nil {
		return nil, err
	}
	if resp.Status == 0 || resp.Status == 429 || resp.Status >= 500 {
		return nil, sign.Fail("UPSTREAM_REQUEST_FAILED", "哔哩哔哩服务暂时不可用", 502)
	}
	if resp.JSON == nil {
		return nil, sign.Fail("BILIBILI_RESPONSE_INVALID", "哔哩哔哩返回了无法识别的响应", 502)
	}
	data := sign.Map(resp.JSON["data"])
	code := sign.ValueInt(resp.JSON["code"])
	if code == -101 {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "哔哩哔哩登录状态已经失效", 422)
	}
	if data == nil {
		return nil, sign.Fail("BILIBILI_RESPONSE_INVALID", "哔哩哔哩返回了无法识别的响应", 502)
	}
	if !sign.ValueBool(data["isLogin"]) {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "哔哩哔哩登录状态已经失效", 422)
	}
	levelInfo := sign.Map(data["level_info"])
	return &sign.Profile{
		ExternalID:  firstText(data["mid"], c.creds["dede_user_id"]),
		DisplayName: firstText(data["uname"], "哔哩哔哩账号"),
		Extra:       map[string]any{"level": sign.ValueInt(levelInfo["current_level"])},
	}, nil
}

func (c *bilibiliClient) currentVideo(ctx context.Context) (map[string]any, error) {
	if c.video != nil {
		return c.video, nil
	}
	resp, err := c.get(ctx, "https://api.bilibili.com/x/web-interface/popular", "", url.Values{"ps": {"20"}, "pn": {"1"}})
	if err != nil {
		return nil, err
	}
	if code := sign.ValueInt(resp.JSON["code"]); code != 0 {
		return nil, sign.Fail("BILIBILI_API_FAILED", firstText(resp.JSON["message"], "获取热门视频失败"), 502)
	}
	data := sign.Map(resp.JSON["data"])
	for _, item := range sign.Slice(data["list"]) {
		v := sign.Map(item)
		if v == nil {
			continue
		}
		if sign.ValueString(v["aid"]) != "" && sign.ValueString(v["cid"]) != "" && sign.ValueString(v["bvid"]) != "" {
			c.video = v
			return v, nil
		}
	}
	return nil, sign.Fail("BILIBILI_VIDEO_NOT_FOUND", "没有找到可用于任务的视频", 502)
}

func (c *bilibiliClient) coinVideo(ctx context.Context) (map[string]any, error) {
	if c.coin != nil {
		return c.coin, nil
	}
	offset := int(crc32Sum(dateToday()+"|"+sign.ValueString(c.creds["dede_user_id"]))) % len(coinTargetBVIDs)
	for i := 0; i < len(coinTargetBVIDs); i++ {
		bvid := coinTargetBVIDs[(offset+i)%len(coinTargetBVIDs)]
		resp, err := c.get(ctx, "https://api.bilibili.com/x/web-interface/view", "https://www.bilibili.com/video/"+bvid, url.Values{"bvid": {bvid}})
		if err != nil {
			continue
		}
		v := sign.Map(resp.JSON["data"])
		if sign.ValueInt(resp.JSON["code"]) == 0 && v != nil && sign.ValueString(v["aid"]) != "" && sign.ValueString(v["cid"]) != "" {
			v["bvid"] = firstText(v["bvid"], bvid)
			c.coin = v
			return v, nil
		}
	}
	return nil, sign.Fail("BILIBILI_COIN_TARGET_UNAVAILABLE", "预设投币视频暂时不可用，本次未消耗硬币", 502)
}

func (c *bilibiliClient) watchVideo(ctx context.Context) sign.Record {
	video, err := c.currentVideo(ctx)
	if err != nil {
		return failedRecord("watch_video", err)
	}
	duration := sign.ValueInt(video["duration"])
	if duration < 1 {
		duration = 1
	}
	form := url.Values{
		"aid":         {sign.ValueString(video["aid"])},
		"cid":         {sign.ValueString(video["cid"])},
		"mid":         {sign.ValueString(c.creds["dede_user_id"])},
		"csrf":        {sign.ValueString(c.creds["bili_jct"])},
		"played_time": {itoa(maxInt(duration-1, 0))},
		"realtime":    {itoa(duration)},
		"start_ts":    {itoa(int(nowMillis() / 1000))},
		"play_type":   {"0"},
	}
	resp, err := c.post(ctx, "https://api.bilibili.com/x/click-interface/web/heartbeat", form, videoReferer(video))
	if err != nil {
		return failedRecord("watch_video", err)
	}
	return videoRecord(resp, "watch_video", video, "视频观看任务提交成功")
}

func (c *bilibiliClient) shareVideo(ctx context.Context) sign.Record {
	video, err := c.currentVideo(ctx)
	if err != nil {
		return failedRecord("share_video", err)
	}
	form := url.Values{
		"aid":  {sign.ValueString(video["aid"])},
		"csrf": {sign.ValueString(c.creds["bili_jct"])},
	}
	resp, err := c.post(ctx, "https://api.bilibili.com/x/web-interface/share/add", form, videoReferer(video))
	if err != nil {
		return failedRecord("share_video", err)
	}
	return videoRecord(resp, "share_video", video, "视频分享任务提交成功")
}

func (c *bilibiliClient) giveCoin(ctx context.Context, count int) sign.Record {
	video, err := c.coinVideo(ctx)
	if err != nil {
		return failedRecord("give_coin", err)
	}
	if count < 1 {
		count = 1
	}
	if count > 2 {
		count = 2
	}
	form := url.Values{
		"aid":          {sign.ValueString(video["aid"])},
		"multiply":     {itoa(count)},
		"select_like":  {"0"},
		"cross_domain": {"true"},
		"csrf":         {sign.ValueString(c.creds["bili_jct"])},
	}
	resp, err := c.post(ctx, "https://api.bilibili.com/x/web-interface/coin/add", form, videoReferer(video))
	if err != nil {
		return failedRecord("give_coin", err)
	}
	rec := videoRecord(resp, "give_coin", video, "视频投币成功")
	if rec.Status == "succeeded" {
		rec.Rewards = map[string]any{"coins_spent": count}
	}
	return rec
}

func (c *bilibiliClient) liveSign(ctx context.Context) sign.Record {
	resp, err := c.get(ctx, "https://api.live.bilibili.com/xlive/web-ucenter/v1/sign/DoSign", "https://live.bilibili.com/", nil)
	if err != nil {
		return failedRecord("live_sign", err)
	}
	if !hasJSON(resp) {
		return invalidRecord("live_sign", "live:daily_sign")
	}
	code := sign.ValueInt(resp.JSON["code"])
	status := "failed"
	switch code {
	case 0:
		status = "succeeded"
	case 1011040:
		status = "already_done"
	}
	data := sign.Map(resp.JSON["data"])
	return sign.Record{
		Key: "live:daily_sign", Action: "live_sign", Status: status,
		TargetType: "live", TargetName: "哔哩哔哩直播", Code: itoa(code),
		Message: firstText(resp.JSON["message"], data["text"], map[string]string{
			"succeeded": "直播签到成功", "already_done": "直播签到今日已完成", "failed": "直播签到失败",
		}[status]),
		Rewards: map[string]any{"text": sign.ValueString(data["text"])},
	}
}

func (c *bilibiliClient) liveDailyBag(ctx context.Context) sign.Record {
	resp, err := c.get(ctx, "https://api.live.bilibili.com/gift/v2/live/receive_daily_bag", "https://live.bilibili.com/", nil)
	if err != nil {
		return failedRecord("live_daily_bag", err)
	}
	if !hasJSON(resp) {
		return invalidRecord("live_daily_bag", "live:live_daily_bag")
	}
	code := sign.ValueInt(resp.JSON["code"])
	message := firstText(resp.JSON["message"], resp.JSON["msg"])
	already := code != 0 && alreadyDoneMessage(message)
	status := "failed"
	switch {
	case code == 0:
		status = "succeeded"
		if message == "" {
			message = "直播每日礼包领取成功"
		}
	case already:
		status = "already_done"
		if message == "" {
			message = "直播每日礼包今日已领取"
		}
	}
	if message == "" {
		message = "直播每日礼包领取失败"
	}
	data := sign.Map(resp.JSON["data"])
	rewards := collectRewards(data)
	return sign.Record{
		Key: "live:live_daily_bag", Action: "live_daily_bag", Status: status,
		TargetType: "live", TargetName: "哔哩哔哩直播礼包", Code: itoa(code),
		Message: message, Rewards: map[string]any{"items": rewards},
	}
}

func (c *bilibiliClient) liveHeartbeat(ctx context.Context, roomID string) sign.Record {
	roomID = strings.TrimSpace(roomID)
	if !regexp.MustCompile(`^[1-9][0-9]{0,19}$`).MatchString(roomID) {
		return sign.Record{Key: "live:live_heartbeat", Action: "live_heartbeat", Status: "failed",
			Code: "VALIDATION_FAILED", Message: "直播心跳需要填写正确的直播间 ID"}
	}
	hb := base64.StdEncoding.EncodeToString([]byte("0|" + roomID + "|1|0"))
	resp, err := c.get(ctx, "https://live-trace.bilibili.com/xlive/rdata-interface/v1/heartbeat/webHeartBeat",
		"https://live.bilibili.com/"+roomID, url.Values{"pf": {"web"}, "hb": {hb}})
	if err != nil {
		return failedRecord("live_heartbeat", err)
	}
	if !hasJSON(resp) {
		return invalidRecord("live_heartbeat", "live:live_heartbeat")
	}
	code := sign.ValueInt(resp.JSON["code"])
	status := "failed"
	if code == 0 {
		status = "succeeded"
	}
	data := sign.Map(resp.JSON["data"])
	return sign.Record{
		Key: "live:live_heartbeat", Action: "live_heartbeat", Status: status,
		TargetType: "live", TargetID: roomID, TargetName: "直播间 " + roomID, Code: itoa(code),
		Message: firstText(resp.JSON["message"], pick(status == "succeeded", "直播心跳提交成功", "直播心跳提交失败")),
		Metrics: map[string]any{"room_id": roomID, "next_interval": sign.ValueInt(data["next_interval"])},
	}
}

func (c *bilibiliClient) openCapsule(ctx context.Context, requested int) sign.Record {
	detail, err := c.get(ctx, "https://api.live.bilibili.com/xlive/web-ucenter/v1/capsule/get_detail", "https://live.bilibili.com/", nil)
	if err != nil {
		return failedRecord("capsule_open", err)
	}
	if !hasJSON(detail) {
		return invalidRecord("capsule_open", "live:capsule_open")
	}
	if sign.ValueInt(detail.JSON["code"]) != 0 {
		return sign.Record{Key: "live:capsule_open", Action: "capsule_open", Status: "failed",
			Code: "BILIBILI_API_FAILED", Message: "获取直播扭蛋币信息失败"}
	}
	data := sign.Map(detail.JSON["data"])
	normal := sign.Map(data["normal"])
	available := sign.ValueInt(normal["coin"])
	if available == 0 {
		return sign.Record{Key: "live:capsule_open", Action: "capsule_open", Status: "already_done",
			TargetType: "live", TargetName: "哔哩哔哩直播扭蛋", Code: "0", Message: "当前没有可使用的普通扭蛋币"}
	}
	count := requested
	if count < 1 {
		count = 1
	}
	if count > available {
		count = available
	}
	if count > 100 {
		count = 100
	}
	form := url.Values{
		"csrf_token": {sign.ValueString(c.creds["bili_jct"])},
		"csrf":       {sign.ValueString(c.creds["bili_jct"])},
		"count":      {itoa(count)},
		"type":       {"normal"},
		"platform":   {"h5"},
	}
	resp, err := c.post(ctx, "https://api.live.bilibili.com/xlive/web-ucenter/v1/capsule/open_capsule", form, "https://live.bilibili.com/")
	if err != nil {
		return failedRecord("capsule_open", err)
	}
	if !hasJSON(resp) {
		return invalidRecord("capsule_open", "live:capsule_open")
	}
	code := sign.ValueInt(resp.JSON["code"])
	status := "failed"
	if code == 0 {
		status = "succeeded"
	}
	respData := sign.Map(resp.JSON["data"])
	rewards := []any{}
	for _, item := range sign.Slice(respData["awards"]) {
		m := sign.Map(item)
		if m == nil {
			continue
		}
		rewards = append(rewards, map[string]any{
			"name":  firstText(m["gift_name"], m["name"], "扭蛋奖励"),
			"count": sign.ValueInt(firstNonNil(m["num"], m["gift_num"])),
		})
	}
	return sign.Record{
		Key: "live:capsule_open", Action: "capsule_open", Status: status,
		TargetType: "live", TargetName: "哔哩哔哩直播扭蛋", Code: itoa(code),
		Message: firstText(resp.JSON["message"], pick(status == "succeeded", "普通扭蛋币使用成功", "普通扭蛋币使用失败")),
		Rewards: map[string]any{"items": rewards},
		Metrics: map[string]any{"capsules_spent": pickInt(status == "succeeded", count, 0), "capsules_available_before": available},
	}
}

func (c *bilibiliClient) comicSign(ctx context.Context) sign.Record {
	form := url.Values{"platform": {"android"}}
	info, err := c.post(ctx, "https://manga.bilibili.com/twirp/activity.v1.Activity/GetClockInInfo", form, "https://manga.bilibili.com/")
	if err != nil {
		return failedRecord("comic_sign", err)
	}
	if !hasJSON(info) {
		return invalidRecord("comic_sign", "comic:comic_sign")
	}
	if sign.ValueInt(info.JSON["code"]) != 0 {
		return sign.Record{Key: "comic:comic_sign", Action: "comic_sign", Status: "failed",
			Code: "BILIBILI_API_FAILED", Message: "获取漫画签到状态失败"}
	}
	if sign.ValueInt(sign.Map(info.JSON["data"])["status"]) == 1 {
		return sign.Record{Key: "comic:comic_sign", Action: "comic_sign", Status: "already_done",
			TargetType: "comic", TargetName: "哔哩哔哩漫画", Code: "0", Message: "漫画签到今日已完成"}
	}
	resp, err := c.post(ctx, "https://manga.bilibili.com/twirp/activity.v1.Activity/ClockIn", form, "https://manga.bilibili.com/")
	if err != nil {
		return failedRecord("comic_sign", err)
	}
	if !hasJSON(resp) {
		return invalidRecord("comic_sign", "comic:comic_sign")
	}
	code := sign.ValueInt(resp.JSON["code"])
	status := "failed"
	if code == 0 {
		status = "succeeded"
	}
	return sign.Record{
		Key: "comic:comic_sign", Action: "comic_sign", Status: status,
		TargetType: "comic", TargetName: "哔哩哔哩漫画", Code: itoa(code),
		Message: firstText(resp.JSON["msg"], pick(status == "succeeded", "漫画签到成功", "漫画签到失败")),
	}
}

// ---------- 直播心跳签名 ----------

// LiveHeartbeatSigner 复刻 BilibiliLiveHeartbeatSigner。
type LiveHeartbeatSigner struct{}

func (LiveHeartbeatSigner) Sign(text string, rules []int, key string) (string, error) {
	result := text
	for _, rule := range rules {
		var h func() hash.Hash
		switch rule {
		case 0:
			h = md5.New
		case 1:
			h = sha1.New
		case 2:
			h = sha256.New
		case 3:
			h = sha256.New224
		case 4:
			h = sha512.New
		case 5:
			h = sha512.New384
		default:
			return "", fmt.Errorf("不支持的直播心跳签名规则")
		}
		mac := hmac.New(h, []byte(key))
		mac.Write([]byte(result))
		result = fmt.Sprintf("%x", mac.Sum(nil))
	}
	return result, nil
}

// ---------- 工具 ----------

func videoReferer(video map[string]any) string {
	return "https://www.bilibili.com/video/" + sign.ValueString(video["bvid"])
}

func videoRecord(resp *sign.Response, action string, video map[string]any, successMessage string) sign.Record {
	if !hasJSON(resp) {
		return sign.Record{
			Key: action + ":" + sign.ValueString(video["aid"]), Action: action, Status: "failed",
			TargetType: "video", TargetID: sign.ValueString(video["aid"]), TargetName: sign.ValueString(video["bvid"]),
			Code: "BILIBILI_RESPONSE_INVALID", Message: "哔哩哔哩返回了无法识别的响应",
			Metrics: map[string]any{"bvid": sign.ValueString(video["bvid"])},
		}
	}
	code := sign.ValueInt(resp.JSON["code"])
	status := "failed"
	if code == 0 {
		status = "succeeded"
	}
	return sign.Record{
		Key: action + ":" + sign.ValueString(video["aid"]), Action: action, Status: status,
		TargetType: "video", TargetID: sign.ValueString(video["aid"]), TargetName: sign.ValueString(video["bvid"]),
		Code:    itoa(code),
		Message: firstText(resp.JSON["message"], pick(status == "succeeded", successMessage, "任务失败")),
		Metrics: map[string]any{"bvid": sign.ValueString(video["bvid"])},
	}
}

func normalizeBilibiliRecord(rec sign.Record) sign.Record {
	if rec.Code == "-101" {
		rec.Code = "PLUGIN_CREDENTIAL_EXPIRED"
		if rec.Message == "" || rec.Message == "-101" {
			rec.Message = "哔哩哔哩登录状态已经失效"
		}
	}
	return rec
}

func hasJSON(resp *sign.Response) bool {
	return resp != nil && resp.JSON != nil
}

func invalidRecord(action, key string) sign.Record {
	return sign.Record{Key: key, Action: action, Status: "failed",
		Code: "BILIBILI_RESPONSE_INVALID", Message: "哔哩哔哩返回了无法识别的响应"}
}

func failedRecord(action string, err error) sign.Record {
	code := "BILIBILI_ACTION_FAILED"
	if e, ok := err.(*sign.Error); ok {
		code = e.Code
	}
	return sign.Record{Key: "action:" + action, Action: action, Status: "failed", Code: code, Message: err.Error()}
}

func alreadyDoneMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(normalized, "已领取") || strings.Contains(normalized, "已经领取") || strings.Contains(normalized, "already")
}

func collectRewards(data map[string]any) []any {
	source := sign.Slice(data["bag_list"])
	if source == nil {
		source = sign.Slice(data["list"])
	}
	if source == nil {
		source = sign.Slice(data["awards"])
	}
	var rewards []any
	for _, item := range source {
		m := sign.Map(item)
		if m == nil {
			continue
		}
		name := firstText(m["gift_name"], m["name"])
		count := sign.ValueInt(firstNonNil(m["gift_num"], m["num"]))
		if name != "" || count > 0 {
			rewards = append(rewards, map[string]any{"name": firstText(name, "直播礼包"), "count": count})
		}
		if len(rewards) >= 20 {
			break
		}
	}
	return rewards
}

func pick(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func pickInt(cond bool, a, b int) int {
	if cond {
		return a
	}
	return b
}

func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func crc32Sum(v string) uint32 {
	const polynomial = 0xEDB88320
	crc := uint32(0xFFFFFFFF)
	for i := 0; i < len(v); i++ {
		crc ^= uint32(v[i])
		for j := 0; j < 8; j++ {
			if crc&1 != 0 {
				crc = (crc >> 1) ^ polynomial
			} else {
				crc >>= 1
			}
		}
	}
	return crc ^ 0xFFFFFFFF
}
