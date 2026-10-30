package service

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"tfcloud/internal/config"
	"tfcloud/internal/database"
	"tfcloud/internal/secure"
)

func newTestAuth(t *testing.T) (*AuthService, *database.Store) {
	t.Helper()
	store, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		store.Close()
		t.Fatal(err)
	}
	cfg := &config.Config{AppKey: "0123456789abcdef0123456789abcdef", SessionTTL: 30 * 24 * time.Hour}
	return NewAuth(store, cfg, NewSettings(store, cfg), nil), store
}

func TestChallengeSingleConsume(t *testing.T) {
	auth, store := newTestAuth(t)
	defer store.Close()

	out, err := auth.IssueChallenge("login", "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := out["challenge_id"].(string)
	time.Sleep(350 * time.Millisecond)
	rec, ok := auth.challenge.snapshot(id)
	if !ok {
		t.Fatal("挑战记录不存在")
	}
	if _, err := auth.VerifyChallenge(id, rec.Answer, "127.0.0.1", "test-agent"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- auth.ConsumeChallenge(id, "login", "127.0.0.1", "test-agent")
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("同一个挑战并发消费应只有一次成功，实际 %d 次", success)
	}
}

func TestConsumeEmailCodeSingleUse(t *testing.T) {
	auth, store := newTestAuth(t)
	defer store.Close()

	const email = "user@example.com"
	code := "123456"
	hash, err := auth.hmacHex(code)
	if err != nil {
		t.Fatal(err)
	}
	now := database.Now()
	if _, err := store.Write.Exec(`INSERT INTO TF_email_verification_codes
		(email, email_norm, purpose, code_hash, attempts, expires_at, created_at)
		VALUES(?,?,?,?,0,?,?)`,
		email, NormalizeIdentity(email), "register", hash,
		database.Ts(time.Now().Add(time.Hour)), now); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- auth.ConsumeEmailCode(email, code, "register")
		}()
	}
	wg.Wait()
	close(results)

	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("同一个验证码并发消费应只有一次成功，实际 %d 次", success)
	}
}

func TestEmailBindingRequiresPassword(t *testing.T) {
	auth, store := newTestAuth(t)
	defer store.Close()

	hash, err := secure.HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	now := database.Now()
	res, err := store.Write.Exec(`INSERT INTO TF_users
		(username, username_norm, password_hash, display_name, role, status, daily_sign_email, push_channel,
		 password_migrated_at, created_at, updated_at)
		VALUES(?,?,?,?, 'user', 'active', 0, 'none', ?,?,?)`,
		"emailtest", NormalizeIdentity("emailtest"), hash, "", now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := res.LastInsertId()

	if _, err := auth.UpdateProfile(userID, map[string]any{"email": "new@example.com"}, ""); err == nil {
		t.Fatal("首次绑定邮箱必须提供当前密码")
	}
	if _, err := auth.UpdateProfile(userID, map[string]any{
		"email": "new@example.com", "current_password": "password123",
	}, ""); err != nil {
		t.Fatalf("正确密码应允许绑定邮箱: %v", err)
	}
}

func TestLoginLocksAccountAfterFailures(t *testing.T) {
	auth, store := newTestAuth(t)
	defer store.Close()

	hash, err := secure.HashPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	now := database.Now()
	if _, err := store.Write.Exec(`INSERT INTO TF_users
		(username, username_norm, password_hash, display_name, role, status, daily_sign_email, push_channel,
		 password_migrated_at, created_at, updated_at)
		VALUES(?,?,?,?, 'user', 'active', 0, 'none', ?,?,?)`,
		"locktest", NormalizeIdentity("locktest"), hash, "", now, now, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < loginFailureLimit; i++ {
		if _, err := auth.Login("locktest", "wrong-password", "127.0.0.1", "ua"); err == nil {
			t.Fatal("错误密码不应登录成功")
		}
	}
	if _, err := auth.Login("locktest", "password123", "127.0.0.1", "ua"); err == nil {
		t.Fatal("连续失败后应临时锁定账号")
	}
}

func TestChallengeSharedAcrossAuthInstances(t *testing.T) {
	auth, store := newTestAuth(t)
	defer store.Close()
	other := NewAuth(store, auth.cfg, auth.settings, auth.mail)

	out, err := auth.IssueChallenge("login", "127.0.0.1", "shared-agent")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := out["challenge_id"].(string)
	time.Sleep(350 * time.Millisecond)
	rec, ok := auth.challenge.snapshot(id)
	if !ok {
		t.Fatal("挑战记录不存在")
	}
	if _, err := other.VerifyChallenge(id, rec.Answer, "127.0.0.1", "shared-agent"); err != nil {
		t.Fatalf("另一个实例应能验证挑战: %v", err)
	}
	if err := other.ConsumeChallenge(id, "login", "127.0.0.1", "shared-agent"); err != nil {
		t.Fatalf("另一个实例应能消费挑战: %v", err)
	}
}
