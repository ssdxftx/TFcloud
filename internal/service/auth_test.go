package service

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"tfcloud/internal/config"
	"tfcloud/internal/database"
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
	if _, err := auth.VerifyChallenge(id, 400, 3, "127.0.0.1", "test-agent"); err != nil {
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
