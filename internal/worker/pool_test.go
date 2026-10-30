package worker

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"tfcloud/internal/config"
	"tfcloud/internal/database"
)

func TestCleanupRetentionRemovesOldFinishedData(t *testing.T) {
	store, err := database.Open(filepath.Join(t.TempDir(), "retention.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := database.Ts(time.Now().AddDate(0, 0, -90))
	now := database.Now()
	if _, err := store.Write.Exec(`INSERT INTO TF_users(username,username_norm,role,status,created_at,updated_at)
		VALUES('retention','retention','user','active',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := store.Read.Get(&userID, `SELECT id FROM TF_users WHERE username_norm='retention'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write.Exec(`INSERT INTO TF_plugin_accounts
		(user_id,plugin_code,display_name,status,created_at,updated_at) VALUES(?, 'tieba','retention','active',?,?)`,
		userID, now, now); err != nil {
		t.Fatal(err)
	}
	var accountID int64
	if err := store.Read.Get(&accountID, `SELECT id FROM TF_plugin_accounts WHERE user_id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write.Exec(`INSERT INTO TF_sign_tasks
		(task_no,user_id,account_id,plugin_code,status,created_at,updated_at)
		VALUES('old-task',?,?,'tieba','succeeded',?,?)`, userID, accountID, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write.Exec(`INSERT INTO TF_audit_logs(action,created_at) VALUES('old-audit',?)`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write.Exec(`INSERT INTO TF_mail_records
		(message_no,recipient,recipient_hash,status,created_at,updated_at)
		VALUES('old-mail','user@example.com','hash','sent',?,?)`, old, old); err != nil {
		t.Fatal(err)
	}

	p := &Pool{store: store, logger: slog.Default(), cfg: &config.Config{RetentionDays: 30, AuditRetentionDays: 30}}
	p.cleanupRetention()

	for _, query := range []string{
		`SELECT COUNT(1) FROM TF_sign_tasks WHERE task_no='old-task'`,
		`SELECT COUNT(1) FROM TF_audit_logs WHERE action='old-audit'`,
		`SELECT COUNT(1) FROM TF_mail_records WHERE message_no='old-mail'`,
	} {
		var count int
		if err := store.Read.Get(&count, query); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("旧数据应被清理: %s -> %d", query, count)
		}
	}
}
