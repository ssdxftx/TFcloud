package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInstallGuardOnlyAllowsInstallPrefix(t *testing.T) {
	m := &Middleware{Installed: func() bool { return false }}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })

	blocked := httptest.NewRecorder()
	m.InstallGuard(next).ServeHTTP(blocked, httptest.NewRequest("GET", "/api/installx", nil))
	if blocked.Code != http.StatusServiceUnavailable {
		t.Fatalf("未安装时 /api/installx 应被拦截，实际 %d", blocked.Code)
	}

	for _, path := range []string{"/api/install", "/api/install/status", "/api/site", "/health/live"} {
		allowed := httptest.NewRecorder()
		m.InstallGuard(next).ServeHTTP(allowed, httptest.NewRequest("GET", path, nil))
		if allowed.Code != http.StatusNoContent {
			t.Fatalf("未安装时 %s 应放行，实际 %d", path, allowed.Code)
		}
	}
}
