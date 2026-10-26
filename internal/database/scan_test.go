package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrateAndMaps(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	var tables int
	if err := store.Read.Get(&tables, `SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name LIKE 'TF_%'`); err != nil {
		t.Fatal(err)
	}
	if tables < 21 {
		t.Fatalf("应创建至少 21 张 TF_ 表，实际 %d", tables)
	}

	var plugins int
	if err := store.Read.Get(&plugins, `SELECT COUNT(1) FROM TF_plugins`); err != nil {
		t.Fatal(err)
	}
	if plugins != 7 {
		t.Fatalf("应预置 7 个插件，实际 %d", plugins)
	}

	// 幂等：重复迁移不应报错
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("重复迁移应幂等: %v", err)
	}

	if _, err := store.Write.Exec(`INSERT INTO TF_users(username, username_norm, role, status, created_at, updated_at)
		VALUES('u1','u1','user','active',?,?)`, Now(), Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := store.Maps(`SELECT id, username, role FROM TF_users WHERE username_norm = ?`, "u1")
	if err != nil {
		t.Fatalf("Maps 查询失败: %v", err)
	}
	if len(rows) != 1 || rows[0]["username"] != "u1" {
		t.Fatalf("Maps 结果错误: %#v", rows)
	}
	// 唯一索引（忽略大小写）必须生效
	if _, err := store.Write.Exec(`INSERT INTO TF_users(username, username_norm, role, status, created_at, updated_at)
		VALUES('U1','u1','user','active',?,?)`, Now(), Now()); err == nil {
		t.Fatal("username_norm 唯一索引未生效")
	}

	// 读池必须是 query_only，防止误写破坏单写连接模型。
	if _, err := store.Read.Exec(`INSERT INTO TF_users(username, username_norm, role, status, created_at, updated_at)
		VALUES('u2','u2','user','active',?,?)`, Now(), Now()); err == nil {
		t.Fatal("读池不应允许写入")
	}
}
