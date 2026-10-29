package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMaintenanceGuardAllowsPublicAndAuthPaths(t *testing.T) {
	m := &Middleware{Maintenance: func() bool { return true }}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, path := range []string{"/api/site", "/api/auth/login", "/api/install/status", "/health/live"} {
		rec := httptest.NewRecorder()
		m.MaintenanceGuard(next).ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("维护模式应放行 %s，实际 %d", path, rec.Code)
		}
	}
}

func TestMaintenanceGuardBlocksUserAPI(t *testing.T) {
	m := &Middleware{Maintenance: func() bool { return true }}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	rec := httptest.NewRecorder()
	m.MaintenanceGuard(next).ServeHTTP(rec, httptest.NewRequest("GET", "/api/me", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("维护模式应拦截普通接口，实际 %d", rec.Code)
	}
}
