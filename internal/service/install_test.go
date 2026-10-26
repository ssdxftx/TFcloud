package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeSiteURL(t *testing.T) {
	site, origin, scheme, err := normalizeSiteURL("example.com/sign/")
	if err != nil {
		t.Fatal(err)
	}
	if site != "https://example.com/sign" || origin != "https://example.com" || scheme != "https" {
		t.Fatalf("unexpected normalize result: %q %q %q", site, origin, scheme)
	}

	if _, _, _, err := normalizeSiteURL("https://user:pass@example.com"); err == nil {
		t.Fatal("带用户信息的地址必须拒绝")
	}
	if _, _, _, err := normalizeSiteURL("ftp://example.com"); err == nil {
		t.Fatal("非 http(s) 协议必须拒绝")
	}
	if _, _, _, err := normalizeSiteURL("https://example.com\nSESSION_SECURE=false"); err == nil {
		t.Fatal("包含换行的地址必须拒绝")
	}
	if _, _, _, err := normalizeSiteURL("https://example.com/?a=1"); err == nil {
		t.Fatal("带查询参数的地址必须拒绝")
	}
}

func TestWriteEnvRejectsNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	err := writeEnv(path, map[string]string{"APP_KEY": "ok", "CORS_ALLOWED_ORIGINS": "https://a\nB=1"})
	if err == nil || !strings.Contains(err.Error(), "非法字符") {
		t.Fatalf("应拒绝换行注入，实际 %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("校验失败时不应写入文件")
	}
}
