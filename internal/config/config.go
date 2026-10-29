package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

// Config 是进程内唯一配置源。环境变量名与 PHP 版保持一致，
// 只是把 DB_* 换成 DATA_DIR/SQLITE_PATH，运维迁移成本最低。
type Config struct {
	mu sync.RWMutex

	AppEnv   string
	HTTPAddr string

	DataDir    string
	Database   string
	UploadDir  string
	StaticDist string

	AppKey                string
	CredentialKeyVersion  int
	FingerprintKey        string
	LegacyPasswordSalt    string
	SessionTTL            time.Duration
	SessionSecure         bool
	CORSAllowedOrigins    []string
	TrustedProxyCIDRs     []string
	TaskLockTimeout       time.Duration
	SignWorkerEnabled     bool
	SignWorkerCount       int
	SignSchedulerEnabled  bool
	TiebaRetryEnabled     bool
	SignSchedulerInterval time.Duration
	MailWorkerEnabled     bool
	MailWorkerInterval    time.Duration
	BilibiliLiveEnabled   bool
	BilibiliLiveWorkers   int
	MaintenanceEnabled    bool
	UpdateVersionURL      string
	UpdatePageURL         string
	UpdateGithubToken     string

	// 运行时填充（安装向导写入后热更新）
	Installed bool
}

func Load() (*Config, error) {
	// 数据目录优先，其次当前目录：容器里 /data 是持久卷，配置必须落在这里。
	dataDir := env("TF_DATA_DIR", defaultDataDir())
	candidates := append([]string{filepath.Join(dataDir, ".env")}, envFileCandidates()...)
	if files := existingEnvFiles(candidates); len(files) > 0 {
		_ = godotenv.Load(files...)
	}
	c := &Config{
		AppEnv:                env("APP_ENV", "production"),
		HTTPAddr:              env("HTTP_ADDR", ":8787"),
		DataDir:               dataDir,
		Database:              env("SQLITE_PATH", filepath.Join(dataDir, "tfcloud.db")),
		UploadDir:             env("TF_UPLOAD_DIR", filepath.Join(dataDir, "uploads")),
		StaticDist:            env("TF_STATIC_DIST", ""),
		AppKey:                env("APP_KEY", ""),
		CredentialKeyVersion:  envInt("CREDENTIAL_KEY_VERSION", 1),
		FingerprintKey:        env("CREDENTIAL_FINGERPRINT_KEY", ""),
		LegacyPasswordSalt:    env("LEGACY_PASSWORD_SALT", "qq1790716272"),
		SessionTTL:            time.Duration(envInt("SESSION_TTL_DAYS", 30)) * 24 * time.Hour,
		SessionSecure:         envBool("SESSION_SECURE", false),
		CORSAllowedOrigins:    splitCSV(env("CORS_ALLOWED_ORIGINS", "")),
		TrustedProxyCIDRs:     splitCSV(env("TRUSTED_PROXY_CIDRS", "127.0.0.0/8,::1/128")),
		TaskLockTimeout:       time.Duration(envInt("TASK_LOCK_TIMEOUT_SECONDS", 7200)) * time.Second,
		SignWorkerEnabled:     envBool("SIGN_WORKER_ENABLED", true),
		SignWorkerCount:       envInt("SIGN_WORKER_COUNT", defaultWorkers()),
		SignSchedulerEnabled:  envBool("SIGN_SCHEDULER_ENABLED", true),
		TiebaRetryEnabled:     envBool("TIEBA_RETRY_WORKER_ENABLED", true),
		SignSchedulerInterval: time.Duration(envInt("SIGN_SCHEDULER_INTERVAL_SECONDS", 30)) * time.Second,
		MailWorkerEnabled:     envBool("MAIL_WORKER_ENABLED", true),
		MailWorkerInterval:    time.Duration(envInt("MAIL_WORKER_INTERVAL_SECONDS", 10)) * time.Second,
		BilibiliLiveEnabled:   envBool("BILIBILI_LIVE_WORKER_ENABLED", true),
		BilibiliLiveWorkers:   envInt("BILIBILI_LIVE_WORKER_COUNT", 2),
		MaintenanceEnabled:    envBool("MAINTENANCE_ENABLED", true),
		UpdateVersionURL:      env("UPDATE_VERSION_URL", "https://raw.githubusercontent.com/longfie/TFcloud/main/backend/VERSION"),
		UpdatePageURL:         env("UPDATE_PAGE_URL", "https://github.com/longfie/TFcloud/releases"),
		UpdateGithubToken:     env("UPDATE_GITHUB_TOKEN", ""),
	}

	if err := os.MkdirAll(c.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}
	if err := os.MkdirAll(c.UploadDir, 0o750); err != nil {
		return nil, fmt.Errorf("创建上传目录失败: %w", err)
	}
	return c, nil
}

func (c *Config) InstalledMarker() string { return filepath.Join(c.DataDir, "installed.json") }

// IsInstalled 同时要求标记文件与关键密钥齐备，避免半安装状态。
func (c *Config) IsInstalled() bool {
	if _, err := os.Stat(c.InstalledMarker()); err != nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.AppKey != "" && c.FingerprintKey != ""
}

// Reload 只热更新安装向导会改动、且运行期会被并发读取的字段。
// 其余配置（Worker 数量、调度间隔等）在进程启动时已固化，改 .env 后需要重启才生效。
func (c *Config) Reload() {
	fresh, err := Load()
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.AppKey = fresh.AppKey
	c.FingerprintKey = fresh.FingerprintKey
	c.SessionSecure = fresh.SessionSecure
	c.CORSAllowedOrigins = fresh.CORSAllowedOrigins
}

// ApplyInstall 把安装向导刚刚写入的运行时配置同步到内存和进程环境。
// godotenv.Load 不会覆盖已有环境变量，安装重试时必须显式同步，否则
// 内存中的 APP_KEY 会和磁盘 .env 不一致。
func (c *Config) ApplyInstall(appKey, fingerprintKey string, sessionSecure bool, origins []string) {
	c.mu.Lock()
	c.AppKey = appKey
	c.FingerprintKey = fingerprintKey
	c.SessionSecure = sessionSecure
	c.CORSAllowedOrigins = origins
	c.mu.Unlock()
	_ = os.Setenv("APP_KEY", appKey)
	_ = os.Setenv("CREDENTIAL_FINGERPRINT_KEY", fingerprintKey)
	_ = os.Setenv("SESSION_SECURE", strconv.FormatBool(sessionSecure))
	_ = os.Setenv("CORS_ALLOWED_ORIGINS", strings.Join(origins, ","))
}

func (c *Config) SecretKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.AppKey
}

func (c *Config) FingerprintSecret() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.FingerprintKey
}

func (c *Config) SessionSecureEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.SessionSecure
}

func (c *Config) AllowedOrigins() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.CORSAllowedOrigins
}

func defaultDataDir() string {
	if v := os.Getenv("TF_DATA_DIR"); v != "" {
		return v
	}
	return "./data"
}

func defaultWorkers() int {
	n := 2
	if v := os.Getenv("GOMAXPROCS"); v != "" {
		if i, err := strconv.Atoi(v); err == nil && i > 0 {
			n = i / 2
		}
	}
	if n < 1 {
		n = 1
	}
	if n > 8 {
		n = 8
	}
	return n
}

func existingEnvFiles(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			out = append(out, path)
		}
	}
	return out
}

func envFileCandidates() []string {
	out := []string{".env"}
	if d, err := os.Executable(); err == nil {
		out = append(out, filepath.Join(filepath.Dir(d), ".env"))
	}
	return out
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok {
		if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return i
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return fallback
}

func splitCSV(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, strings.TrimRight(s, "/"))
		}
	}
	return out
}
