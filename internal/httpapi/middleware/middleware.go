package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

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

const SessionCookie = "tf_sign_token"

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
	Auth      Authenticator
	Installed func() bool
	CORS      []string
	Logger    *slog.Logger
}

func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-Id")
			if len(id) != 32 {
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
		if origin != "" && m.originAllowed(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id, Accept")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Middleware) originAllowed(origin string) bool {
	if len(m.CORS) == 0 {
		return true // 同源部署为主，未配置时放开预检（凭据仍受 Token 保护）
	}
	trimmed := strings.TrimRight(origin, "/")
	for _, allowed := range m.CORS {
		if allowed == "*" || strings.EqualFold(allowed, trimmed) {
			return true
		}
	}
	return false
}

// InstallGuard 在未安装时把除安装接口外的请求挡在门外。
func (m *Middleware) InstallGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.Installed() {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/install") || strings.HasPrefix(p, "/health") ||
			strings.HasPrefix(p, "/assets/") || p == "/install" || p == "/favicon.svg" ||
			strings.HasPrefix(p, "/fonts/") || p == "/og.png" || strings.HasPrefix(p, "/static/") {
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

func ClientIP(r *http.Request) string {
	if v := r.Header.Get("CF-Connecting-IP"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		if idx := strings.Index(v, ","); idx > 0 {
			return strings.TrimSpace(v[:idx])
		}
		return strings.TrimSpace(v)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// RateLimiter 是进程内的固定窗口限流，用于登录/验证码等敏感接口。
type RateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	limit   int
	buckets map[string]*bucket
}

type bucket struct {
	count int
	reset time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{limit: limit, window: window, buckets: make(map[string]*bucket)}
}

func (l *RateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[key]
	if !ok || now.After(b.reset) {
		l.buckets[key] = &bucket{count: 1, reset: now.Add(l.window)}
		if len(l.buckets) > 20000 {
			for k, v := range l.buckets {
				if now.After(v.reset) {
					delete(l.buckets, k)
				}
			}
		}
		return true
	}
	if b.count >= l.limit {
		return false
	}
	b.count++
	return true
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
