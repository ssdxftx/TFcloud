package middleware

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPIgnoresForwardedHeadersFromPublicPeer(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.9:4321"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	r.Header.Set("CF-Connecting-IP", "5.6.7.8")
	r.Header.Set("X-Real-IP", "9.9.9.9")
	if got := ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("公网直连应忽略转发头，实际 %s", got)
	}
}

func TestClientIPTrustsForwardedHeadersFromConfiguredProxy(t *testing.T) {
	SetTrustedProxies([]string{"127.0.0.0/8", "10.0.0.0/8"})
	defer SetTrustedProxies([]string{"127.0.0.0/8", "::1/128"})

	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:4321"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 10.0.0.2")
	if got := ClientIP(r); got != "1.2.3.4" {
		t.Fatalf("本机代理应取 XFF 第一段，实际 %s", got)
	}

	r2 := httptest.NewRequest("GET", "/", nil)
	r2.RemoteAddr = "10.0.0.5:1234"
	r2.Header.Set("CF-Connecting-IP", "9.9.9.9")
	if got := ClientIP(r2); got != "9.9.9.9" {
		t.Fatalf("已配置的内网代理应信任 CF 头，实际 %s", got)
	}
}

func TestClientIPIgnoresForwardedHeadersFromUnconfiguredPrivatePeer(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.8:1234"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := ClientIP(r); got != "192.168.1.8" {
		t.Fatalf("未配置的私网来源不应信任转发头，实际 %s", got)
	}
}

func TestClientIPRejectsMalformedForwardedValue(t *testing.T) {
	SetTrustedProxies([]string{"127.0.0.0/8"})
	defer SetTrustedProxies([]string{"127.0.0.0/8", "::1/128"})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "not-an-ip")
	if got := ClientIP(r); got != "127.0.0.1" {
		t.Fatalf("非法转发头应回退到代理地址，实际 %s", got)
	}
}
