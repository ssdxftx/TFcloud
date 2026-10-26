package sign

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Error 是插件层错误，携带与 PHP 版一致的错误码，供上层转成 API 响应。
type Error struct {
	Code    string
	Message string
	Status  int
}

func (e *Error) Error() string { return e.Message }

func Fail(code, message string, status int) error {
	return &Error{Code: code, Message: message, Status: status}
}

type Metadata struct {
	Code            string
	Name            string
	Version         string
	Description     string
	CredentialTypes []string
	Status          string
}

type CredentialRule struct {
	Key      string
	Required bool
}

type Profile struct {
	ExternalID  string
	DisplayName string
	Extra       map[string]any
}

type Record struct {
	Key        string
	Action     string
	Status     string
	TargetType string
	TargetID   string
	TargetName string
	Code       string
	Message    string
	Rewards    map[string]any
	Metrics    map[string]any
}

type Result struct {
	Status       string
	Message      string
	Records      []Record
	Summary      map[string]any
	ProfilePatch map[string]any
	Credentials  map[string]any
}

type Context struct {
	UserID      int64
	AccountID   int64
	Action      string
	Credentials map[string]any
	Settings    map[string]any
	Options     map[string]any
	Report      func(Record)
}

func (c *Context) Emit(r Record) {
	if c.Report != nil {
		c.Report(r)
	}
}

func (c *Context) Credential(key string) string {
	if c.Credentials == nil {
		return ""
	}
	return strings.TrimSpace(ValueString(c.Credentials[key]))
}

func (c *Context) Setting(key string, fallback any) any {
	if c.Settings == nil {
		return fallback
	}
	if v, ok := c.Settings[key]; ok {
		return v
	}
	return fallback
}

func (c *Context) BoolSetting(key string, fallback bool) bool {
	v, ok := c.Setting(key, fallback).(bool)
	if !ok {
		return fallback
	}
	return v
}

func (c *Context) IntSetting(key string, fallback int) int {
	switch v := c.Setting(key, fallback).(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		var n int
		fmt.Sscanf(v, "%d", &n)
		return n
	}
	return fallback
}

type Plugin interface {
	Metadata() Metadata
	CredentialRules() []CredentialRule
	ValidateAccount(creds map[string]any) (*Profile, error)
	SupportedActions() []string
	Execute(ctx context.Context, c *Context) (*Result, error)
}

// RefreshAware 由需要刷新令牌的插件实现（天翼云盘/小米运动/哔咔）。
type RefreshAware interface {
	RefreshedCredentials() map[string]any
}

type Registry struct {
	byCode map[string]Plugin
	order  []string
}

func NewRegistry(plugins ...Plugin) *Registry {
	r := &Registry{byCode: make(map[string]Plugin, len(plugins))}
	for _, p := range plugins {
		r.byCode[p.Metadata().Code] = p
		r.order = append(r.order, p.Metadata().Code)
	}
	return r
}

func (r *Registry) Get(code string) (Plugin, error) {
	p, ok := r.byCode[code]
	if !ok {
		return nil, Fail("PLUGIN_NOT_FOUND", "签到插件不存在", 404)
	}
	return p, nil
}

func (r *Registry) All() []Plugin {
	out := make([]Plugin, 0, len(r.order))
	for _, code := range r.order {
		out = append(out, r.byCode[code])
	}
	return out
}

// ---------- HTTP 客户端 ----------

type HTTP struct {
	Client *http.Client
}

func NewHTTP() *HTTP {
	return &HTTP{Client: &http.Client{
		Timeout: 25 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}}
}

type RequestOptions struct {
	Query    url.Values
	Form     url.Values
	JSONBody any
	RawBody  []byte
	Headers  map[string]string
	Cookie   string
	Referer  string
	// NoRedirect 让 3xx 原样返回（用于抓 Location）
	NoRedirect bool
}

type Response struct {
	Status       int
	Body         []byte
	JSON         map[string]any
	Header       http.Header
	EffectiveURL string
}

func (h *HTTP) Do(ctx context.Context, method, target string, opts RequestOptions) (*Response, error) {
	if opts.Query != nil {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		target += sep + opts.Query.Encode()
	}
	var body io.Reader
	contentType := ""
	switch {
	case opts.JSONBody != nil:
		buf, err := json.Marshal(opts.JSONBody)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(buf)
		contentType = "application/json"
	case opts.Form != nil:
		body = strings.NewReader(opts.Form.Encode())
		contentType = "application/x-www-form-urlencoded"
	case len(opts.RawBody) > 0:
		body = bytes.NewReader(opts.RawBody)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, Fail("UPSTREAM_REQUEST_FAILED", "请求参数无效", 502)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if opts.Cookie != "" {
		req.Header.Set("Cookie", opts.Cookie)
	}
	if opts.Referer != "" {
		req.Header.Set("Referer", opts.Referer)
	}
	for k, v := range opts.Headers {
		req.Header.Set(k, v)
	}

	client := h.Client
	if opts.NoRedirect {
		clone := *h.Client
		clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &clone
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, Fail("UPSTREAM_REQUEST_FAILED", "访问上游服务失败："+sanitize(err.Error()), 502)
	}
	defer resp.Body.Close()

	var reader io.Reader = resp.Body
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Encoding")), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err == nil {
			defer gz.Close()
			reader = gz
		}
	}
	raw, err := io.ReadAll(io.LimitReader(reader, 8<<20))
	if err != nil {
		return nil, Fail("UPSTREAM_REQUEST_FAILED", "读取上游响失败", 502)
	}

	out := &Response{
		Status:       resp.StatusCode,
		Body:         raw,
		Header:       resp.Header,
		EffectiveURL: resp.Request.URL.String(),
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		var parsed map[string]any
		if err := json.Unmarshal(trimmed, &parsed); err == nil {
			out.JSON = parsed
		}
	}
	return out, nil
}

func (h *HTTP) Get(ctx context.Context, target string, opts RequestOptions) (*Response, error) {
	return h.Do(ctx, http.MethodGet, target, opts)
}

func (h *HTTP) PostForm(ctx context.Context, target string, opts RequestOptions) (*Response, error) {
	return h.Do(ctx, http.MethodPost, target, opts)
}

func (h *HTTP) PostJSON(ctx context.Context, target string, opts RequestOptions) (*Response, error) {
	return h.Do(ctx, http.MethodPost, target, opts)
}

// ---------- 通用工具 ----------

// ValueString 兼容 JSON 解码后的 float64 与字符串。
func ValueString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.6f", t), "0"), ".")
	case int:
		return fmt.Sprintf("%d", t)
	case int64:
		return fmt.Sprintf("%d", t)
	case json.Number:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

func ValueInt(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case float64:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	case string:
		var n int
		fmt.Sscanf(strings.TrimSpace(t), "%d", &n)
		return n
	case bool:
		if t {
			return 1
		}
		return 0
	default:
		return 0
	}
}

func ValueBool(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	return false
}

func Map(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func Slice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

// SignedParams 复刻 PHP 端 ksort 后拼接的签名文本。
func SignedParams(params map[string]string, suffix string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(params[k])
	}
	b.WriteString(suffix)
	return b.String()
}

func sanitize(v string) string {
	r := strings.NewReplacer("\n", " ", "\r", " ")
	return r.Replace(v)
}
