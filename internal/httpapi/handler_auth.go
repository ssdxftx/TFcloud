package httpapi

import (
	"net/http"
	"strings"

	"tfcloud/internal/httpapi/middleware"
	"tfcloud/internal/httpapi/respond"
	"tfcloud/internal/service"
)

// ---------- 健康检查 ----------

func (s *Server) healthLive(w http.ResponseWriter, r *http.Request) {
	respond.OK(w, r, map[string]any{"status": "ok", "runtime": "go"})
}

func (s *Server) healthReady(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Write.Ping(); err != nil {
		respond.JSON(w, http.StatusServiceUnavailable, respond.Envelope{
			Code: "NOT_READY", Message: "数据库不可用", RequestID: respond.RequestID(r.Context()),
		})
		return
	}
	respond.OK(w, r, map[string]any{"status": "ready", "installed": s.Cfg.IsInstalled()})
}

// qqVerification 返回域名校验文件内容，未配置时返回 404。
func (s *Server) qqVerification(w http.ResponseWriter, r *http.Request) {
	group := s.Settings.Group("site")
	content := strings.TrimSpace(strValue(group["qq_relay_verification_content"]))
	if content == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(content))
}

// ---------- 安装 ----------

func (s *Server) installStatus(w http.ResponseWriter, r *http.Request) {
	respond.OK(w, r, s.Install.Status())
}

func (s *Server) installChecks(w http.ResponseWriter, r *http.Request) {
	respond.OK(w, r, s.Install.Checks())
}

func (s *Server) installTestStorage(w http.ResponseWriter, r *http.Request) {
	respond.OK(w, r, s.Install.TestStorage())
}

func (s *Server) installRun(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	input := service.InstallInput{SiteURL: strOf(payload["site_url"]), SessionSecure: boolOf(payload["session_secure"])}
	if admin, ok := payload["admin"].(map[string]any); ok {
		input.Admin.Username = strOf(admin["username"])
		input.Admin.Password = strOf(admin["password"])
		input.Admin.DisplayName = strOf(admin["display_name"])
	}
	result, err := s.Install.Run(input)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, result)
}

// ---------- 站点信息 ----------

func (s *Server) siteShow(w http.ResponseWriter, r *http.Request) {
	respond.OK(w, r, s.Settings.PublicSite())
}

// ---------- 人机验证 ----------

func (s *Server) issueChallenge(w http.ResponseWriter, r *http.Request) {
	payload, _ := body(r)
	purpose := strOf(payload["purpose"])
	if purpose == "" {
		purpose = "login"
	}
	out, err := s.Auth.IssueChallenge(purpose, middleware.ClientIP(r), r.UserAgent())
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) verifyChallenge(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Auth.VerifyChallenge(strOf(payload["challenge_id"]), intOf(payload["elapsed_ms"]),
		intOf(payload["interaction_count"]), middleware.ClientIP(r), r.UserAgent())
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

// ---------- 注册 / 登录 ----------

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	if err := s.consumeRegistrationChallenge(r, payload); err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Auth.Register(service.RegisterInput{
		Username:  strOf(payload["username"]),
		Password:  strOf(payload["password"]),
		Email:     strOf(payload["email"]),
		EmailCode: strOf(payload["email_code"]),
	}, middleware.ClientIP(r), r.UserAgent())
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.setSessionCookie(w, strOf(out["access_token"]))
	respond.Created(w, r, out)
}

func (s *Server) consumeRegistrationChallenge(r *http.Request, payload map[string]any) error {
	site := s.Settings.Group("site")
	if !truthyValue(site["registration_challenge_enabled"]) {
		return nil
	}
	id := strOf(payload["challenge_id"])
	if id == "" {
		return respond.NewError("HUMAN_VERIFICATION_REQUIRED", "请先完成人机验证", 422)
	}
	return s.Auth.ConsumeChallenge(id, "register", middleware.ClientIP(r), r.UserAgent())
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	site := s.Settings.Group("site")
	if truthyValue(site["login_challenge_enabled"]) {
		id := strOf(payload["challenge_id"])
		if id == "" {
			respond.Fail(w, r, respond.NewError("HUMAN_VERIFICATION_REQUIRED", "请先完成人机验证", 422))
			return
		}
		if err := s.Auth.ConsumeChallenge(id, "login", middleware.ClientIP(r), r.UserAgent()); err != nil {
			respond.Fail(w, r, err)
			return
		}
	}
	account := firstNonEmptyStr(strOf(payload["account"]), strOf(payload["username"]), strOf(payload["email"]))
	out, err := s.Auth.Login(account, strOf(payload["password"]), middleware.ClientIP(r), r.UserAgent())
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.setSessionCookie(w, strOf(out["access_token"]))
	respond.OK(w, r, out)
}

func (s *Server) loginEmailCodeIssue(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Auth.IssueEmailCode(strOf(payload["email"]), middleware.ClientIP(r), "login")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) loginWithEmailCode(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Auth.LoginWithEmailCode(strOf(payload["email"]), strOf(payload["code"]),
		middleware.ClientIP(r), r.UserAgent())
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.setSessionCookie(w, strOf(out["access_token"]))
	respond.OK(w, r, out)
}

func (s *Server) registerEmailCodeIssue(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Auth.IssueEmailCode(strOf(payload["email"]), middleware.ClientIP(r), "register")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) forgotPassword(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Auth.IssuePasswordReset(strOf(payload["email"]))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	if err := s.Auth.ResetPassword(strOf(payload["email"]), strOf(payload["code"]), strOf(payload["password"])); err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Message(w, r, map[string]any{"reset": true}, "密码已重置，请使用新密码登录")
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Auth.Logout(middleware.TokenFromContext(r.Context()))
	s.clearSessionCookie(w)
	respond.Message(w, r, map[string]any{"logout": true}, "已退出登录")
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	out, err := s.Auth.PublicUser(userID(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Auth.UpdateProfile(userID(r), payload)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Message(w, r, out, "资料已更新")
}

func (s *Server) changeEmailCode(w http.ResponseWriter, r *http.Request) {
	out, err := s.Auth.IssueChangeEmailCode(userID(r), middleware.ClientIP(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) passwordCode(w http.ResponseWriter, r *http.Request) {
	out, err := s.Auth.IssuePasswordCode(userID(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	if err := s.Auth.ChangePassword(userID(r), strOf(payload["code"]), strOf(payload["password"])); err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.clearSessionCookie(w)
	respond.Message(w, r, map[string]any{"changed": true}, "密码已修改，请重新登录")
}

func (s *Server) testPush(w http.ResponseWriter, r *http.Request) {
	me, err := s.Auth.PublicUser(userID(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	channel := strOf(me["push_channel"])
	if channel == "none" || channel == "" {
		respond.Fail(w, r, respond.NewError("PUSH_NOT_CONFIGURED", "尚未配置推送渠道", 422))
		return
	}
	respond.Message(w, r, map[string]any{"channel": channel, "delivered": false},
		"推送渠道已保存；本版本仅支持邮件通知，站内推送将在后续版本接入")
}

// ---------- QQ 快捷登录 ----------

func (s *Server) qqStart(w http.ResponseWriter, r *http.Request) {
	site := s.Settings.Group("site")
	if !truthyValue(site["qq_login_enabled"]) {
		respond.Fail(w, r, respond.NewError("QQ_LOGIN_DISABLED", "站点未开启 QQ 登录", 409))
		return
	}
	respond.Fail(w, r, respond.NewError("QQ_LOGIN_UNAVAILABLE",
		"QQ 快捷登录依赖中转服务，本版本尚未移植，请使用账号密码或邮箱验证码登录", 501))
}

func (s *Server) qqBindStart(w http.ResponseWriter, r *http.Request) {
	respond.Fail(w, r, respond.NewError("QQ_LOGIN_UNAVAILABLE",
		"QQ 绑定依赖中转服务，本版本尚未移植", 501))
}

func (s *Server) qqCallback(w http.ResponseWriter, r *http.Request) {
	respond.Fail(w, r, respond.NewError("QQ_LOGIN_UNAVAILABLE", "QQ 快捷登录尚未启用", 501))
}

func (s *Server) qqExchange(w http.ResponseWriter, r *http.Request) {
	respond.Fail(w, r, respond.NewError("QQ_LOGIN_UNAVAILABLE", "QQ 快捷登录尚未启用", 501))
}

func (s *Server) qqRegister(w http.ResponseWriter, r *http.Request) {
	respond.Fail(w, r, respond.NewError("QQ_LOGIN_UNAVAILABLE", "QQ 快捷登录尚未启用", 501))
}

// ---------- Cookie ----------

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	if token == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.Cfg.SessionSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   14 * 24 * 60 * 60,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.Cfg.SessionSecure,
		MaxAge:   -1,
	})
}

// ---------- 小工具 ----------

func strOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func boolOf(v any) bool {
	b, _ := v.(bool)
	return b
}

func intOf(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		var n int
		for _, r := range t {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		return n
	}
	return 0
}

func strValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func truthyValue(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "true", "yes", "on":
			return true
		}
	case float64:
		return t != 0
	case int:
		return t != 0
	}
	return false
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
