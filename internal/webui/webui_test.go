package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func testHandler() *spaHandler {
	return &spaHandler{root: fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><div id=root></div>")},
		"assets/app-abc123.js": {Data: []byte("console.log('app')")},
		"fonts/app.woff":       {Data: []byte("font-data")},
		"favicon.svg":          {Data: []byte("<svg></svg>")},
	}}
}

func TestSPAHandlerCacheHeaders(t *testing.T) {
	h := testHandler()
	tests := []struct {
		name      string
		path      string
		status    int
		cache     string
		cdnCache  string
		surrogate string
		wantETag  bool
	}{
		{
			name:     "index",
			path:     "/",
			status:   http.StatusOK,
			cache:    htmlCacheControl,
			cdnCache: htmlCDNCache,
			wantETag: true,
		},
		{
			name:     "spa fallback",
			path:     "/dashboard",
			status:   http.StatusOK,
			cache:    htmlCacheControl,
			cdnCache: htmlCDNCache,
			wantETag: true,
		},
		{
			name:      "hashed asset",
			path:      "/assets/app-abc123.js",
			status:    http.StatusOK,
			cache:     immutableCacheControl,
			cdnCache:  immutableCDNCache,
			surrogate: immutableSurrogate,
			wantETag:  true,
		},
		{
			name:      "font",
			path:      "/fonts/app.woff",
			status:    http.StatusOK,
			cache:     immutableCacheControl,
			cdnCache:  immutableCDNCache,
			surrogate: immutableSurrogate,
			wantETag:  true,
		},
		{
			name:      "root static file",
			path:      "/favicon.svg",
			status:    http.StatusOK,
			cache:     staticCacheControl,
			cdnCache:  staticCDNCache,
			surrogate: staticSurrogate,
			wantETag:  true,
		},
		{
			name:   "missing asset",
			path:   "/assets/missing.js",
			status: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.cache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.cache)
			}
			if got := rec.Header().Get("CDN-Cache-Control"); got != tt.cdnCache {
				t.Errorf("CDN-Cache-Control = %q, want %q", got, tt.cdnCache)
			}
			if got := rec.Header().Get("Surrogate-Control"); got != tt.surrogate {
				t.Errorf("Surrogate-Control = %q, want %q", got, tt.surrogate)
			}
			if tt.wantETag && rec.Header().Get("ETag") == "" {
				t.Error("ETag is empty")
			}
		})
	}
}

func TestSPAHandlerETagRevalidation(t *testing.T) {
	h := testHandler()
	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/assets/app-abc123.js", nil))
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("first response has no ETag")
	}

	req := httptest.NewRequest(http.MethodGet, "/assets/app-abc123.js", nil)
	req.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	h.ServeHTTP(second, req)

	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want %d", second.Code, http.StatusNotModified)
	}
	if got := second.Header().Get("Cache-Control"); got != immutableCacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, immutableCacheControl)
	}
}
