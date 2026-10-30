package middleware

import (
	"context"
	"crypto/hmac"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"tfcloud/internal/database"
	"tfcloud/internal/httpapi/respond"
	"tfcloud/internal/secure"
)

type Principal struct {
	UserID   int64
	Role     string
	Username string
	Status   string
}

func (p *Principal) IsAdmin() bool { return p != nil && p.Role == "admin" }

type principalKey struct{}
type tokenKey struct{}

func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func FromContext(ctx context.Context) *Principal {
	if v, ok := ctx.Value(principalKey{}).(*Principal); ok {
		return v
	}
	return nil
}

func TokenFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(tokenKey{}).(string); ok {
		return v
	}
	return ""
}

func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenKey{}, token)
}

// Authenticator 由 service.AuthService 实现。
type Authenticator interface {
	Authenticate(rawToken string, ip string) (*Principal, error)
}

const (
	SessionCookie = "tf_sign_token"
	CSRFCookie    = "tf_csrf"
)

// ExtractToken 优先 Bearer，其次 Cookie，与原实现一致。
func ExtractToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if v := strings.TrimSpace(h[7:]); v != "" {
			return v
		}
	}
	if c, err := r.Cookie(SessionCookie); err == nil {
		return c.Value
	}
	if h := r.Header.Get("X-Auth-Token"); h != "" {
		return h
	}
	return ""
}

type Middleware struct {
	Auth        Authenticator
	Installed   func() bool
	Maintenance func() bool
	CORS        func() []string
	Logger      *slog.Logger
}

func (m *Middleware) corsOrigins() []string {
	if m.CORS == nil {
		return nil
	}
	return m.CORS()
}

func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-Id")
			if len(id) != 32 {
				id = secure.RandomHex(16)
			} else if _, err := hex.DecodeString(id); err != nil {
				id = secure.RandomHex(16)
			}
			w.Header().Set("X-Request-Id", id)
			next.ServeHTTP(w, r.WithContext(respond.WithRequestID(r.Context(), id)))
		})
	}
}

func (m *Middleware) Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				m.Logger.Error("panic recovered",
					slog.Any("panic", rec),
					slog.String("path", r.URL.Path),
					slog.String("stack", string(debug.Stack())))
				respond.Fail(w, r, respond.Internal("服务器内部错误"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (m *Middleware) CORSHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Add("Vary", "Origin")
			if allowed, credentials := m.originAllowed(origin); allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				if credentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id, X-Auth-Token, X-CSRF-Token, Accept")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id")
				w.Header().Set("Access-Control-Max-Age", "86400")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originAllowed 只对显式配置的来源开放跨域。同源请求不依赖 CORS，
// 因此未配置 CORS_ALLOWED_ORIGINS 时必须拒绝跨域，避免任意站点带 Cookie 读取接口。
// 通配符来源仅用于无凭据的公开接口，禁止与 Allow-Credentials 同时出现。
func (m *Middleware) originAllowed(origin string) (allowed, credentials bool) {
	origins := m.corsOrigins()
	if len(origins) == 0 {
		return false, false
	}
	trimmed := strings.TrimRight(origin, "/")
	for _, item := range origins {
		if item == "*" {
			return true, false
		}
		if strings.EqualFold(item, trimmed) {
			return true, true
		}
	}
	return false, false
}

// SecurityHeaders 设置与具体业务无关的基础安全响应头。
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'; object-src 'none'")
		h.Set("Strict-Transport-Security", "max-age=31536000")
		next.ServeHTTP(w, r)
	})
}

// CSRF 使用双提交 Cookie：前端从非 HttpOnly Cookie 读取令牌并放入 X-CSRF-Token。
func CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		token := ""
		if c, err := r.Cookie(CSRFCookie); err == nil {
			token = c.Value
		}
		if len(token) != 32 {
			token = secure.RandomHex(16)
			secureCookie := r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
			http.SetCookie(w, &http.Cookie{
				Name:     CSRFCookie,
				Value:    token,
				Path:     "/",
				Secure:   secureCookie,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   30 * 24 * 60 * 60,
			})
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || csrfExempt(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// Bearer/X-Auth-Token 由调用方显式携带，不存在浏览器自动附带，不需要 CSRF。
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || r.Header.Get("X-Auth-Token") != "" {
			next.ServeHTTP(w, r)
			return
		}
		provided := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
		if provided == "" || !hmac.Equal([]byte(provided), []byte(token)) {
			respond.Fail(w, r, respond.NewError("CSRF_TOKEN_INVALID", "请求校验失败，请刷新页面后重试", 403))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func csrfExempt(path string) bool {
	switch {
	case path == "/api/install" || strings.HasPrefix(path, "/api/install/"):
		return true
	case path == "/api/auth/login", path == "/api/auth/login/email", path == "/api/auth/login/email-code",
		path == "/api/auth/register", path == "/api/auth/register/email-code", path == "/api/auth/logout",
		path == "/api/auth/password/forgot", path == "/api/auth/password/reset",
		path == "/api/auth/human-challenge", path == "/api/auth/human-challenge/verify":
		return true
	case strings.HasPrefix(path, "/api/auth/qq/"):
		return true
	default:
		return false
	}
}

// InstallGuard 在未安装时把除安装接口外的请求挡在门外。
func (m *Middleware) InstallGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.Installed() {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		// 页面请求一律交给前端：SPA 会自行检测安装状态并跳到 /install。
		// 否则用户直接打开域名根路径只能看到一段 JSON，体验很差。
		if !strings.HasPrefix(p, "/api/") ||
			p == "/api/install" || strings.HasPrefix(p, "/api/install/") ||
			p == "/api/site" || // 登录/安装页都要读站点名与 Logo
			strings.HasPrefix(p, "/health") {
			next.ServeHTTP(w, r)
			return
		}
		respond.JSON(w, http.StatusServiceUnavailable, respond.Envelope{
			Code:      "INSTALL_REQUIRED",
			Message:   "站点尚未完成安装",
			RequestID: respond.RequestID(r.Context()),
		})
	})
}

// MaintenanceGuard 在维护模式下阻止普通用户访问业务接口，管理员仍然可以进入后台处理事务。
func (m *Middleware) MaintenanceGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.Maintenance == nil || !m.Maintenance() {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		allowed := p == "/api/site" || strings.HasPrefix(p, "/api/install") ||
			strings.HasPrefix(p, "/api/auth/") || !strings.HasPrefix(p, "/api/")
		if allowed {
			next.ServeHTTP(w, r)
			return
		}
		if token := ExtractToken(r); token != "" && m.Auth != nil {
			if principal, err := m.Auth.Authenticate(token, ClientIP(r)); err == nil && principal.IsAdmin() {
				next.ServeHTTP(w, r)
				return
			}
		}
		respond.JSON(w, http.StatusServiceUnavailable, respond.Envelope{
			Code: "MAINTENANCE", Message: "站点正在维护，请稍后再试",
			RequestID: respond.RequestID(r.Context()),
		})
	})
}

func (m *Middleware) AuthRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ExtractToken(r)
		if token == "" {
			respond.Fail(w, r, respond.Unauthorized("请先登录"))
			return
		}
		principal, err := m.Auth.Authenticate(token, ClientIP(r))
		if err != nil {
			respond.Fail(w, r, err)
			return
		}
		ctx := WithPrincipal(r.Context(), principal)
		ctx = WithToken(ctx, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *Middleware) AdminRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := FromContext(r.Context())
		if !p.IsAdmin() {
			respond.Fail(w, r, respond.Forbidden("需要管理员权限"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

var (
	trustedProxyMu   sync.RWMutex
	trustedProxyNets []*net.IPNet
)

func init() {
	SetTrustedProxies([]string{"127.0.0.0/8", "::1/128"})
}

// SetTrustedProxies 配置允许提供转发头的代理网段。默认只信任本机回环地址；
// 使用宿主机 Nginx/Caddy 反向代理容器时，应显式配置代理所在网段。
func SetTrustedProxies(cidrs []string) {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		if _, network, err := net.ParseCIDR(strings.TrimSpace(cidr)); err == nil {
			nets = append(nets, network)
		}
	}
	trustedProxyMu.Lock()
	trustedProxyNets = nets
	trustedProxyMu.Unlock()
}

func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !isTrustedProxy(host) {
		return host
	}
	if v := forwardedIP(r.Header.Get("CF-Connecting-IP")); v != "" {
		return v
	}
	if v := forwardedIP(r.Header.Get("X-Real-IP")); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		first := v
		if idx := strings.Index(v, ","); idx > 0 {
			first = v[:idx]
		}
		if value := forwardedIP(first); value != "" {
			return value
		}
	}
	return host
}

func forwardedIP(value string) string {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	if net.ParseIP(value) == nil {
		return ""
	}
	return value
}

func isTrustedProxy(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	trustedProxyMu.RLock()
	defer trustedProxyMu.RUnlock()
	for _, network := range trustedProxyNets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// RateLimiter 是进程内的固定窗口限流，用于登录/验证码等敏感接口。
type RateLimiter struct {
	store  *database.Store
	window time.Duration
	limit  int
}

func NewRateLimiter(store *database.Store, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{store: store, limit: limit, window: window}
}

func (l *RateLimiter) Allow(key string) bool {
	if l.store == nil {
		return true
	}
	now := time.Now()
	reset := database.Ts(now.Add(l.window))
	var count int
	err := l.store.Write.Get(&count, `INSERT INTO TF_rate_limits(scope_key, count, reset_at, updated_at)
		VALUES(?, 1, ?, ?)
		ON CONFLICT(scope_key) DO UPDATE SET
			count = CASE WHEN TF_rate_limits.reset_at <= ? THEN 1 ELSE TF_rate_limits.count + 1 END,
			reset_at = CASE WHEN TF_rate_limits.reset_at <= ? THEN excluded.reset_at ELSE TF_rate_limits.reset_at END,
			updated_at = excluded.updated_at
		RETURNING count`,
		key, reset, database.Now(), database.Now(), database.Now())
	if err != nil {
		// 数据库异常时优先保证可用性；认证本身会继续做账号/密码校验。
		return true
	}
	return count <= l.limit
}

// Limit 返回按 IP + scope 限流的中间件。
func (l *RateLimiter) Limit(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(scope + "|" + ClientIP(r)) {
				respond.Fail(w, r, respond.NewError("RATE_LIMITED", "操作过于频繁，请稍后再试", 429))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
