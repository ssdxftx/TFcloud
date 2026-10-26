package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"golang.org/x/text/encoding/simplifiedchinese"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"tfcloud/internal/sign"
)

const (
	cloud189WebURL     = "https://cloud.189.cn"
	cloud189AuthURL    = "https://open.e.189.cn"
	cloud189APIURL     = "https://api.cloud.189.cn"
	cloud189AppID      = "8025431004"
	cloud189ClientType = "10020"
	cloud189ReturnURL  = "https://m.cloud.189.cn/zhuanti/2020/loginErrorPc/index.html"
	cloud189UA         = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/87.0.4280.88 Safari/537.36"
)

var (
	cloud189CaptchaPattern = regexp.MustCompile(`name=['"]captchaToken['"][^>]*value=['"]([^'"]+)['"]`)
	cloud189LtPattern      = regexp.MustCompile(`\blt\s*=\s*["']([^"']+)["']`)
	cloud189ParamPattern   = regexp.MustCompile(`\bparamId\s*=\s*["']([^"']+)["']`)
	cloud189ReqPattern     = regexp.MustCompile(`\breqId\s*=\s*["']([^"']+)["']`)
)

type Cloud189 struct{ http *sign.HTTP }

func NewCloud189(h *sign.HTTP) *Cloud189 { return &Cloud189{http: h} }

func (p *Cloud189) Metadata() sign.Metadata {
	return sign.Metadata{
		Code: "cloud189", Name: "天翼云盘", Version: "0.1.0",
		Description:     "天翼云盘账号登录与每日签到",
		CredentialTypes: []string{"account_password"},
		Status:          "ready",
	}
}

func (p *Cloud189) CredentialRules() []sign.CredentialRule {
	return []sign.CredentialRule{{Key: "username", Required: true}, {Key: "password", Required: true}}
}

func (p *Cloud189) SupportedActions() []string { return []string{"daily_sign"} }

func (p *Cloud189) ValidateAccount(creds map[string]any) (*sign.Profile, error) {
	c := &cloud189Client{http: p.http, creds: cloneMap(creds)}
	session, err := c.authenticate(context.Background())
	if err != nil {
		return nil, err
	}
	return &sign.Profile{
		ExternalID:  c.accountIdentifier(),
		DisplayName: "天翼云盘",
		Extra: map[string]any{
			"login_name": c.maskedLoginName(session),
			"provider":   "天翼云盘",
		},
	}, nil
}

func (p *Cloud189) Execute(ctx context.Context, c *sign.Context) (*sign.Result, error) {
	client := &cloud189Client{http: p.http, creds: cloneMap(c.Credentials)}
	rec, err := client.dailySign(ctx)
	if err != nil {
		return nil, err
	}
	status := "succeeded"
	if rec.Status == "failed" {
		status = "failed"
	}
	result := &sign.Result{
		Status: status, Message: rec.Message, Records: []sign.Record{rec},
		Summary: map[string]any{"storage_mb": rec.Rewards["storage_mb"]},
	}
	if refreshed := client.refreshed(); refreshed != nil {
		result.Credentials = refreshed
	}
	return result, nil
}

type cloud189Client struct {
	http    *sign.HTTP
	creds   map[string]any
	session map[string]any
	changed bool
}

func (c *cloud189Client) refreshed() map[string]any {
	if !c.changed {
		return nil
	}
	return c.creds
}

func (c *cloud189Client) assertCredentials() error {
	if strings.TrimSpace(sign.ValueString(c.creds["username"])) == "" || sign.ValueString(c.creds["password"]) == "" {
		return sign.Fail("PLUGIN_CREDENTIAL_INVALID", "天翼云盘账号和密码不能为空", 422)
	}
	return nil
}

func (c *cloud189Client) accountIdentifier() string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(sign.ValueString(c.creds["username"])))))
	return hex.EncodeToString(sum[:])[:24]
}

func (c *cloud189Client) maskedLoginName(session map[string]any) string {
	loginName := firstText(session["loginName"], c.creds["username"])
	if m := regexp.MustCompile(`^(\d{3})\d+(\d{4})$`).FindStringSubmatch(loginName); len(m) == 3 {
		return m[1] + "****" + m[2]
	}
	if idx := strings.Index(loginName, "@"); idx > 0 {
		name := loginName[:idx]
		if len([]rune(name)) > 2 {
			name = string([]rune(name)[:2])
		}
		return name + "***" + loginName[idx:]
	}
	return "天翼云盘账号"
}

func (c *cloud189Client) authenticate(ctx context.Context) (map[string]any, error) {
	if c.session != nil {
		return c.session, nil
	}
	if err := c.assertCredentials(); err != nil {
		return nil, err
	}

	accessToken := strings.TrimSpace(sign.ValueString(c.creds["access_token"]))
	expiresAt := int64(sign.ValueInt(c.creds["access_token_expires_at"]))
	if accessToken != "" && expiresAt > time.Now().Unix()+60 {
		if session, err := c.getSession(ctx, map[string]string{"accessToken": accessToken}, false); err == nil && session != nil {
			c.session = session
			return session, nil
		}
	}

	if refreshToken := strings.TrimSpace(sign.ValueString(c.creds["refresh_token"])); refreshToken != "" {
		if refreshed, err := c.refreshToken(ctx, refreshToken); err == nil && refreshed != nil {
			if session, err := c.getSession(ctx, map[string]string{"accessToken": sign.ValueString(refreshed["access_token"])}, false); err == nil && session != nil {
				c.rememberTokens(sign.ValueString(refreshed["access_token"]), sign.ValueString(refreshed["refresh_token"]), sign.ValueInt(refreshed["expires_in"]))
				c.session = session
				return session, nil
			}
		}
	}

	redirectURL, err := c.passwordLogin(ctx,
		strings.TrimSpace(sign.ValueString(c.creds["username"])),
		sign.ValueString(c.creds["password"]))
	if err != nil {
		return nil, err
	}
	session, err := c.getSession(ctx, map[string]string{"redirectURL": redirectURL}, true)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, sign.Fail("CLOUD189_LOGIN_FAILED", "天翼云盘登录会话获取失败", 502)
	}
	c.rememberTokens(sign.ValueString(session["accessToken"]), sign.ValueString(session["refreshToken"]), 6*24*60*60)
	c.session = session
	return session, nil
}

func (c *cloud189Client) passwordLogin(ctx context.Context, username, password string) (string, error) {
	form, err := c.getLoginForm(ctx)
	if err != nil {
		return "", err
	}
	confResp, err := c.http.PostForm(ctx, cloud189AuthURL+"/api/logbox/config/encryptConf.do", sign.RequestOptions{
		Headers: c.authHeaders(),
	})
	if err != nil {
		return "", err
	}
	if err := assertCloud189Available(confResp); err != nil {
		return "", err
	}
	encrypt := sign.Map(sign.Map(confResp.JSON["data"])["data"])
	if encrypt == nil {
		encrypt = sign.Map(confResp.JSON["data"])
	}
	pubKey := sign.ValueString(encrypt["pubKey"])
	prefix := sign.ValueString(encrypt["pre"])
	if pubKey == "" {
		return "", sign.Fail("CLOUD189_ENCRYPT_CONFIG_INVALID", "天翼云盘登录公钥获取失败", 502)
	}
	encUser, err := rsaEncryptHex(pubKey, username)
	if err != nil {
		return "", sign.Fail("CLOUD189_LOGIN_ENCRYPT_FAILED", "天翼云盘登录信息加密失败", 500)
	}
	encPass, err := rsaEncryptHex(pubKey, password)
	if err != nil {
		return "", sign.Fail("CLOUD189_LOGIN_ENCRYPT_FAILED", "天翼云盘登录信息加密失败", 500)
	}
	form.Set("appKey", cloud189AppID)
	form.Set("accountType", "02")
	form.Set("validateCode", "")
	form.Set("dynamicCheck", "FALSE")
	form.Set("clientType", "1")
	form.Set("cb_SaveName", "3")
	form.Set("isOauth2", "false")
	form.Set("returnUrl", cloud189ReturnURL)
	form.Set("userName", prefix+encUser)
	form.Set("password", prefix+encPass)

	headers := c.authHeaders()
	headers["Referer"] = cloud189AuthURL
	headers["lt"] = sign.ValueString(c.creds["_lt"])
	headers["REQID"] = sign.ValueString(c.creds["_reqId"])

	resp, err := c.http.PostForm(ctx, cloud189AuthURL+"/api/logbox/oauth2/loginSubmit.do", sign.RequestOptions{
		Form:    form,
		Headers: headers,
	})
	if err != nil {
		return "", err
	}
	if err := assertCloud189Available(resp); err != nil {
		return "", err
	}
	if resp.JSON == nil {
		return "", sign.Fail("CLOUD189_LOGIN_FAILED", "天翼云盘登录返回格式错误", 502)
	}
	if sign.ValueInt(resp.JSON["result"]) != 0 || sign.ValueString(resp.JSON["toUrl"]) == "" {
		message := firstText(resp.JSON["msg"], "登录失败")
		if strings.Contains(message, "验证码") || strings.Contains(message, "设备锁") || strings.Contains(message, "安全验证") {
			return "", sign.Fail("CLOUD189_INTERACTION_REQUIRED", "天翼云盘要求人工验证，请先在网页端完成验证或关闭设备锁", 409)
		}
		return "", sign.Fail("PLUGIN_CREDENTIAL_INVALID", "天翼云盘账号或密码不正确："+message, 422)
	}
	return sign.ValueString(resp.JSON["toUrl"]), nil
}

func (c *cloud189Client) getLoginForm(ctx context.Context) (url.Values, error) {
	resp, err := c.http.Get(ctx, cloud189WebURL+"/api/portal/unifyLoginForPC.action", sign.RequestOptions{
		Query: url.Values{
			"appId":      {cloud189AppID},
			"clientType": {cloud189ClientType},
			"returnURL":  {cloud189ReturnURL},
			"timeStamp":  {strconv.FormatInt(nowMillis(), 10)},
		},
		Headers: c.webHeaders(),
	})
	if err != nil {
		return nil, err
	}
	if err := assertCloud189Available(resp); err != nil {
		return nil, err
	}
	html := decodeGBK(resp.Body)
	captcha := cloud189CaptchaPattern.FindStringSubmatch(html)
	lt := cloud189LtPattern.FindStringSubmatch(html)
	param := cloud189ParamPattern.FindStringSubmatch(html)
	req := cloud189ReqPattern.FindStringSubmatch(html)
	if len(captcha) < 2 || len(lt) < 2 || len(param) < 2 || len(req) < 2 {
		return nil, sign.Fail("CLOUD189_LOGIN_FORM_CHANGED", "天翼云盘登录页面结构已经变化", 502)
	}
	c.creds["_lt"] = lt[1]
	c.creds["_reqId"] = req[1]
	return url.Values{"captchaToken": {captcha[1]}, "paramId": {param[1]}}, nil
}

func (c *cloud189Client) getSession(ctx context.Context, credential map[string]string, required bool) (map[string]any, error) {
	query := url.Values{
		"appId":      {cloud189AppID},
		"clientType": {"TELEPC"},
		"version":    {"6.2"},
		"channelId":  {"web_cloud.189.cn"},
		"rand":       {strconv.FormatInt(nowMillis(), 10)},
	}
	for k, v := range credential {
		query.Set(k, v)
	}
	resp, err := c.http.PostForm(ctx, cloud189APIURL+"/getSessionForPC.action", sign.RequestOptions{
		Query:   query,
		Headers: c.apiHeaders(),
	})
	if err != nil {
		return nil, err
	}
	if err := assertCloud189Available(resp); err != nil {
		return nil, err
	}
	valid := resp.JSON != nil &&
		sign.ValueInt(resp.JSON["res_code"]) == 0 &&
		sign.ValueString(resp.JSON["sessionKey"]) != "" &&
		sign.ValueString(resp.JSON["accessToken"]) != ""
	if !valid {
		if required {
			return nil, sign.Fail("CLOUD189_LOGIN_FAILED", "天翼云盘登录会话无效", 502)
		}
		return nil, nil
	}
	return resp.JSON, nil
}

func (c *cloud189Client) refreshToken(ctx context.Context, refreshToken string) (map[string]any, error) {
	resp, err := c.http.PostForm(ctx, cloud189AuthURL+"/api/oauth2/refreshToken.do", sign.RequestOptions{
		Form: url.Values{
			"clientId":     {cloud189AppID},
			"refreshToken": {refreshToken},
			"grantType":    {"refresh_token"},
			"format":       {"json"},
		},
		Headers: c.authHeaders(),
	})
	if err != nil {
		return nil, err
	}
	if err := assertCloud189Available(resp); err != nil {
		return nil, err
	}
	if resp.JSON == nil || sign.ValueString(resp.JSON["accessToken"]) == "" || sign.ValueString(resp.JSON["refreshToken"]) == "" {
		return nil, nil
	}
	expiresIn := sign.ValueInt(resp.JSON["expiresIn"])
	if expiresIn < 60 {
		expiresIn = 6 * 24 * 60 * 60
	}
	return map[string]any{
		"access_token":  sign.ValueString(resp.JSON["accessToken"]),
		"refresh_token": sign.ValueString(resp.JSON["refreshToken"]),
		"expires_in":    expiresIn,
	}, nil
}

func (c *cloud189Client) rememberTokens(accessToken, refreshToken string, expiresIn int) {
	c.creds["access_token"] = accessToken
	c.creds["refresh_token"] = refreshToken
	if expiresIn < 60 {
		expiresIn = 60
	}
	c.creds["access_token_expires_at"] = int(time.Now().Unix()) + expiresIn
	c.creds["token_updated_at"] = timeNowISO()
	c.changed = true
}

func (c *cloud189Client) dailySign(ctx context.Context) (sign.Record, error) {
	session, err := c.authenticate(ctx)
	if err != nil {
		return sign.Record{}, err
	}
	resp, err := c.http.Get(ctx, cloud189WebURL+"/mkt/userSign.action", sign.RequestOptions{
		Query: url.Values{
			"rand":       {strconv.FormatInt(nowMillis(), 10)},
			"clientType": {"TELEANDROID"},
			"version":    {"9.0.6"},
			"model":      {"KB2000"},
			"sessionKey": {sign.ValueString(session["sessionKey"])},
		},
		Headers: c.webHeaders(),
	})
	if err != nil {
		return sign.Record{}, err
	}
	if err := assertCloud189Available(resp); err != nil {
		return sign.Record{}, err
	}
	if resp.JSON == nil {
		return sign.Record{}, sign.Fail("CLOUD189_RESPONSE_INVALID", "天翼云盘签到返回格式错误", 502)
	}
	if _, ok := resp.JSON["isSign"]; !ok {
		return sign.Record{}, sign.Fail("CLOUD189_RESPONSE_INVALID", "天翼云盘签到返回格式错误", 502)
	}
	already := sign.ValueBool(resp.JSON["isSign"])
	bonus := sign.ValueInt(resp.JSON["netdiskBonus"])
	if bonus < 0 {
		bonus = 0
	}
	status := "succeeded"
	message := "天翼云盘签到成功，获得 " + itoa(bonus) + " MB"
	if already {
		status = "already_done"
		message = "天翼云盘今日已签到，获得 " + itoa(bonus) + " MB"
	}
	return sign.Record{
		Key: "cloud189:daily_sign:" + dateToday(), Action: "daily_sign", Status: status,
		TargetType: "cloud_storage", TargetID: c.accountIdentifier(), TargetName: "天翼云盘",
		Code: "0", Message: message,
		Rewards: map[string]any{"storage_mb": bonus},
	}, nil
}

func (c *cloud189Client) authHeaders() map[string]string {
	return map[string]string{"User-Agent": cloud189UA, "Accept": "application/json;charset=UTF-8"}
}

func (c *cloud189Client) apiHeaders() map[string]string {
	return map[string]string{"User-Agent": cloud189UA, "Accept": "application/json;charset=UTF-8"}
}

func (c *cloud189Client) webHeaders() map[string]string {
	return map[string]string{
		"User-Agent": cloud189UA,
		"Referer":    cloud189WebURL + "/web/main/",
		"Accept":     "application/json;charset=UTF-8",
	}
}

func assertCloud189Available(resp *sign.Response) error {
	switch {
	case resp.Status == 429:
		return sign.Fail("CLOUD189_RATE_LIMITED", "天翼云盘请求过于频繁，请稍后再试", 429)
	case resp.Status == 0 || resp.Status >= 500:
		return sign.Fail("UPSTREAM_HTTP_ERROR", "天翼云盘服务暂时不可用", 502)
	}
	return nil
}

func decodeGBK(body []byte) string {
	if decoded, err := simplifiedchinese.GBK.NewDecoder().Bytes(body); err == nil {
		return string(decoded)
	}
	return string(body)
}
