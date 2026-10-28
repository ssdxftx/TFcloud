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
