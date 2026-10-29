package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tfcloud/internal/config"
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

func TestInstallReusesExistingSecretsAndEnv(t *testing.T) {
	store := newTestStore(t)
	dir := t.TempDir()
	appKey := "base64:MTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI="
	fingerprint := "existing-fingerprint"
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("APP_KEY="+appKey+"\nCREDENTIAL_FINGERPRINT_KEY="+fingerprint+"\nCUSTOM_FLAG=keep-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		DataDir: dir, Database: store.Path, HTTPAddr: ":8787",
		AppKey: appKey, FingerprintKey: fingerprint, SignWorkerCount: 2,
	}
	t.Setenv("APP_KEY", "")
	t.Setenv("CREDENTIAL_FINGERPRINT_KEY", "")
	t.Setenv("SESSION_SECURE", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	svc := NewInstall(store, cfg, NewSettings(store, cfg))
	input := InstallInput{SiteURL: "https://example.com"}
	input.Admin.Username = "admin"
	input.Admin.Password = "password123"
	if _, err := svc.Run(input); err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	raw, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	envText := string(raw)
	if !strings.Contains(envText, "APP_KEY="+appKey) {
		t.Fatal("安装不应覆盖已有 APP_KEY")
	}
	if !strings.Contains(envText, "CREDENTIAL_FINGERPRINT_KEY="+fingerprint) {
		t.Fatal("安装不应覆盖已有指纹密钥")
	}
	if !strings.Contains(envText, "CUSTOM_FLAG=keep-me") {
		t.Fatal("安装应保留 .env 中的自定义配置")
	}
}
