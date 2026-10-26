package middleware

import "testing"

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
