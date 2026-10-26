package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"tfcloud/internal/httpapi/middleware"
	"tfcloud/internal/httpapi/respond"
	"tfcloud/internal/service"
)

// Router 组装全部 HTTP 路由。
// 分组与路径顺序严格对齐 PHP 版 config/route.php，前端无需任何改动。
func (s *Server) Router(static http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RealIP)
	r.Use(middleware.RequestID())
	r.Use(middleware.SecurityHeaders)
	r.Use(s.mw().Recover)
	r.Use(s.mw().CORSHandler)
	r.Use(s.mw().InstallGuard)
	r.Use(chimw.Timeout(180 * time.Second))

	// 健康检查
	r.Get("/health/live", s.healthLive)
	r.Get("/health/ready", s.healthReady)
	r.Get("/.well-known/qq-login-verification.txt", s.qqVerification)

	r.Route("/api", func(api chi.Router) {
		limiter := middleware.NewRateLimiter(30, time.Minute)
		sensitive := middleware.NewRateLimiter(10, time.Minute)

		// 安装向导
		installLimiter := middleware.NewRateLimiter(10, time.Minute)
		api.Get("/install/status", s.installStatus)
		api.With(installLimiter.Limit("install")).Get("/install/checks", s.installChecks)
		api.With(installLimiter.Limit("install")).Post("/install/test-database", s.installTestStorage)
		api.With(installLimiter.Limit("install")).Post("/install", s.installRun)

		// 认证
		api.Group(func(auth chi.Router) {
			auth.Use(limiter.Limit("auth"))
			auth.Post("/auth/human-challenge", s.issueChallenge)
			auth.Post("/auth/human-challenge/verify", s.verifyChallenge)
			auth.Post("/auth/qq/start", s.qqStart)
			auth.Get("/auth/qq/verification", s.qqVerification)
			auth.Get("/auth/qq/callback", s.qqCallback)
			auth.Get("/auth/qq/callback/relay/{state}", s.qqCallback)
			auth.Post("/auth/qq/exchange", s.qqExchange)
			auth.Post("/auth/qq/register", s.qqRegister)
		})
		api.Group(func(auth chi.Router) {
			auth.Use(sensitive.Limit("login"))
			auth.Post("/auth/login", s.login)
			auth.Post("/auth/login/email-code", s.loginEmailCodeIssue)
			auth.Post("/auth/login/email", s.loginWithEmailCode)
			auth.Post("/auth/register", s.register)
			auth.Post("/auth/register/email-code", s.registerEmailCodeIssue)
			auth.Post("/auth/password/forgot", s.forgotPassword)
			auth.Post("/auth/password/reset", s.resetPassword)
		})
		api.Get("/site", s.siteShow)

		// 需要登录
		api.Group(func(priv chi.Router) {
			priv.Use(s.mw().AuthRequired)
			priv.Post("/auth/logout", s.logout)
			priv.Get("/me", s.me)
			priv.Patch("/me", s.updateMe)
			priv.Post("/me/email/code", s.changeEmailCode)
			priv.Post("/me/password/code", s.passwordCode)
			priv.Post("/me/password", s.changePassword)
			priv.Post("/me/qq/start", s.qqBindStart)
			priv.Post("/me/push/test", s.testPush)

			priv.Get("/platforms", s.platforms)
			priv.Get("/platforms/{platformCode}", s.platformDetail)
			priv.Post("/platform-auth/{platformCode}/qr/start", s.platformQRStart)
			priv.Get("/platform-auth/{platformCode}/qr/{flowNo}", s.platformQRPoll)
			priv.Post("/platform-auth/{platformCode}/password", s.platformPassword)
			priv.Post("/platform-auth/{platformCode}/sms/send", s.platformSMSSend)
			priv.Post("/platform-auth/{platformCode}/sms/complete", s.platformSMSComplete)

			priv.Get("/plugin-accounts", s.accountsIndex)
			priv.Post("/plugin-accounts", s.accountsStore)
			priv.Get("/plugin-accounts/{id:[0-9]+}", s.accountsShow)
			priv.Patch("/plugin-accounts/{id:[0-9]+}", s.accountsUpdate)
			priv.Delete("/plugin-accounts/{id:[0-9]+}", s.accountsDestroy)
			priv.Post("/plugin-accounts/{id:[0-9]+}/actions/{action}", s.accountsAction)

			priv.Get("/sign-tasks", s.tasksIndex)
			priv.Post("/sign-tasks", s.tasksStore)
			priv.Get("/sign-tasks/calendar", s.tasksCalendar)
			priv.Get("/sign-tasks/{taskNo}", s.tasksShow)
			priv.Get("/sign-tasks/{taskNo}/runs", s.tasksRuns)
			priv.Get("/sign-tasks/{taskNo}/records", s.tasksRecords)
			priv.Post("/sign-tasks/{taskNo}/retry", s.tasksRetry)
			priv.Post("/sign-tasks/{taskNo}/cancel", s.tasksCancel)

			priv.Get("/assistant/status", s.assistantStatus)
			priv.Get("/assistant/messages", s.assistantMessages)
			priv.Post("/assistant/messages", s.assistantSend)
			priv.Post("/assistant/messages/stream", s.assistantStream)
			priv.Delete("/assistant/messages", s.assistantReset)

			// 管理员
			priv.Group(func(admin chi.Router) {
				admin.Use(s.mw().AdminRequired)

				admin.Get("/plugins", s.plugins)
				admin.Get("/plugins/{pluginCode}", s.pluginDetail)
				admin.Get("/plugins/{pluginCode}/actions", s.pluginActions)

				admin.Get("/admin/overview", s.adminOverview)
				admin.Get("/admin/system/version", s.adminVersion)
				admin.Get("/admin/settings", s.adminSettings)
				admin.Get("/admin/settings/{group}", s.adminSettingsGroup)
				admin.Patch("/admin/settings/{group}", s.adminSettingsUpdate)
				admin.Post("/admin/settings/qq-relay/apply", s.adminQqRelayApply)
				admin.Post("/admin/settings/qq-relay/verify", s.adminQqRelayVerify)
				admin.Post("/admin/settings/qq-relay/configure", s.adminQqRelayConfigure)

				admin.Get("/admin/mail/summary", s.adminMailSummary)
				admin.Get("/admin/mail/tasks", s.adminMailTasks)
				admin.Get("/admin/mail/tasks/{taskNo}/records", s.adminMailTaskRecords)
				admin.Get("/admin/mail/records", s.adminMailRecords)
				admin.Delete("/admin/mail/records/{id:[0-9]+}", s.adminMailRecordDelete)
				admin.Post("/admin/mail/tasks/{taskNo}/retry", s.adminMailRetry)
				admin.Post("/admin/mail/compose", s.adminMailCompose)
				admin.Post("/admin/mail/test", s.adminMailTest)

				admin.Get("/admin/sign-tasks", s.adminTasks)
				admin.Get("/admin/sign-tasks/{taskNo}", s.adminTaskShow)
				admin.Get("/admin/sign-tasks/{taskNo}/runs", s.adminTaskRuns)
				admin.Get("/admin/sign-tasks/{taskNo}/records", s.adminTaskRecords)
				admin.Post("/admin/sign-tasks/{taskNo}/retry", s.adminTaskRetry)
				admin.Post("/admin/sign-tasks/{taskNo}/cancel", s.adminTaskCancel)

				admin.Get("/admin/audit-logs", s.adminAuditLogs)

				admin.Get("/admin/users", s.adminUsers)
				admin.Post("/admin/users", s.adminUserCreate)
				admin.Get("/admin/users/{id:[0-9]+}", s.adminUserShow)
				admin.Patch("/admin/users/{id:[0-9]+}", s.adminUserUpdate)
				admin.Delete("/admin/users/{id:[0-9]+}", s.adminUserDelete)
				admin.Post("/admin/users/{id:[0-9]+}/reset-password", s.adminUserResetPassword)
				admin.Post("/admin/users/{id:[0-9]+}/revoke-sessions", s.adminUserRevokeSessions)

				admin.Get("/admin/plugin-accounts", s.adminAccounts)
				admin.Patch("/admin/plugin-accounts/{id:[0-9]+}", s.adminAccountUpdate)
				admin.Delete("/admin/plugin-accounts/{id:[0-9]+}", s.adminAccountDelete)
				admin.Post("/admin/plugin-accounts/{id:[0-9]+}/actions/{action}", s.adminAccountAction)

				admin.Post("/admin/astrbot/test", s.adminAssistantTest)
			})
		})

		api.NotFound(func(w http.ResponseWriter, r *http.Request) {
			respond.Fail(w, r, respond.NewError("ROUTE_NOT_FOUND", "接口不存在", 404))
		})
	})

	// 前端静态资源与 SPA 回退
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		if static != nil {
			static.ServeHTTP(w, r)
			return
		}
		respond.Fail(w, r, respond.NewError("ROUTE_NOT_FOUND", "资源不存在", 404))
	})
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		if static != nil {
			static.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})

	return r
}

func (s *Server) mw() *middleware.Middleware {
	return &middleware.Middleware{
		Auth:      s.Auth,
		Installed: s.Cfg.IsInstalled,
		CORS:      func() []string { return s.Cfg.CORSAllowedOrigins },
		Logger:    s.Logger,
	}
}

var _ = service.QueueDefault
