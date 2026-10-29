package config

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestReloadUpdatesRuntimeFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TF_DATA_DIR", dir)
	appKey := "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	env := "APP_KEY=" + appKey + "\n" +
		"CREDENTIAL_FINGERPRINT_KEY=fingerprint\n" +
		"SESSION_SECURE=true\n" +
		"CORS_ALLOWED_ORIGINS=https://example.com\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.mu.Lock()
	cfg.AppKey = ""
	cfg.FingerprintKey = ""
	cfg.SessionSecure = false
	cfg.CORSAllowedOrigins = nil
	cfg.mu.Unlock()

	cfg.Reload()

	if cfg.SecretKey() != appKey {
		t.Fatalf("APP_KEY 未热更新: %q", cfg.SecretKey())
	}
	if cfg.FingerprintSecret() != "fingerprint" {
		t.Fatalf("指纹密钥未热更新: %q", cfg.FingerprintSecret())
	}
	if !cfg.SessionSecureEnabled() {
		t.Fatal("SESSION_SECURE 未热更新")
	}
	origins := cfg.AllowedOrigins()
	if len(origins) != 1 || origins[0] != "https://example.com" {
		t.Fatalf("CORS 来源未热更新: %#v", origins)
	}
}

func TestReloadConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TF_DATA_DIR", dir)
	appKey := "base64:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_KEY="+appKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = cfg.SecretKey()
					_ = cfg.AllowedOrigins()
					_ = cfg.IsInstalled()
				}
			}
		}()
	}
	for i := 0; i < 10; i++ {
		cfg.Reload()
	}
	close(stop)
	wg.Wait()
}

func TestExistingEnvFilesSkipsMissing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.env")
	present := filepath.Join(dir, "present.env")
	if err := os.WriteFile(present, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := existingEnvFiles([]string{missing, present, present})
	if len(got) != 1 || got[0] != present {
		t.Fatalf("应只保留存在的文件，实际 %#v", got)
	}
}

func TestApplyInstallSyncsProcessEnv(t *testing.T) {
	t.Setenv("APP_KEY", "old")
	t.Setenv("CREDENTIAL_FINGERPRINT_KEY", "")
	t.Setenv("SESSION_SECURE", "")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	cfg := &Config{}
	key := "base64:MTIzNDU2Nzg5MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTI="
	cfg.ApplyInstall(key, "fp-new", true, []string{"https://example.com"})
	if cfg.SecretKey() != key || cfg.FingerprintSecret() != "fp-new" || !cfg.SessionSecureEnabled() {
		t.Fatalf("安装配置未同步: %#v", cfg)
	}
	if os.Getenv("APP_KEY") != key {
		t.Fatalf("进程环境 APP_KEY 未同步: %q", os.Getenv("APP_KEY"))
	}
}
