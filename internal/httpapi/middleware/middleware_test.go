package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"tfcloud/internal/database"
)

func TestOriginAllowed(t *testing.T) {
	empty := &Middleware{}
	if allowed, credentials := empty.originAllowed("https://app.example.com"); allowed || credentials {
		t.Fatal("未配置 CORS 时必须拒绝跨域")
	}

	m := &Middleware{CORS: func() []string { return []string{"https://app.example.com"} }}
	if allowed, credentials := m.originAllowed("https://app.example.com/"); !allowed || !credentials {
		t.Fatal("显式允许的来源应允许并携带凭据")
	}
	if allowed, _ := m.originAllowed("https://evil.example.com"); allowed {
		t.Fatal("未列出的来源必须拒绝")
	}

	wildcard := &Middleware{CORS: func() []string { return []string{"*"} }}
	if allowed, credentials := wildcard.originAllowed("https://any.example.com"); !allowed || credentials {
		t.Fatal("通配符来源只能用于无凭据场景")
	}
}

func TestCSRFRequiresMatchingHeader(t *testing.T) {
	handler := CSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookie, Value: "0123456789abcdef0123456789abcdef"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺少 CSRF 头应拒绝，实际 %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookie, Value: "0123456789abcdef0123456789abcdef"})
	req.Header.Set("X-CSRF-Token", "0123456789abcdef0123456789abcdef")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("匹配的 CSRF 头应通过，实际 %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("登录入口应豁免 CSRF，实际 %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer explicit-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("显式 Bearer 调用应豁免 CSRF，实际 %d", rec.Code)
	}
}

func TestRateLimiterPersistsAcrossInstances(t *testing.T) {
	store, err := database.Open(filepath.Join(t.TempDir(), "rate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := NewRateLimiter(store, 2, time.Minute)
	second := NewRateLimiter(store, 2, time.Minute)
	if !first.Allow("login|1.2.3.4") {
		t.Fatal("第一次请求应通过")
	}
	if !first.Allow("login|1.2.3.4") {
		t.Fatal("第二次请求应通过")
	}
	if second.Allow("login|1.2.3.4") {
		t.Fatal("另一个实例应共享已用次数")
	}
	if !second.Allow("login|5.6.7.8") {
		t.Fatal("不同来源应独立计数")
	}
}
