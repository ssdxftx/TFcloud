package plugins

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/aes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"tfcloud/internal/sign"
)

const (
	sportLoginKey         = "xeNtBVqzDc6tuNTh"
	sportLoginIV          = "MAAAYAAAAAAAAABg"
	sportUA               = "MiFit6.14.0 (M2007J1SC; Android 12; Density/2.75)"
	sportClientID         = "428135909242707968"
	sportBandDataTemplate = "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"
)

var sportDeviceUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

type Sport struct{ http *sign.HTTP }

func NewSport(h *sign.HTTP) *Sport { return &Sport{http: h} }

func (p *Sport) Metadata() sign.Metadata {
	return sign.Metadata{
		Code: "sport", Name: "小米运动", Version: "0.2.0",
		Description:     "Zepp Life（原小米运动）步数更新",
		CredentialTypes: []string{"account_password"},
		Status:          "ready",
	}
}

func (p *Sport) CredentialRules() []sign.CredentialRule {
	return []sign.CredentialRule{{Key: "username", Required: true}, {Key: "password", Required: true}}
}

func (p *Sport) SupportedActions() []string { return []string{"update_steps"} }

func (p *Sport) ValidateAccount(creds map[string]any) (*sign.Profile, error) {
	profile, _, err := p.ValidateAccountWithRefresh(creds)
	return profile, err
}

func (p *Sport) ValidateAccountWithRefresh(creds map[string]any) (*sign.Profile, map[string]any, error) {
	c := &sportClient{http: p.http, creds: cloneMap(creds)}
	session, err := c.authenticate(context.Background())
	if err != nil {
		return nil, nil, err
	}
	profile := &sign.Profile{
		ExternalID:  sign.ValueString(session["user_id"]),
		DisplayName: firstText(session["nickname"], "小米运动账号"),
		Extra:       map[string]any{"provider": "Zepp Life", "login_verified_by": "app_token"},
	}
	return profile, c.refreshed(), nil
}

func (p *Sport) Execute(ctx context.Context, c *sign.Context) (*sign.Result, error) {
	client := &sportClient{http: p.http, creds: cloneMap(c.Credentials)}
	steps := sportSteps(c)
	rec, err := client.updateSteps(ctx, steps)
	if err != nil {
		return nil, err
	}
	status := "failed"
	if rec.Status == "succeeded" {
		status = "succeeded"
	}
	result := &sign.Result{
		Status: status, Message: rec.Message, Records: []sign.Record{rec},
		Summary: map[string]any{"steps": steps},
	}
	if refreshed := client.refreshed(); refreshed != nil {
		result.Credentials = refreshed
	}
	return result, nil
}

func sportSteps(c *sign.Context) int {
	mode := strings.TrimSpace(sign.ValueString(c.Setting("step_mode", "fixed")))
	if mode != "random" {
		return clampInt(c.IntSetting("steps", 18000), 1, 98800)
	}
	minimum := clampInt(c.IntSetting("min_steps", 18000), 1, 98800)
	maximum := clampInt(c.IntSetting("max_steps", 25000), minimum, 98800)
	if sign.ValueBool(c.Setting("progressive_by_time", false)) {
		now := time.Now()
		minutes := now.Hour()*60 + now.Minute()
		rate := float64(minutes) / (22 * 60)
		if rate > 1 {
			rate = 1
		}
		minimum = int(float64(minimum) * rate)
		if minimum < 1 {
			minimum = 1
		}
		maximum = int(float64(maximum) * rate)
		if maximum < minimum {
			maximum = minimum
		}
	}
	rangeSize := maximum - minimum + 1
	seed := sha256.Sum256([]byte(dateToday() + ":" + strconv.FormatInt(c.AccountID, 10) + ":sport"))
	offset := int(uint32(seed[0])<<24|uint32(seed[1])<<16|uint32(seed[2])<<8|uint32(seed[3])) % rangeSize
	return minimum + offset
}

// ---------- 客户端 ----------

type sportClient struct {
	http    *sign.HTTP
	creds   map[string]any
	session map[string]any
	changed bool
}

func (c *sportClient) refreshed() map[string]any {
	if !c.changed {
		return nil
	}
	return c.creds
}

func (c *sportClient) normalizedUsername() (string, error) {
	username := strings.TrimSpace(sign.ValueString(c.creds["username"]))
	password := sign.ValueString(c.creds["password"])
	if username == "" || password == "" {
		return "", sign.Fail("PLUGIN_CREDENTIAL_INVALID", "小米运动账号和密码不能为空", 422)
	}
	if !strings.Contains(username, "@") && !strings.HasPrefix(username, "+86") {
		username = "+86" + username
	}
	return username, nil
}

func (c *sportClient) authenticate(ctx context.Context) (map[string]any, error) {
	if c.session != nil {
		return c.session, nil
	}
	username, err := c.normalizedUsername()
	if err != nil {
		return nil, err
	}
	deviceID := strings.TrimSpace(sign.ValueString(c.creds["device_id"]))
	if !sportDeviceUUIDPattern.MatchString(deviceID) {
		deviceID = uuidV4()
		c.creds["device_id"] = deviceID
		c.changed = true
	}

	appToken := strings.TrimSpace(sign.ValueString(c.creds["app_token"]))
	userID := strings.TrimSpace(sign.ValueString(c.creds["user_id"]))
	if appToken != "" && userID != "" {
		if profile, err := c.checkAppToken(ctx, appToken, userID); err == nil && profile != nil {
			return c.setSession(appToken, userID, deviceID, firstText(profile["nickname"]))
		}
	}

	if loginToken := strings.TrimSpace(sign.ValueString(c.creds["login_token"])); loginToken != "" && userID != "" {
		if token, err := c.grantAppToken(ctx, loginToken); err == nil && token != "" {
			c.creds["app_token"] = token
			c.changed = true
			return c.setSession(token, userID, deviceID, "")
		}
	}

	accessToken := strings.TrimSpace(sign.ValueString(c.creds["access_token"]))
	if accessToken == "" {
		accessToken, err = c.loginAccessToken(ctx, username, sign.ValueString(c.creds["password"]))
		if err != nil {
			return nil, err
		}
	}
	tokens, err := c.grantLoginTokens(ctx, accessToken, deviceID, strings.HasPrefix(username, "+86"))
	if err != nil || tokens == nil {
		if accessToken, err = c.loginAccessToken(ctx, username, sign.ValueString(c.creds["password"])); err != nil {
			return nil, err
		}
		if tokens, err = c.grantLoginTokens(ctx, accessToken, deviceID, strings.HasPrefix(username, "+86")); err != nil || tokens == nil {
			return nil, sign.Fail("SPORT_LOGIN_FAILED", "Zepp Life 登录令牌获取失败", 502)
		}
	}
	c.creds["access_token"] = accessToken
	c.creds["login_token"] = tokens["login_token"]
	c.creds["app_token"] = tokens["app_token"]
	c.creds["user_id"] = tokens["user_id"]
	c.creds["token_updated_at"] = timeNowISO()
	c.changed = true
	return c.setSession(sign.ValueString(tokens["app_token"]), sign.ValueString(tokens["user_id"]), deviceID, "")
}

func (c *sportClient) setSession(appToken, userID, deviceID, nickname string) (map[string]any, error) {
	c.session = map[string]any{
		"app_token": appToken,
		"user_id":   userID,
		"device_id": deviceID,
		"nickname":  nickname,
	}
	return c.session, nil
}

func (c *sportClient) loginAccessToken(ctx context.Context, username, password string) (string, error) {
	form := url.Values{
		"emailOrPhone": {username},
		"password":     {password},
		"state":        {"REDIRECTION"},
		"client_id":    {"HuaMi"},
		"country_code": {"CN"},
		"token":        {"access"},
		"redirect_uri": {"https://s3-us-west-2.amazonaws.com/hm-registration/successsignin.html"},
	}
	encrypted, err := aesCBCEncryptRaw([]byte(form.Encode()), []byte(sportLoginKey), []byte(sportLoginIV))
	if err != nil {
		return "", sign.Fail("SPORT_LOGIN_ENCRYPT_FAILED", "Zepp Life 登录请求加密失败", 500)
	}
	resp, err := c.http.Do(ctx, "POST", "https://api-user.zepp.com/v2/registrations/tokens", sign.RequestOptions{
		RawBody: encrypted,
		Headers: map[string]string{
			"Content-Type":    "application/x-www-form-urlencoded; charset=UTF-8",
			"User-Agent":      sportUA,
			"app_name":        "com.xiaomi.hm.health",
			"appname":         "com.xiaomi.hm.health",
			"appplatform":     "android_phone",
			"x-hm-ekv":        "1",
			"hm-privacy-ceip": "false",
		},
		NoRedirect: true,
	})
	if err != nil {
		return "", err
	}
	if err := assertSportAvailable(resp); err != nil {
		return "", err
	}
	location := resp.Header.Get("Location")
	if resp.Status != 303 || location == "" {
		if resp.Status == 400 || resp.Status == 401 || resp.Status == 403 {
			return "", sign.Fail("PLUGIN_CREDENTIAL_INVALID", "Zepp Life 账号或密码不正确", 422)
		}
		return "", sign.Fail("SPORT_LOGIN_FAILED", "Zepp Life 登录服务返回异常状态", 502)
	}
	if m := regexp.MustCompile(`(?:[?&#])access=([^&]+)`).FindStringSubmatch(location); len(m) == 2 {
		if decoded, err := url.QueryUnescape(m[1]); err == nil && decoded != "" {
			return decoded, nil
		}
	}
	msg := "账号或密码不正确"
	if m := regexp.MustCompile(`(?:[?&#])error=([^&]+)`).FindStringSubmatch(location); len(m) == 2 {
		if decoded, err := url.QueryUnescape(m[1]); err == nil && decoded != "" {
			msg = decoded
		}
	}
	return "", sign.Fail("PLUGIN_CREDENTIAL_INVALID", "Zepp Life 登录失败："+msg, 422)
}

func (c *sportClient) grantLoginTokens(ctx context.Context, accessToken, deviceID string, isPhone bool) (map[string]any, error) {
	form := url.Values{
		"app_name":     {"com.xiaomi.hm.health"},
		"app_version":  {"6.14.0"},
		"code":         {accessToken},
		"country_code": {"CN"},
		"device_id":    {deviceID},
		"grant_type":   {"access_token"},
	}
	if isPhone {
		form.Set("device_model", "phone")
		form.Set("third_name", "huami_phone")
	} else {
		form.Set("allow_registration", "false")
		form.Set("device_model", "android_phone")
		form.Set("dn", "account.zepp.com,api-user.zepp.com,api-mifit.zepp.com,api-watch.zepp.com,app-analytics.zepp.com,api-analytics.huami.com,auth.zepp.com")
		form.Set("lang", "zh_CN")
		form.Set("os_version", "1.5.0")
		form.Set("source", "com.xiaomi.hm.health:6.14.0:50818")
		form.Set("third_name", "email")
	}
	resp, err := c.http.PostForm(ctx, "https://account.huami.com/v2/client/login", sign.RequestOptions{
		Form:    form,
		Headers: c.accountHeaders(),
	})
	if err != nil {
		return nil, err
	}
	if err := assertSportAvailable(resp); err != nil {
		return nil, err
	}
	if resp.Status != 200 || sign.ValueString(resp.JSON["result"]) != "ok" {
		return nil, nil
	}
	token := sign.Map(resp.JSON["token_info"])
	if sign.ValueString(token["login_token"]) == "" || sign.ValueString(token["app_token"]) == "" || sign.ValueString(token["user_id"]) == "" {
		return nil, nil
	}
	return map[string]any{
		"login_token": sign.ValueString(token["login_token"]),
		"app_token":   sign.ValueString(token["app_token"]),
		"user_id":     sign.ValueString(token["user_id"]),
	}, nil
}

func (c *sportClient) grantAppToken(ctx context.Context, loginToken string) (string, error) {
	resp, err := c.http.Get(ctx, "https://account-cn.huami.com/v1/client/app_tokens", sign.RequestOptions{
		Query: url.Values{
			"app_name":    {"com.xiaomi.hm.health"},
			"dn":          {"api-user.huami.com,api-mifit.huami.com,app-analytics.huami.com"},
			"login_token": {loginToken},
		},
		Headers: map[string]string{"User-Agent": "MiFit/5.3.0 (iPhone; iOS 14.7.1; Scale/3.00)"},
	})
	if err != nil {
		return "", err
	}
	if err := assertSportAvailable(resp); err != nil {
		return "", err
	}
	if resp.Status != 200 || sign.ValueString(resp.JSON["result"]) != "ok" {
		return "", nil
	}
	return strings.TrimSpace(sign.ValueString(sign.Map(resp.JSON["token_info"])["app_token"])), nil
}

func (c *sportClient) checkAppToken(ctx context.Context, appToken, userID string) (map[string]any, error) {
	resp, err := c.http.Get(ctx, "https://api-mifit-cn3.zepp.com/huami.health.getUserInfo.json", sign.RequestOptions{
		Query: url.Values{
			"r":           {uuidV4()},
			"userid":      {userID},
			"appid":       {sportClientID},
			"channel":     {"Normal"},
			"country":     {"CN"},
			"cv":          {"50818_6.14.0"},
			"device":      {"android_31"},
			"device_type": {"android_phone"},
			"lang":        {"zh_CN"},
			"timezone":    {"Asia/Shanghai"},
			"v":           {"2.0"},
		},
		Headers: c.apiHeaders(appToken),
	})
	if err != nil {
		return nil, err
	}
	if err := assertSportAvailable(resp); err != nil {
		return nil, err
	}
	if resp.Status != 200 || sign.ValueString(resp.JSON["message"]) != "success" {
		return nil, nil
	}
	return sign.Map(resp.JSON["data"]), nil
}

func (c *sportClient) updateSteps(ctx context.Context, steps int) (sign.Record, error) {
	if steps < 1 || steps > 98800 {
		return sign.Record{}, sign.Fail("VALIDATION_FAILED", "运动步数必须在 1 到 98800 之间", 422)
	}
	session, err := c.authenticate(ctx)
	if err != nil {
		return sign.Record{}, err
	}
	location := time.FixedZone("CST", 8*3600)
	date := time.Now().In(location).Format("2006-01-02")
	deviceID := sign.ValueString(session["device_id"])
	sum := sha256.Sum256([]byte(deviceID))
	bandDeviceID := strings.ToUpper(hex.EncodeToString(sum[:])[:16])

	dataJSON, err := buildBandData(date, steps, bandDeviceID)
	if err != nil {
		return sign.Record{}, err
	}
	form := url.Values{
		"userid":              {sign.ValueString(session["user_id"])},
		"last_sync_data_time": {"1597306380"},
		"device_type":         {"0"},
		"last_deviceid":       {bandDeviceID},
		"data_json":           {dataJSON},
	}
	resp, err := c.http.PostForm(ctx, "https://api-mifit-cn.huami.com/v1/data/band_data.json", sign.RequestOptions{
		Query: url.Values{"t": {strconv.FormatInt(nowMillis(), 10)}, "r": {uuidV4()}},
		Form:  form,
		Headers: map[string]string{
			"apptoken":     sign.ValueString(session["app_token"]),
			"Content-Type": "application/x-www-form-urlencoded",
			"User-Agent":   sportUA,
		},
	})
	if err != nil {
		return sign.Record{}, err
	}
	if err := assertSportAvailable(resp); err != nil {
		return sign.Record{}, err
	}
	message := sign.ValueString(resp.JSON["message"])
	success := resp.Status == 200 && message == "success"
	status := "failed"
	if success {
		status = "succeeded"
	}
	code := "0"
	if !success {
		code = firstText(resp.JSON["code"], itoa(resp.Status))
	}
	record := sign.Record{
		Key: "sport:steps:" + date, Action: "update_steps", Status: status,
		TargetType: "sport", TargetID: sign.ValueString(session["user_id"]), TargetName: "Zepp Life",
		Code:    code,
		Message: firstText(map[bool]string{true: "运动步数已更新为 " + itoa(steps), false: message}[success], "运动步数更新失败"),
		Metrics: map[string]any{"steps": steps, "date": date},
	}
	return record, nil
}

func (c *sportClient) accountHeaders() map[string]string {
	return map[string]string{
		"app_name":        "com.xiaomi.hm.health",
		"x-request-id":    uuidV4(),
		"accept-language": "zh-CN",
		"appname":         "com.xiaomi.hm.health",
		"cv":              "50818_6.14.0",
		"v":               "2.0",
		"appplatform":     "android_phone",
		"Content-Type":    "application/x-www-form-urlencoded; charset=UTF-8",
		"User-Agent":      sportUA,
	}
}

func (c *sportClient) apiHeaders(appToken string) map[string]string {
	return map[string]string{
		"User-Agent":   sportUA,
		"country":      "CN",
		"appplatform":  "android_phone",
		"x-request-id": uuidV4(),
		"timezone":     "Asia/Shanghai",
		"channel":      "Normal",
		"cv":           "50818_6.14.0",
		"appname":      "com.xiaomi.hm.health",
		"v":            "2.0",
		"apptoken":     appToken,
		"lang":         "zh_CN",
		"clientid":     sportClientID,
	}
}

func assertSportAvailable(resp *sign.Response) error {
	switch {
	case resp.Status == 429:
		return sign.Fail("SPORT_RATE_LIMITED", "Zepp Life 登录请求过于频繁，请稍后再试", 429)
	case resp.Status == 0 || resp.Status >= 500:
		return sign.Fail("UPSTREAM_HTTP_ERROR", "Zepp Life 服务暂时不可用", 502)
	}
	return nil
}

func buildBandData(date string, steps int, deviceID string) (string, error) {
	compressed, err := base64.StdEncoding.DecodeString(sportBandDataTemplate)
	if err != nil {
		return "", sign.Fail("SPORT_TEMPLATE_INVALID", "运动数据模板无法读取", 500)
	}
	zr, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", sign.Fail("SPORT_TEMPLATE_INVALID", "运动数据模板无法读取", 500)
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return "", sign.Fail("SPORT_TEMPLATE_INVALID", "运动数据模板无法读取", 500)
	}
	var payload []map[string]any
	if err := jsonUnmarshal(raw, &payload); err != nil || len(payload) == 0 {
		return "", sign.Fail("SPORT_TEMPLATE_INVALID", "运动数据模板无法读取", 500)
	}
	entry := payload[0]
	summaryStr, _ := entry["summary"].(string)
	var summary map[string]any
	if err := jsonUnmarshal([]byte(summaryStr), &summary); err != nil {
		return "", sign.Fail("SPORT_TEMPLATE_INVALID", "运动数据摘要模板无法读取", 500)
	}
	stp := sign.Map(summary["stp"])
	if stp == nil {
		return "", sign.Fail("SPORT_TEMPLATE_INVALID", "运动数据摘要模板无法读取", 500)
	}
	entry["date"] = date
	dataList := sign.Slice(entry["data"])
	if len(dataList) == 0 {
		return "", sign.Fail("SPORT_TEMPLATE_INVALID", "运动数据模板无法读取", 500)
	}
	if first := sign.Map(dataList[0]); first != nil {
		first["did"] = deviceID
	}
	stp["ttl"] = steps
	summaryJSON, err := marshalJSON(summary)
	if err != nil {
		return "", sign.Fail("SPORT_TEMPLATE_INVALID", "运动数据摘要模板无法读取", 500)
	}
	entry["summary"] = summaryJSON
	out, err := marshalJSON(payload)
	if err != nil {
		return "", sign.Fail("SPORT_TEMPLATE_INVALID", "运动数据模板无法读取", 500)
	}
	return out, nil
}

func aesCBCEncryptRaw(plain, key, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plain, block.BlockSize())
	out := make([]byte, len(padded))
	cipherNewCBC(block, iv).CryptBlocks(out, padded)
	return out, nil
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

var _ = fmt.Sprintf
