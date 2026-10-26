package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"tfcloud/internal/config"
	"tfcloud/internal/database"
	"tfcloud/internal/httpapi/respond"
	"tfcloud/internal/secure"
)

type InstallService struct {
	store    *database.Store
	cfg      *config.Config
	settings *SettingsService
}

func NewInstall(store *database.Store, cfg *config.Config, settings *SettingsService) *InstallService {
	return &InstallService{store: store, cfg: cfg, settings: settings}
}

func (s *InstallService) Status() map[string]any {
	installed := s.cfg.IsInstalled()
	_, envErr := os.Stat(filepath.Join(s.cfg.DataDir, ".env"))
	_, markerErr := os.Stat(s.cfg.InstalledMarker())
	return map[string]any{
		"installed":     installed,
		"lock_exists":   markerErr == nil,
		"env_exists":    envErr == nil,
		"can_install":   !installed,
		"restart_hint":  "安装完成后无需重启，配置即时生效",
		"storage":       "sqlite",
		"data_dir":      s.cfg.DataDir,
		"database_path": s.cfg.Database,
	}
}

func (s *InstallService) Checks() map[string]any {
	var items []map[string]any
	add := func(key, label string, ok bool, detail string) {
		items = append(items, map[string]any{"key": key, "label": label, "ok": ok, "detail": detail})
	}

	add("go_runtime", "Go 运行时", true, runtime.Version())
	add("os", "操作系统", true, runtime.GOOS+"/"+runtime.GOARCH)

	dataWritable := dirWritable(s.cfg.DataDir)
	add("data_dir", "数据目录可写", dataWritable, s.cfg.DataDir)

	dbOK := false
	if err := s.store.Write.Ping(); err == nil {
		var version string
		_ = s.store.Read.Get(&version, "SELECT sqlite_version()")
		dbOK = true
		add("sqlite", "SQLite 内嵌数据库", true, "SQLite "+version)
	} else {
		add("sqlite", "SQLite 内嵌数据库", false, err.Error())
	}

	freeMB := freeDiskMB(s.cfg.DataDir)
	add("disk", "磁盘空间", freeMB >= 200, fmt.Sprintf("可用 %d MB", freeMB))

	installed := s.cfg.IsInstalled()
	add("not_installed", "未重复安装", !installed, map[bool]string{true: "可以开始安装", false: "站点已完成安装"}[!installed])

	passed := dataWritable && dbOK && freeMB >= 200 && !installed
	return map[string]any{"passed": passed, "checks": items}
}

// TestStorage 对应原 /install/test-database：原先测 MySQL，现在测内嵌库可写与容量。
func (s *InstallService) TestStorage() map[string]any {
	var version string
	_ = s.store.Read.Get(&version, "SELECT sqlite_version()")
	probe := filepath.Join(s.cfg.DataDir, ".write_probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return map[string]any{"ok": false, "message": "数据目录不可写：" + err.Error()}
	}
	_ = os.Remove(probe)
	return map[string]any{
		"ok":             true,
		"server_version": "SQLite " + version,
		"message":        fmt.Sprintf("内嵌数据库可读写，可用空间 %d MB", freeDiskMB(s.cfg.DataDir)),
		"database_path":  s.cfg.Database,
		"mode":           "WAL",
	}
}

type InstallInput struct {
	SiteURL       string `json:"site_url"`
	SessionSecure bool   `json:"session_secure"`
	Admin         struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	} `json:"admin"`
}

func (s *InstallService) Run(in InstallInput) (map[string]any, error) {
	if s.cfg.IsInstalled() {
		return nil, respond.NewError("ALREADY_INSTALLED", "站点已完成安装", 409)
	}
	siteURL := strings.TrimRight(strings.TrimSpace(in.SiteURL), "/")
	if siteURL == "" {
		return nil, respond.NewError("VALIDATION_FAILED", "请填写站点地址", 422)
	}
	if !strings.HasPrefix(siteURL, "http://") && !strings.HasPrefix(siteURL, "https://") {
		siteURL = "https://" + siteURL
	}
	username := strings.TrimSpace(in.Admin.Username)
	if len([]rune(username)) < 2 || len([]rune(username)) > 64 {
		return nil, respond.NewError("VALIDATION_FAILED", "管理员用户名长度必须为2到64个字符", 422)
	}
	if len(in.Admin.Password) < 8 || len(in.Admin.Password) > 1024 {
		return nil, respond.NewError("VALIDATION_FAILED", "管理员密码长度必须为8到1024个字符", 422)
	}

	appKey := "base64:" + base64Std(secure.RandomBytes(32))
	fingerprintKey := secure.RandomHex(32)

	envLines := map[string]string{
		"APP_ENV":                      "production",
		"APP_KEY":                      appKey,
		"CREDENTIAL_KEY_VERSION":       "1",
		"CREDENTIAL_FINGERPRINT_KEY":   fingerprintKey,
		"TF_DATA_DIR":                  s.cfg.DataDir,
		"SQLITE_PATH":                  s.cfg.Database,
		"HTTP_ADDR":                    s.cfg.HTTPAddr,
		"CORS_ALLOWED_ORIGINS":         siteURL,
		"SESSION_SECURE":               strconv.FormatBool(strings.HasPrefix(siteURL, "https://")),
		"SIGN_WORKER_ENABLED":          "true",
		"SIGN_WORKER_COUNT":            strconv.Itoa(s.cfg.SignWorkerCount),
		"SIGN_SCHEDULER_ENABLED":       "true",
		"TIEBA_RETRY_WORKER_ENABLED":   "true",
		"MAIL_WORKER_ENABLED":          "true",
		"MAINTENANCE_ENABLED":          "true",
		"BILIBILI_LIVE_WORKER_ENABLED": "true",
		"TASK_LOCK_TIMEOUT_SECONDS":    "7200",
		"UPDATE_VERSION_URL":           s.cfg.UpdateVersionURL,
		"UPDATE_PAGE_URL":              s.cfg.UpdatePageURL,
	}
	if err := writeEnv(filepath.Join(s.cfg.DataDir, ".env"), envLines); err != nil {
		return nil, respond.Internal("写入配置文件失败：" + err.Error())
	}
	s.cfg.Reload()

	hash, err := secure.HashPassword(in.Admin.Password)
	if err != nil {
		return nil, respond.Internal("密码加密失败")
	}
	now := database.Now()
	displayName := strings.TrimSpace(in.Admin.DisplayName)
	if displayName == "" {
		displayName = username
	}
	tx, err := s.store.Write.Beginx()
	if err != nil {
		return nil, respond.Internal("初始化数据库失败")
	}
	defer func() { _ = tx.Rollback() }()

	normalized := NormalizeIdentity(username)
	if _, err := tx.Exec(`INSERT INTO TF_users
		(username, username_norm, password_hash, display_name, role, status, daily_sign_email, push_channel,
		 password_migrated_at, created_at, updated_at)
		VALUES(?,?,?,?, 'admin', 'active', 0, 'none', ?,?,?)`,
		username, normalized, hash, displayName, now, now, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, respond.NewError("USER_FIELD_DUPLICATE", "管理员用户名已存在", 409)
		}
		return nil, respond.Internal("创建管理员失败")
	}
	siteSettings := map[string]any{
		"base_url":             siteURL,
		"registration_enabled": false,
	}
	for key, value := range siteSettings {
		buf, _ := marshalNoEscape(value)
		if _, err := tx.Exec(`INSERT INTO TF_settings(setting_key, setting_value, is_secret, updated_at)
			VALUES(?,?,0,?) ON CONFLICT(setting_key) DO UPDATE SET setting_value=excluded.setting_value,
			updated_at=excluded.updated_at`, "site."+key, buf, now); err != nil {
			return nil, respond.Internal("写入站点配置失败")
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, respond.Internal("初始化数据库失败")
	}

	marker := fmt.Sprintf(`{"installed_at":%q,"site_url":%q,"admin":%q,"version":1}`,
		time.Now().Format(time.RFC3339), siteURL, username)
	if err := os.WriteFile(s.cfg.InstalledMarker(), []byte(marker), 0o600); err != nil {
		return nil, respond.Internal("写入安装标记失败：" + err.Error())
	}

	return map[string]any{
		"installed":      true,
		"admin_username": username,
		"site_url":       siteURL,
		"server_resources": map[string]any{
			"cpu_cores": runtime.NumCPU(),
			"memory_mb": totalMemoryMB(),
		},
		"process_counts": map[string]any{
			"webman":      1,
			"sign_worker": s.cfg.SignWorkerCount,
		},
		"restart_required": false,
		"message":          "安装完成，站点已就绪",
	}, nil
}

func dirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return false
	}
	probe := filepath.Join(dir, ".probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

func writeEnv(path string, values map[string]string) error {
	var b strings.Builder
	b.WriteString("# 由安装向导生成，请勿泄露\n")
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	// 稳定输出，便于 diff
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(values[k])
		b.WriteString("\n")
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func freeDiskMB(dir string) uint64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0
	}
	return stat.Bavail * uint64(stat.Bsize) / 1024 / 1024
}

func totalMemoryMB() int {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if kb, err := strconv.Atoi(fields[1]); err == nil {
					return kb / 1024
				}
			}
		}
	}
	return 0
}
