package plugins

import (
	"context"
	"net/url"
	"strings"

	"tfcloud/internal/sign"
)

// ============================ 网易云音乐 ============================

const (
	neteaseNonce     = "0CoJUm6Qyw8W8jud"
	neteaseIV        = "0102030405060708"
	neteaseSecretKey = "TA3YiYCfY2dDJQgg"
	neteaseEncSecKey = "84ca47bca10bad09a6b04c5c927ef077d9b9f1e37098aa3eac6ea70eb59df0aa28b691b7e75e4f1f9831754919ea784c8f74fbfadf2898b0be17849fd656060162857830e241aba44991601f137624094c114ea8d17bce815b0cd4e5b8e2fbaba978c6d1d14dc3d1faf852bdd28818031ccdaaa13a6018e1024e2aae98844210"
)

type Netease struct{ http *sign.HTTP }

func NewNetease(h *sign.HTTP) *Netease { return &Netease{http: h} }

func (p *Netease) Metadata() sign.Metadata {
	return sign.Metadata{
		Code: "netease", Name: "网易云音乐", Version: "0.2.0",
		Description:     "网易云音乐每日签到",
		CredentialTypes: []string{"cookie"},
		Status:          "ready",
	}
}

func (p *Netease) CredentialRules() []sign.CredentialRule {
	return []sign.CredentialRule{{Key: "music_u", Required: true}, {Key: "csrf", Required: true}}
}

func (p *Netease) SupportedActions() []string { return []string{"daily_sign"} }

func (p *Netease) ValidateAccount(creds map[string]any) (*sign.Profile, error) {
	c := &neteaseClient{http: p.http, musicU: sign.ValueString(creds["music_u"]), csrf: sign.ValueString(creds["csrf"])}
	return c.profile(context.Background())
}

func (p *Netease) Execute(ctx context.Context, c *sign.Context) (*sign.Result, error) {
	if c.Action != "daily_sign" {
		return nil, sign.Fail("PLUGIN_ACTION_NOT_SUPPORTED", "网易云插件不支持该动作", 400)
	}
	client := &neteaseClient{http: p.http, musicU: c.Credential("music_u"), csrf: c.Credential("csrf")}
	if client.musicU == "" || client.csrf == "" {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据：music_u、csrf", 422)
	}
	status, code, message, points, err := client.dailySign(ctx)
	if err != nil {
		return nil, err
	}
	record := sign.Record{
		Key: "account:daily_sign", Action: "daily_sign", Status: status,
		TargetType: "account", Code: code, Message: message,
		Rewards: map[string]any{"points": points},
	}
	resultStatus := "succeeded"
	if status == "failed" {
		resultStatus = "failed"
	}
	return &sign.Result{
		Status: resultStatus, Message: message, Records: []sign.Record{record},
		Summary: map[string]any{"points": points},
	}, nil
}

type neteaseClient struct {
	http   *sign.HTTP
	musicU string
	csrf   string
}

func (c *neteaseClient) cookie() string {
	return "os=pc; appver=2.9.7; MUSIC_U=" + c.musicU + "; __csrf=" + c.csrf
}

func (c *neteaseClient) profile(ctx context.Context) (*sign.Profile, error) {
	resp, err := c.http.Get(ctx, "https://music.163.com/api/nuser/account/get", sign.RequestOptions{
		Cookie:  c.cookie(),
		Referer: "https://music.163.com/",
	})
	if err != nil {
		return nil, err
	}
	account := sign.Map(resp.JSON["account"])
	if sign.ValueInt(resp.JSON["code"]) != 200 || account == nil || sign.ValueString(account["id"]) == "" {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "网易云音乐登录状态已经失效", 422)
	}
	profile := sign.Map(resp.JSON["profile"])
	return &sign.Profile{
		ExternalID:  sign.ValueString(account["id"]),
		DisplayName: firstText(profile["nickname"], "网易云音乐账号"),
		Extra:       map[string]any{"vip_type": sign.ValueInt(profile["vipType"])},
	}, nil
}

func (c *neteaseClient) dailySign(ctx context.Context) (status, code, message string, points int, err error) {
	params, err := neteaseEncrypt(map[string]any{"type": 0, "csrf_token": c.csrf})
	if err != nil {
		return "", "", "", 0, sign.Fail("NETEASE_ENCRYPT_FAILED", "网易云请求加密失败", 500)
	}
	resp, err := c.http.PostForm(ctx, "https://music.163.com/weapi/point/dailyTask", sign.RequestOptions{
		Form:    params,
		Cookie:  c.cookie(),
		Referer: "https://music.163.com/",
	})
	if err != nil {
		return "", "", "", 0, err
	}
	codeNum := -1
	if v, ok := resp.JSON["code"]; ok {
		codeNum = sign.ValueInt(v)
	}
	msg := sign.ValueString(resp.JSON["msg"])
	already := codeNum == -2 || strings.Contains(msg, "重复")
	status = "failed"
	switch {
	case codeNum == 200:
		status = "succeeded"
		if msg == "" {
			msg = "网易云音乐签到成功"
		}
	case already:
		status = "already_done"
		if msg == "" {
			msg = "网易云音乐今日已签到"
		}
	}
	if msg == "" {
		msg = "网易云音乐签到失败"
	}
	return status, itoa(codeNum), msg, sign.ValueInt(resp.JSON["point"]), nil
}

func neteaseEncrypt(payload map[string]any) (url.Values, error) {
	body, err := marshalJSON(payload)
	if err != nil {
		return nil, err
	}
	first, err := aesCBCEncryptBase64([]byte(body), []byte(neteaseNonce), []byte(neteaseIV))
	if err != nil {
		return nil, err
	}
	second, err := aesCBCEncryptBase64([]byte(first), []byte(neteaseSecretKey), []byte(neteaseIV))
	if err != nil {
		return nil, err
	}
	return url.Values{"params": {second}, "encSecKey": {neteaseEncSecKey}}, nil
}

// ============================ 爱奇艺 ============================

const (
	iqiyiVIPKey   = "UKobMjDMsDoScuWOfp6F"
	iqiyiScoreKey = "DO58SzN6ip9nbJ4QkM8H"
	iqiyiUA       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) TF-Sign/1.0"
)

type Iqiyi struct{ http *sign.HTTP }

func NewIqiyi(h *sign.HTTP) *Iqiyi { return &Iqiyi{http: h} }

func (p *Iqiyi) Metadata() sign.Metadata {
	return sign.Metadata{
		Code: "iqiyi", Name: "爱奇艺", Version: "0.2.0",
		Description:     "爱奇艺会员与积分签到",
		CredentialTypes: []string{"cookie"},
		Status:          "ready",
	}
}

func (p *Iqiyi) CredentialRules() []sign.CredentialRule {
	return []sign.CredentialRule{{Key: "p00001", Required: true}, {Key: "p00003", Required: true}}
}

func (p *Iqiyi) SupportedActions() []string {
	return []string{"vip_sign", "score_sign", "daily_tasks"}
}

func (p *Iqiyi) ValidateAccount(creds map[string]any) (*sign.Profile, error) {
	c := &iqiyiClient{http: p.http, p00001: sign.ValueString(creds["p00001"]), p00003: sign.ValueString(creds["p00003"])}
	return c.profile(context.Background())
}

func (p *Iqiyi) Execute(ctx context.Context, c *sign.Context) (*sign.Result, error) {
	client := &iqiyiClient{http: p.http, p00001: c.Credential("p00001"), p00003: c.Credential("p00003")}
	if client.p00001 == "" || client.p00003 == "" {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据：p00001、p00003", 422)
	}
	actions := []string{c.Action}
	if c.Action == "daily_tasks" {
		actions = []string{"vip_sign", "score_sign"}
	}
	var records []sign.Record
	for _, action := range actions {
		var rec sign.Record
		switch action {
		case "vip_sign":
			rec = client.vipSign(ctx, c.AccountID)
		case "score_sign":
			rec = client.scoreSign(ctx, c.AccountID)
		default:
			rec = sign.Record{Key: "account:" + action, Action: action, Status: "failed",
				Code: "PLUGIN_ACTION_NOT_SUPPORTED", Message: "爱奇艺插件不支持该动作"}
		}
		records = append(records, rec)
		if c.Report != nil {
			c.Report(rec)
		}
	}
	failed := 0
	for _, r := range records {
		if r.Status == "failed" {
			failed++
		}
	}
	status := "succeeded"
	message := "爱奇艺签到完成"
	if failed > 0 {
		status = "partial"
		message = "部分爱奇艺签到失败"
		if failed == len(records) {
			status = "failed"
		}
	}
	return &sign.Result{
		Status: status, Message: message, Records: records,
		Summary: map[string]any{"total": len(records), "failed": failed},
	}, nil
}

type iqiyiClient struct {
	http   *sign.HTTP
	p00001 string
	p00003 string
}

func (c *iqiyiClient) cookie() string {
	return "P00001=" + c.p00001 + "; P00003=" + c.p00003
}

func (c *iqiyiClient) signature(data map[string]string, key string, sorted bool) string {
	var text strings.Builder
	if sorted {
		text.WriteString(sign.SignedParams(data, ""))
	} else {
		// PHP 版本按数组插入顺序拼接，Go 的 map 无序，这里按固定字段顺序还原
		for _, name := range iqiyiFieldOrder(data) {
			text.WriteString(name + "=" + data[name] + "|")
		}
	}
	return md5Hex(text.String() + key)
}

var iqiyiVIPFieldOrder = []string{
	"agentType", "agentversion", "appKey", "authCookie", "qyid", "task_code", "timestamp", "typeCode", "userId",
}

var iqiyiScoreFieldOrder = []string{
	"agenttype", "agentversion", "appKey", "appver", "authCookie", "channelCode", "scoreType", "srcplatform",
	"typeCode", "userId", "user_agent", "verticalCode",
}

func iqiyiFieldOrder(data map[string]string) []string {
	order := iqiyiVIPFieldOrder
	if _, ok := data["agenttype"]; ok {
		order = iqiyiScoreFieldOrder
	}
	var out []string
	for _, k := range order {
		if _, ok := data[k]; ok {
			out = append(out, k)
		}
	}
	return out
}

func (c *iqiyiClient) profile(ctx context.Context) (*sign.Profile, error) {
	resp, err := c.http.Get(ctx, "https://serv.vip.iqiyi.com/vipgrowth/query.action", sign.RequestOptions{
		Cookie:  c.cookie(),
		Referer: "https://www.iqiyi.com/",
		Headers: map[string]string{"User-Agent": iqiyiUA},
	})
	if err != nil {
		return nil, err
	}
	if sign.ValueString(resp.JSON["code"]) != "A00000" {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "爱奇艺登录状态已经失效", 422)
	}
	data := sign.Map(resp.JSON["data"])
	return &sign.Profile{
		ExternalID:  c.p00003,
		DisplayName: firstText(data["userName"], "爱奇艺账号"),
		Extra:       map[string]any{"vip_level": sign.ValueInt(data["level"])},
	}, nil
}

func (c *iqiyiClient) vipSign(ctx context.Context, accountID int64) sign.Record {
	query := map[string]string{
		"agentType":    "1",
		"agentversion": "1.0",
		"appKey":       "basic_pcw",
		"authCookie":   c.p00001,
		"qyid":         strings.ToUpper(randHex(16)),
		"task_code":    "natural_month_sign",
		"timestamp":    itoa(int(nowMillis())),
		"typeCode":     "point",
		"userId":       c.p00003,
	}
	query["sign"] = c.signature(query, iqiyiVIPKey, false)
	formJSON := map[string]any{
		"natural_month_sign": map[string]any{
			"agentType":    "1",
			"agentversion": "1.0",
			"authCookie":   c.p00001,
			"qyid":         strings.ToUpper(randHex(16)),
			"taskCode":     "iQIYI_mofhr",
			"verticalCode": "iQIYI",
		},
	}
	resp, err := c.http.PostJSON(ctx, "https://community.iqiyi.com/openApi/task/execute", sign.RequestOptions{
		Query:    toValues(query),
		JSONBody: formJSON,
		Cookie:   c.cookie(),
		Referer:  "https://www.iqiyi.com/",
		Headers:  map[string]string{"User-Agent": iqiyiUA},
	})
	rec := sign.Record{Key: "account:vip_sign", Action: "vip_sign", TargetType: "account",
		TargetID: itoa(int(accountID)), TargetName: "爱奇艺账号"}
	if err != nil {
		rec.Status = "failed"
		rec.Code = "IQIYI_ACTION_FAILED"
		rec.Message = err.Error()
		return rec
	}
	outer := sign.ValueString(resp.JSON["code"])
	data := sign.Map(resp.JSON["data"])
	code := firstText(data["code"], outer)
	switch {
	case outer == "A00000" && code == "A0000":
		rec.Status = "succeeded"
	case outer == "A00000" && code == "A0014":
		rec.Status = "already_done"
	default:
		rec.Status = "failed"
	}
	if outer == "A00401" {
		rec.Code = "PLUGIN_CREDENTIAL_EXPIRED"
		rec.Message = "爱奇艺登录状态已经失效"
		return rec
	}
	rec.Code = code
	rec.Message = firstText(data["msg"], resp.JSON["message"], map[bool]string{true: "会员签到成功", false: "会员签到失败"}[rec.Status == "succeeded"])
	inner := sign.Map(data["data"])
	rewards := sign.Slice(inner["rewards"])
	points := 0
	if len(rewards) > 0 {
		points = sign.ValueInt(sign.Map(rewards[0])["rewardCount"])
	}
	rec.Rewards = map[string]any{"points": points}
	rec.Metrics = map[string]any{"sign_days": sign.ValueInt(inner["signDays"])}
	return rec
}

func (c *iqiyiClient) scoreSign(ctx context.Context, accountID int64) sign.Record {
	query := map[string]string{
		"agenttype":    "1",
		"agentversion": "0",
		"appKey":       "basic_pca",
		"appver":       "0",
		"authCookie":   c.p00001,
		"channelCode":  "sign_pcw",
		"scoreType":    "1",
		"srcplatform":  "1",
		"typeCode":     "point",
		"userId":       c.p00003,
		"user_agent":   iqiyiUA,
		"verticalCode": "iQIYI",
	}
	query["sign"] = c.signature(query, iqiyiScoreKey, false)
	resp, err := c.http.Get(ctx, "https://community.iqiyi.com/openApi/score/add", sign.RequestOptions{
		Query:   toValues(query),
		Cookie:  c.cookie(),
		Referer: "https://www.iqiyi.com/",
		Headers: map[string]string{"User-Agent": iqiyiUA},
	})
	rec := sign.Record{Key: "account:score_sign", Action: "score_sign", TargetType: "account",
		TargetID: itoa(int(accountID)), TargetName: "爱奇艺账号"}
	if err != nil {
		rec.Status = "failed"
		rec.Code = "IQIYI_ACTION_FAILED"
		rec.Message = err.Error()
		return rec
	}
	outer := sign.ValueString(resp.JSON["code"])
	list := sign.Slice(resp.JSON["data"])
	first := map[string]any{}
	if len(list) > 0 {
		first = sign.Map(list[0])
	}
	code := firstText(first["code"], outer)
	switch {
	case outer == "A00000" && code == "A0000":
		rec.Status = "succeeded"
	case outer == "A00000" && code == "A0002":
		rec.Status = "already_done"
	default:
		rec.Status = "failed"
	}
	if outer == "A00401" {
		rec.Status = "failed"
		rec.Code = "PLUGIN_CREDENTIAL_EXPIRED"
		rec.Message = "爱奇艺登录状态已经失效"
		return rec
	}
	rec.Code = code
	rec.Message = firstText(first["message"], resp.JSON["message"], map[bool]string{true: "积分签到成功", false: "积分签到失败"}[rec.Status == "succeeded"])
	rec.Rewards = map[string]any{"points": sign.ValueInt(first["score"])}
	rec.Metrics = map[string]any{"continuous_days": sign.ValueInt(first["continuousValue"])}
	return rec
}

func toValues(m map[string]string) url.Values {
	v := url.Values{}
	for k, val := range m {
		v.Set(k, val)
	}
	return v
}
