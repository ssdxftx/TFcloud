package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // 内嵌时区库，容器里无需额外安装 tzdata

	"tfcloud/internal/config"
	"tfcloud/internal/database"
	"tfcloud/internal/httpapi"
	"tfcloud/internal/httpapi/middleware"
	"tfcloud/internal/plugins"
	"tfcloud/internal/secure"
	"tfcloud/internal/service"
	"tfcloud/internal/sign"
	"tfcloud/internal/webui"
	"tfcloud/internal/worker"
)

const version = "2.0.0-go"

func main() {
	if len(os.Args) < 2 {
		runServe()
		return
	}
	switch os.Args[1] {
	case "serve", "start":
		runServe()
	case "migrate":
		runMigrate()
	case "version", "-v", "--version":
		fmt.Printf("tfcloud %s (go)\n", version)
	case "admin":
		runAdmin(os.Args[2:])
	case "doctor":
		runDoctor()
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "未知命令：%s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`天方云签 · Go 版

用法：
  tfcloud serve              启动 HTTP 服务与后台 Worker
  tfcloud migrate            仅执行数据库迁移
  tfcloud admin create       创建管理员账号
  tfcloud admin passwd       重置管理员密码
  tfcloud doctor             自检（配置、数据库、密钥）
  tfcloud version            查看版本
`)
}

func logger() *slog.Logger {
	level := slog.LevelInfo
	if os.Getenv("APP_DEBUG") == "true" {
		level = slog.LevelDebug
	}
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

type app struct {
	cfg       *config.Config
	store     *database.Store
	logger    *slog.Logger
	registry  *sign.Registry
	settings  *service.SettingsService
	auth      *service.AuthService
	mail      *service.MailService
	accounts  *service.AccountService
	tasks     *service.TaskService
	audit     *service.AuditService
	install   *service.InstallService
	platform  *service.PlatformService
	admin     *service.AdminService
	assistant *service.AssistantService
	live      *service.LiveService
}

func bootstrap() (*app, error) {
	log := logger()
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	middleware.SetTrustedProxies(cfg.TrustedProxyCIDRs)
	store, err := database.Open(cfg.Database)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Migrate(ctx); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}

	httpClient := sign.NewHTTP()
	registry := sign.NewRegistry(
		plugins.NewTieba(httpClient),
		plugins.NewBilibili(httpClient),
		plugins.NewNetease(httpClient),
		plugins.NewIqiyi(httpClient),
		plugins.NewSport(httpClient),
		plugins.NewCloud189(httpClient),
		plugins.NewPicacomic(httpClient),
	)
	settings := service.NewSettings(store, cfg)
	mail := service.NewMail(store, cfg, settings, log)
	auth := service.NewAuth(store, cfg, settings, mail)
	accounts := service.NewAccount(store, cfg, settings, registry)
	audit := service.NewAudit(store)
	tasks := service.NewTask(store, cfg, settings, accounts, registry, audit, mail, log)
	accounts.SetTaskService(tasks)
	install := service.NewInstall(store, cfg, settings)
	platform := service.NewPlatform(registry)
	admin := service.NewAdmin(store, cfg, settings, accounts, tasks, audit, registry, platform, mail)
	assistant := service.NewAssistant(store, cfg, settings)
	live := service.NewLive(store, accounts, tasks, log)

	return &app{cfg: cfg, store: store, logger: log, registry: registry, settings: settings,
		auth: auth, mail: mail, accounts: accounts, tasks: tasks, audit: audit,
		install: install, platform: platform, admin: admin, assistant: assistant, live: live}, nil
}

func runServe() {
	a, err := bootstrap()
	if err != nil {
		fmt.Fprintf(os.Stderr, "启动失败：%v\n", err)
		os.Exit(1)
	}
	defer a.store.Close()

	server := &httpapi.Server{
		Cfg: a.cfg, Store: a.store, Auth: a.auth, Settings: a.settings, Install: a.install,
		Accounts: a.accounts, Tasks: a.tasks, Admin: a.admin, Mail: a.mail,
		Assistant: a.assistant, Platform: a.platform, Audit: a.audit,
		Registry: a.registry, Logger: a.logger,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool := worker.NewPool(a.cfg, a.store, a.tasks, a.mail, a.live, a.logger)
	pool.Start(ctx)

	handler := server.Router(webui.Handler(a.cfg.StaticDist))
	httpServer := &http.Server{
		Addr:              a.cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	go func() {
		a.logger.Info("tfcloud started",
			slog.String("addr", a.cfg.HTTPAddr),
			slog.String("data", a.cfg.DataDir),
			slog.Bool("installed", a.cfg.IsInstalled()),
			slog.String("version", version))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.logger.Error("http server failed", slog.String("error", err.Error()))
			cancel()
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-stop:
		a.logger.Info("shutting down")
	case <-ctx.Done():
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	_ = httpServer.Shutdown(shutdownCtx)
	cancel()
	pool.Wait()
	_ = a.store.Checkpoint(context.Background())
}

func runMigrate() {
	a, err := bootstrap()
	if err != nil {
		fmt.Fprintf(os.Stderr, "迁移失败：%v\n", err)
		os.Exit(1)
	}
	defer a.store.Close()
	fmt.Printf("数据库已就绪：%s\n", a.cfg.Database)
}

func runDoctor() {
	a, err := bootstrap()
	if err != nil {
		fmt.Fprintf(os.Stderr, "自检失败：%v\n", err)
		os.Exit(1)
	}
	defer a.store.Close()
	fmt.Printf("版本        : %s\n", version)
	fmt.Printf("数据目录    : %s\n", a.cfg.DataDir)
	fmt.Printf("数据库      : %s\n", a.cfg.Database)
	fmt.Printf("已安装      : %v\n", a.cfg.IsInstalled())
	if _, err := secure.DecodeKey(a.cfg.AppKey); err != nil {
		fmt.Printf("APP_KEY     : 未配置或长度不正确（安装向导会生成）\n")
	} else {
		fmt.Printf("APP_KEY     : 正常（32 字节）\n")
	}
	if a.cfg.FingerprintKey == "" {
		fmt.Printf("指纹密钥    : 未配置\n")
	} else {
		fmt.Printf("指纹密钥    : 已配置\n")
	}
	var tables int
	_ = a.store.Read.Get(&tables, `SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name LIKE 'TF_%'`)
	fmt.Printf("数据表      : %d 张\n", tables)
	fmt.Printf("插件        : %d 个\n", len(a.registry.All()))
	for _, p := range a.registry.All() {
		fmt.Printf("  - %-10s %s v%s\n", p.Metadata().Code, p.Metadata().Name, p.Metadata().Version)
	}
}

func runAdmin(args []string) {
	fs := flag.NewFlagSet("admin", flag.ExitOnError)
	username := fs.String("username", "", "管理员用户名")
	password := fs.String("password", "", "管理员密码（至少 8 位）")
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	action := args[0]
	_ = fs.Parse(args[1:])
	a, err := bootstrap()
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败：%v\n", err)
		os.Exit(1)
	}
	defer a.store.Close()

	switch action {
	case "create":
		if *username == "" || len(*password) < 8 {
			fmt.Fprintln(os.Stderr, "用法：tfcloud admin create --username admin --password '至少8位密码'")
			os.Exit(2)
		}
		hash, err := secure.HashPassword(*password)
		if err != nil {
			fmt.Fprintf(os.Stderr, "密码加密失败：%v\n", err)
			os.Exit(1)
		}
		now := database.Now()
		_, err = a.store.Write.Exec(`INSERT INTO TF_users
			(username, username_norm, password_hash, display_name, role, status, daily_sign_email, push_channel,
			 password_migrated_at, created_at, updated_at)
			VALUES(?,?,?,?, 'admin', 'active', 0, 'none', ?,?,?)`,
			*username, service.NormalizeIdentity(*username), hash, *username, now, now, now)
		if err != nil {
			fmt.Fprintf(os.Stderr, "创建失败：%v\n", err)
			os.Exit(1)
		}
		fmt.Printf("管理员 %s 已创建\n", *username)
	case "passwd":
		if *username == "" || len(*password) < 8 {
			fmt.Fprintln(os.Stderr, "用法：tfcloud admin passwd --username admin --password '至少8位密码'")
			os.Exit(2)
		}
		hash, err := secure.HashPassword(*password)
		if err != nil {
			fmt.Fprintf(os.Stderr, "密码加密失败：%v\n", err)
			os.Exit(1)
		}
		res, err := a.store.Write.Exec(`UPDATE TF_users SET password_hash=?, password_migrated_at=?, updated_at=?
			WHERE username_norm=?`, hash, database.Now(), database.Now(), service.NormalizeIdentity(*username))
		if err != nil {
			fmt.Fprintf(os.Stderr, "更新失败：%v\n", err)
			os.Exit(1)
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			fmt.Fprintln(os.Stderr, "用户不存在")
			os.Exit(1)
		}
		_, _ = a.store.Write.Exec(`UPDATE TF_sessions SET revoked_at=? WHERE user_id IN
			(SELECT id FROM TF_users WHERE username_norm=?) AND revoked_at IS NULL`,
			database.Now(), service.NormalizeIdentity(*username))
		fmt.Println("密码已更新，相关会话已失效")
	default:
		usage()
		os.Exit(2)
	}
}
