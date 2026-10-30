package database

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaFS embed.FS

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migration struct {
	Version int
	Name    string
	Body    []byte
}

const (
	// TimeLayout 与 MySQL DATETIME 的字符串输出保持一致，接口对前端零差异。
	TimeLayout   = "2006-01-02 15:04:05"
	TimeLayoutMs = "2006-01-02 15:04:05.000"
)

// Store 持有读写两个连接池。
// SQLite 在 WAL 模式下允许「多读一写」，但同一个 *sql.DB 内混合读写会互相等待，
// 因此写池固定为单连接，读池按 CPU 数量放开。这是本项目并发正确性的基石。
type Store struct {
	Read  *sqlx.DB
	Write *sqlx.DB
	Path  string
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("创建数据库目录失败: %w", err)
		}
	}

	dsn := buildDSN(path)
	write, err := sqlx.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(0)
	if err := write.Ping(); err != nil {
		return nil, fmt.Errorf("数据库不可用: %w", err)
	}
	hardenPermissions(path)

	read, err := sqlx.Open("sqlite", dsn+"&_pragma=query_only(1)")
	if err != nil {
		return nil, fmt.Errorf("打开只读连接失败: %w", err)
	}
	read.SetMaxOpenConns(maxInt(4, 4))
	read.SetMaxIdleConns(4)
	read.SetConnMaxLifetime(0)
	hardenPermissions(path)

	return &Store{Read: read, Write: write, Path: path}, nil
}

func hardenPermissions(path string) {
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		_ = os.Chmod(name, 0o600)
	}
}

func buildDSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "temp_store(MEMORY)")
	q.Add("_pragma", "cache_size(-32000)")
	return "file:" + path + "?" + q.Encode()
}

func (s *Store) Close() error {
	if s.Read != nil {
		_ = s.Read.Close()
	}
	if s.Write != nil {
		return s.Write.Close()
	}
	return nil
}

// Migrate 先执行基线 schema，再按编号应用增量迁移。
func (s *Store) Migrate(ctx context.Context) error {
	body, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		return err
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	tx, err := s.Write.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	if err := applyStatements(ctx, tx, "schema.sql", body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, applied_at) VALUES(1, ?) ON CONFLICT(version) DO NOTHING`,
		NowMs()); err != nil {
		return err
	}
	var current int
	if err := tx.GetContext(ctx, &current, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`); err != nil {
		return err
	}
	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		if err := applyStatements(ctx, tx, m.Name, m.Body); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES(?, ?)`, m.Version, NowMs()); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	hardenPermissions(s.Path)
	return nil
}

func applyStatements(ctx context.Context, tx *sqlx.Tx, name string, body []byte) error {
	for i, stmt := range splitStatements(string(body)) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("执行迁移 %s 语句 #%d 失败: %w", name, i+1, err)
		}
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	out := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("迁移文件名无效: %s", entry.Name())
		}
		version, err := strconv.Atoi(parts[0])
		if err != nil || version <= 1 {
			return nil, fmt.Errorf("迁移版本无效: %s", entry.Name())
		}
		body, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{Version: version, Name: entry.Name(), Body: body})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Checkpoint 定期截断 WAL，避免单文件无限增长。
func (s *Store) Checkpoint(ctx context.Context) error {
	_, err := s.Write.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

func (s *Store) Backup(ctx context.Context, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	_, err := s.Write.ExecContext(ctx, "VACUUM INTO ?", target)
	return err
}

func splitStatements(body string) []string {
	raw := strings.Split(body, ";")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		// 去掉纯注释块
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		var b strings.Builder
		for _, line := range strings.Split(trimmed, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		if v := strings.TrimSpace(b.String()); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func Now() string { return time.Now().Format(TimeLayout) }

func NowMs() string { return time.Now().Format(TimeLayoutMs) }

func Ts(t time.Time) string { return t.Format(TimeLayout) }

func TsMs(t time.Time) string { return t.Format(TimeLayoutMs) }

// Parse 兼容秒级与毫秒级两种历史格式。
func Parse(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{TimeLayoutMs, TimeLayout, time.RFC3339} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Maps 执行查询并返回按行组织的 map。
// sqlx 的 Select 不支持多列 map 目标，这里统一走 MapScan，并把 []byte 归一成 string，
// 避免 JSON 输出被 base64 编码。
func (s *Store) Maps(query string, args ...any) ([]map[string]any, error) {
	rows, err := s.Read.Queryx(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		row := map[string]any{}
		if err := rows.MapScan(row); err != nil {
			return nil, err
		}
		for k, v := range row {
			if b, ok := v.([]byte); ok {
				row[k] = string(b)
			}
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// Map 返回单行结果；无数据时返回 sql.ErrNoRows 语义的错误。
func (s *Store) Map(query string, args ...any) (map[string]any, error) {
	rows, err := s.Maps(query, args...)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return rows[0], nil
}
