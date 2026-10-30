package service

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"tfcloud/internal/config"
	"tfcloud/internal/database"
	"tfcloud/internal/httpapi/middleware"
	"tfcloud/internal/httpapi/respond"
	"tfcloud/internal/model"
	"tfcloud/internal/secure"
)

const (
	codeTTL         = 600 * time.Second
	codeResend      = 60 * time.Second
	codeMaxAttempts = 5
	codeHourlyEmail = 5
	codeHourlyIP    = 10
	sessionTTL      = 14 * 24 * time.Hour
)

type AuthService struct {
	store     *database.Store
	cfg       *config.Config
	settings  *SettingsService
	mail      *MailService
	challenge *challengeStore
}

func NewAuth(store *database.Store, cfg *config.Config, settings *SettingsService, mail *MailService) *AuthService {
	return &AuthService{store: store, cfg: cfg, settings: settings, mail: mail, challenge: newChallengeStore(store)}
}

// ---------- 归一化 ----------

// NormalizeIdentity 复刻 MySQL utf8mb4_0900_ai_ci 的唯一性语义：
// 忽略大小写与重音，用于 username_norm / email_norm。
func NormalizeIdentity(v string) string {
	v = strings.TrimSpace(v)
	v = norm.NFKD.String(v)
	var b strings.Builder
	for _, r := range v {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return norm.NFC.String(b.String())
}

func maskEmail(email string) string {
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return email
	}
	name, domain := parts[0], parts[1]
	runes := []rune(name)
	visible := runes
	if len(visible) > 2 {
		visible = visible[:2]
	}
	stars := len(runes) - len(visible)
	if stars < 2 {
		stars = 2
	}
	if stars > 6 {
		stars = 6
	}
	return string(visible) + strings.Repeat("*", stars) + "@" + domain
}

func (s *AuthService) hmacKey() ([]byte, error) {
	return secure.DecodeKey(s.cfg.SecretKey())
}

func (s *AuthService) hmacHex(value string) (string, error) {
	key, err := s.hmacKey()
	if err != nil {
		return "", respond.NewError("APP_KEY_MISSING", "应用密钥未配置", 500)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func randomCode() string {
	n, err := rand.Int(rand.Reader, big.NewInt(900000))
	if err != nil {
		panic("系统随机源不可用: " + err.Error())
	}
	return fmt.Sprintf("%06d", 100000+n.Int64())
}

// ---------- 认证 ----------

func (s *AuthService) Authenticate(rawToken, ip string) (*middleware.Principal, error) {
	if rawToken == "" {
		return nil, respond.Unauthorized("请先登录")
	}
	var row struct {
		SessionID  int64   `db:"sid"`
		UserID     int64   `db:"user_id"`
		Role       string  `db:"role"`
		Username   string  `db:"username"`
		Status     string  `db:"status"`
		ExpiresAt  string  `db:"expires_at"`
		RevokedAt  *string `db:"revoked_at"`
		LastSeenAt *string `db:"last_seen_at"`
	}
	err := s.store.Read.Get(&row, `SELECT s.id AS sid, s.user_id, s.expires_at, s.revoked_at, s.last_seen_at,
			u.role, u.username, u.status
		FROM TF_sessions s JOIN TF_users u ON u.id = s.user_id
		WHERE s.token_hash = ?`, secure.SHA256Hex(rawToken))
	if err != nil {
		return nil, respond.Unauthorized("登录已失效，请重新登录")
	}
	if row.RevokedAt != nil {
		return nil, respond.Unauthorized("登录已失效，请重新登录")
	}
	expires, ok := database.Parse(row.ExpiresAt)
	if !ok || time.Now().After(expires) {
		return nil, respond.Unauthorized("登录已过期，请重新登录")
	}
	if row.Status != "active" {
		return nil, respond.NewError("AUTH_ACCOUNT_DISABLED", "账号已停用", 403)
	}
	// 节流写：每 5 分钟才更新一次 last_seen_at，避免把单写连接打满。
	if row.LastSeenAt == nil || time.Since(mustParse(row.LastSeenAt)) > 5*time.Minute {
		_, _ = s.store.Write.Exec(`UPDATE TF_sessions SET last_seen_at = ? WHERE id = ? AND (last_seen_at IS NULL OR last_seen_at < ?)`,
			database.Now(), row.SessionID, database.Ts(time.Now().Add(-5*time.Minute)))
	}
	return &middleware.Principal{UserID: row.UserID, Role: row.Role, Username: row.Username, Status: row.Status}, nil
}

func mustParse(v *string) time.Time {
	if v == nil {
		return time.Time{}
	}
	if t, ok := database.Parse(*v); ok {
		return t
	}
	return time.Time{}
}

type RegisterInput struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	Email     string `json:"email"`
	EmailCode string `json:"email_code"`
}

func (s *AuthService) Register(in RegisterInput, ip, ua string) (map[string]any, error) {
	username := strings.TrimSpace(in.Username)
	password := in.Password
	email := strings.ToLower(strings.TrimSpace(in.Email))

	if n := utf8.RuneCountInString(username); n < 2 || n > 64 {
		return nil, respond.NewError("VALIDATION_FAILED", "用户名长度必须为2到64个字符", 422)
	}
	if len(password) < 8 || len(password) > 1024 {
		return nil, respond.NewError("VALIDATION_FAILED", "密码长度必须为8到1024个字符", 422)
	}
	if !validEmail(email) || len(email) > 191 {
		return nil, respond.NewError("VALIDATION_FAILED", "请输入有效的邮箱地址", 422)
	}

	site := s.settings.Group("site")
	if !truthy(site["registration_enabled"]) {
		return nil, respond.NewError("REGISTRATION_DISABLED", "站点已关闭注册", 403)
	}

	usernameNorm := NormalizeIdentity(username)
	emailNorm := NormalizeIdentity(email)
	var exists int
	_ = s.store.Read.Get(&exists, `SELECT COUNT(1) FROM TF_users WHERE username_norm = ?`, usernameNorm)
	if exists > 0 {
		return nil, respond.NewError("USER_FIELD_DUPLICATE", "用户名已存在", 409)
	}
	_ = s.store.Read.Get(&exists, `SELECT COUNT(1) FROM TF_users WHERE email_norm = ?`, emailNorm)
	if exists > 0 {
		return nil, respond.NewError("USER_FIELD_DUPLICATE", "邮箱已被使用", 409)
	}

	if truthy(site["registration_email_verification"]) {
		if !truthy(s.settings.Group("mail")["enabled"]) {
			return nil, respond.NewError("REGISTRATION_EMAIL_VERIFICATION_UNAVAILABLE", "注册邮箱验证已开启，但邮件服务未配置", 409)
		}
		if err := s.ConsumeEmailCode(email, strings.TrimSpace(in.EmailCode), "register"); err != nil {
			return nil, err
		}
	}

	hash, err := secure.HashPassword(password)
	if err != nil {
		return nil, respond.Internal("密码加密失败")
	}
	now := database.Now()
	res, err := s.store.Write.Exec(`INSERT INTO TF_users
		(username, username_norm, password_hash, display_name, email, email_norm, role, status,
		 daily_sign_email, push_channel, password_migrated_at, created_at, updated_at)
		VALUES(?,?,?,'',?,?,'user','active',0,'none',?,?,?)`,
		username, usernameNorm, hash, email, emailNorm, now, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, respond.NewError("USER_FIELD_DUPLICATE", "用户名或邮箱已被使用", 409)
		}
		return nil, respond.Internal("注册失败")
	}
	userID, _ := res.LastInsertId()
	return s.issueSession(userID, ip, ua, now)
}

func (s *AuthService) Login(account, password, ip, ua string) (map[string]any, error) {
	account = strings.TrimSpace(account)
	loginKey := NormalizeIdentity(account)
	if !s.loginAllowed(loginKey) {
		return nil, respond.NewError("AUTH_ACCOUNT_LOCKED", "登录尝试过多，请 15 分钟后再试", 429)
	}
	var user model.User
	var err error
	if validEmail(account) {
		err = s.store.Read.Get(&user, `SELECT * FROM TF_users WHERE email_norm = ?`, NormalizeIdentity(account))
	} else {
		err = s.store.Read.Get(&user, `SELECT * FROM TF_users WHERE username_norm = ?`, NormalizeIdentity(account))
	}
	if err != nil || user.Status != "active" || !s.verifyPassword(password, user.PasswordHash) {
		s.recordLoginFailure(loginKey)
		return nil, respond.NewError("AUTH_INVALID_CREDENTIALS", "账号或密码错误", 401)
	}
	s.clearLoginFailures(loginKey)
	now := database.Now()
	stored := ""
	if user.PasswordHash != nil {
		stored = *user.PasswordHash
	}
	if secure.NeedsRehash(stored) {
		if hash, err := secure.HashPassword(password); err == nil {
			_, _ = s.store.Write.Exec(`UPDATE TF_users SET password_hash=?, password_migrated_at=?, updated_at=? WHERE id=?`,
				hash, now, now, user.ID)
		}
	}
	return s.issueSession(user.ID, ip, ua, now)
}

const (
	loginFailureWindow = 15 * time.Minute
	loginFailureLimit  = 5
)

func (s *AuthService) loginAllowed(key string) bool {
	var row struct {
		Failures        int     `db:"failures"`
		WindowStartedAt string  `db:"window_started_at"`
		LockedUntil     *string `db:"locked_until"`
	}
	if err := s.store.Read.Get(&row, `SELECT failures, window_started_at, locked_until
		FROM TF_login_failures WHERE login_key = ?`, key); err != nil {
		return true
	}
	now := time.Now()
	if row.LockedUntil != nil {
		if until, ok := database.Parse(*row.LockedUntil); ok && now.Before(until) {
			return false
		}
	}
	window, ok := database.Parse(row.WindowStartedAt)
	if !ok || now.Sub(window) > loginFailureWindow {
		_, _ = s.store.Write.Exec(`DELETE FROM TF_login_failures WHERE login_key = ?`, key)
		return true
	}
	return row.Failures < loginFailureLimit
}

func (s *AuthService) recordLoginFailure(key string) {
	tx, err := s.store.Write.Beginx()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	var row struct {
		Failures        int     `db:"failures"`
		WindowStartedAt string  `db:"window_started_at"`
		LockedUntil     *string `db:"locked_until"`
	}
	now := time.Now()
	failures := 1
	windowStarted := now
	if err := tx.Get(&row, `SELECT failures, window_started_at, locked_until
		FROM TF_login_failures WHERE login_key = ?`, key); err == nil {
		if started, ok := database.Parse(row.WindowStartedAt); ok && now.Sub(started) <= loginFailureWindow {
			failures = row.Failures + 1
			windowStarted = started
		}
	}
	var locked any
	if failures >= loginFailureLimit {
		locked = database.Ts(now.Add(loginFailureWindow))
	}
	_, err = tx.Exec(`INSERT INTO TF_login_failures(login_key, failures, window_started_at, locked_until, updated_at)
		VALUES(?,?,?,?,?) ON CONFLICT(login_key) DO UPDATE SET
		failures=excluded.failures, window_started_at=excluded.window_started_at,
		locked_until=excluded.locked_until, updated_at=excluded.updated_at`,
		key, failures, database.Ts(windowStarted), locked, database.Now())
	if err != nil {
		return
	}
	_ = tx.Commit()
}

func (s *AuthService) clearLoginFailures(key string) {
	_, _ = s.store.Write.Exec(`DELETE FROM TF_login_failures WHERE login_key = ?`, key)
}

func (s *AuthService) verifyPassword(password string, stored *string) bool {
	if stored == nil || *stored == "" {
		return false
	}
	if secure.VerifyPassword(*stored, password) {
		return true
	}
	// 兼容 md5(md5(pass) + md5(salt)) 的极早期哈希
	if !strings.HasPrefix(*stored, "$") && s.cfg.LegacyPasswordSalt != "" {
		sum := md5.Sum([]byte(password))
		salt := md5.Sum([]byte(s.cfg.LegacyPasswordSalt))
		legacy := md5.Sum([]byte(hex.EncodeToString(sum[:]) + hex.EncodeToString(salt[:])))
		return hmac.Equal([]byte(*stored), []byte(hex.EncodeToString(legacy[:])))
	}
	return false
}

func (s *AuthService) LoginWithEmailCode(email, code, ip, ua string) (map[string]any, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !validEmail(email) || len(email) > 191 {
		return nil, respond.NewError("VALIDATION_FAILED", "请输入有效的登录邮箱", 422)
	}
	var user model.User
	if err := s.store.Read.Get(&user, `SELECT * FROM TF_users WHERE email_norm = ?`, NormalizeIdentity(email)); err != nil {
		return nil, respond.NewError("AUTH_INVALID_EMAIL_CODE", "邮箱或验证码错误", 401)
	}
	if user.Status != "active" {
		return nil, respond.NewError("AUTH_INVALID_EMAIL_CODE", "邮箱或验证码错误", 401)
	}
	if err := s.ConsumeEmailCode(email, code, "login"); err != nil {
		if apiErr, ok := err.(*respond.APIError); ok && strings.HasPrefix(apiErr.Code, "VERIFICATION_CODE_") {
			return nil, respond.NewError("AUTH_INVALID_EMAIL_CODE", "邮箱或验证码错误", 401)
		}
		return nil, err
	}
	return s.issueSession(user.ID, ip, ua, database.Now())
}

func (s *AuthService) LoginExternal(userID int64, ip, ua string) (map[string]any, error) {
	var user model.User
	if err := s.store.Read.Get(&user, `SELECT * FROM TF_users WHERE id = ?`, userID); err != nil {
		return nil, respond.NotFound("用户不存在")
	}
	if user.Status != "active" {
		return nil, respond.NewError("AUTH_ACCOUNT_DISABLED", "账号已停用，无法使用快捷登录", 403)
	}
	return s.issueSession(userID, ip, ua, database.Now())
}

func (s *AuthService) Logout(token string) {
	if token == "" {
		return
	}
	_, _ = s.store.Write.Exec(`UPDATE TF_sessions SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`,
		database.Now(), secure.SHA256Hex(token))
}

func (s *AuthService) RevokeUserSessions(userID int64) (int64, error) {
	res, err := s.store.Write.Exec(`UPDATE TF_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`,
		database.Now(), userID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *AuthService) issueSession(userID int64, ip, ua, now string) (map[string]any, error) {
	plain := secure.RandomHex(32)
	ttl := s.cfg.SessionTTL
	if ttl <= 0 {
		ttl = sessionTTL
	}
	expires := database.Ts(time.Now().Add(ttl))
	if ip == "" {
		ip = "-"
	}
	if len(ua) > 500 {
		ua = ua[:500]
	}
	if len(ip) > 64 {
		ip = ip[:64]
	}
	if _, err := s.store.Write.Exec(`INSERT INTO TF_sessions
		(user_id, token_hash, ip_address, user_agent, last_seen_at, expires_at, created_at)
		VALUES(?,?,?,?,?,?,?)`,
		userID, secure.SHA256Hex(plain), ip, ua, now, expires, now); err != nil {
		return nil, respond.Internal("创建会话失败")
	}
	if _, err := s.store.Write.Exec(`UPDATE TF_users SET last_login_at=?, last_login_ip=?, updated_at=? WHERE id=?`,
		now, ip, now, userID); err != nil {
		return nil, respond.Internal("更新登录状态失败")
	}
	user, err := s.PublicUser(userID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"access_token": plain,
		"token_type":   "Bearer",
		"expires_at":   expires,
		"user":         user,
	}, nil
}

func (s *AuthService) PublicUser(userID int64) (map[string]any, error) {
	var user model.User
	if err := s.store.Read.Get(&user, `SELECT * FROM TF_users WHERE id = ?`, userID); err != nil {
		return nil, respond.NotFound("用户不存在")
	}
	var identity struct {
		Provider    string  `db:"provider"`
		DisplayName string  `db:"display_name"`
		ProfileJSON *string `db:"profile_json"`
		LinkedAt    string  `db:"linked_at"`
	}
	_ = s.store.Read.Get(&identity, `SELECT provider, display_name, profile_json, linked_at
		FROM TF_user_identities WHERE user_id = ? AND provider IN ('qq','qq_relay') ORDER BY id DESC LIMIT 1`, userID)

	qqBound := identity.Provider != ""
	site := s.settings.Group("site")
	requiresRebind := identity.Provider == "qq_relay" &&
		str(site["qq_relay_application_status"]) == "active" &&
		(identity.ProfileJSON == nil || !strings.Contains(*identity.ProfileJSON, "relay_application"))

	return map[string]any{
		"id":               user.ID,
		"username":         user.Username,
		"display_name":     user.DisplayName,
		"email":            user.Email,
		"qq":               user.QQ,
		"role":             user.Role,
		"status":           user.Status,
		"daily_sign_email": user.DailySignEmail == 1,
		"push_channel":     firstNonEmpty(user.PushChannel, "none"),
		"push_token":       deref(user.PushToken),
		"has_password":     user.HasPassword(),
		"qq_binding": map[string]any{
			"bound":           qqBound,
			"requires_rebind": requiresRebind,
			"provider":        nilIfEmpty(identity.Provider),
			"display_name":    nilIfEmpty(identity.DisplayName),
			"linked_at":       nilIfEmpty(identity.LinkedAt),
		},
		"last_login_at": user.LastLoginAt,
		"created_at":    user.CreatedAt,
	}, nil
}

func (s *AuthService) UpdateProfile(userID int64, input map[string]any, currentToken string) (map[string]any, error) {
	var user model.User
	if err := s.store.Read.Get(&user, `SELECT * FROM TF_users WHERE id = ?`, userID); err != nil {
		return nil, respond.NotFound("用户不存在")
	}
	now := database.Now()
	sets := []string{"updated_at = ?"}
	args := []any{now}
	currentEmail := strings.ToLower(deref(user.Email))

	if raw, ok := input["email"]; ok {
		value := strings.ToLower(strings.TrimSpace(str(raw)))
		if value != "" && !validEmail(value) {
			return nil, respond.NewError("VALIDATION_FAILED", "邮箱格式不正确", 422)
		}
		if len(value) > 191 {
			return nil, respond.NewError("VALIDATION_FAILED", "邮箱过长", 422)
		}
		if value != currentEmail {
			mailEnabled := truthy(s.settings.Group("mail")["enabled"])
			switch {
			case currentEmail != "" && mailEnabled:
				code := strings.TrimSpace(str(input["current_email_code"]))
				if err := s.ConsumeEmailCode(currentEmail, code, "change_email"); err != nil {
					return nil, err
				}
			default:
				// 首次绑定邮箱或邮件服务不可用时，用当前密码证明身份。
				password := str(input["current_password"])
				if !s.verifyPassword(password, user.PasswordHash) {
					if user.PasswordHash == nil || *user.PasswordHash == "" {
						return nil, respond.NewError("EMAIL_BINDING_REQUIRES_ADMIN", "当前账号尚未设置密码，请联系管理员绑定邮箱", 422)
					}
					return nil, respond.NewError("CURRENT_PASSWORD_INVALID", "当前密码不正确", 422)
				}
			}
		}
		if value != "" {
			var exists int
			_ = s.store.Read.Get(&exists, `SELECT COUNT(1) FROM TF_users WHERE email_norm = ? AND id <> ?`,
				NormalizeIdentity(value), userID)
			if exists > 0 {
				return nil, respond.NewError("USER_FIELD_DUPLICATE", "邮箱已被使用", 409)
			}
		}
		sets = append(sets, "email = ?", "email_norm = ?")
		args = append(args, nilIfEmpty(value), nilIfEmpty(NormalizeIdentity(value)))
	}

	if raw, ok := input["qq"]; ok {
		value := strings.TrimSpace(str(raw))
		if len(value) > 32 {
			return nil, respond.NewError("VALIDATION_FAILED", "QQ号过长", 422)
		}
		if value != "" {
			var exists int
			_ = s.store.Read.Get(&exists, `SELECT COUNT(1) FROM TF_users WHERE qq = ? AND id <> ?`, value, userID)
			if exists > 0 {
				return nil, respond.NewError("USER_FIELD_DUPLICATE", "QQ号已被使用", 409)
			}
		}
		sets = append(sets, "qq = ?")
		args = append(args, nilIfEmpty(value))
	}

	if raw, ok := input["daily_sign_email"]; ok {
		value := truthy(raw)
		email := currentEmail
		if v, exists := input["email"]; exists {
			email = strings.ToLower(strings.TrimSpace(str(v)))
		}
		if value && email == "" {
			return nil, respond.NewError("EMAIL_REQUIRED", "开启每日签到邮件前请先设置邮箱", 422)
		}
		flag := 0
		if value {
			flag = 1
		}
		sets = append(sets, "daily_sign_email = ?")
		args = append(args, flag)
	}

	if raw, ok := input["push_channel"]; ok {
		channel := strings.TrimSpace(str(raw))
		if !contains([]string{"none", "wecom", "serverchan", "pushplus", "bark"}, channel) {
			return nil, respond.NewError("VALIDATION_FAILED", "推送渠道无效", 422)
		}
		token := deref(user.PushToken)
		if v, exists := input["push_token"]; exists {
			token = strings.TrimSpace(str(v))
		}
		if len(token) > 255 {
			return nil, respond.NewError("VALIDATION_FAILED", "推送令牌过长", 422)
		}
		if channel != "none" && token == "" {
			return nil, respond.NewError("VALIDATION_FAILED", "请填写所选推送渠道的令牌", 422)
		}
		sets = append(sets, "push_channel = ?", "push_token = ?")
		if channel == "none" {
			args = append(args, channel, nil)
		} else {
			args = append(args, channel, token)
		}
	}

	args = append(args, userID)
	if _, err := s.store.Write.Exec(`UPDATE TF_users SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, respond.NewError("USER_FIELD_DUPLICATE", "邮箱或QQ号已被使用", 409)
		}
		return nil, respond.Internal("保存资料失败")
	}
	if _, changed := input["email"]; changed {
		if strings.ToLower(strings.TrimSpace(str(input["email"]))) != currentEmail {
			_ = s.revokeOtherSessions(userID, currentToken)
		}
	}
	return s.PublicUser(userID)
}

func (s *AuthService) revokeOtherSessions(userID int64, currentToken string) error {
	now := database.Now()
	if strings.TrimSpace(currentToken) == "" {
		_, err := s.store.Write.Exec(`UPDATE TF_sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, now, userID)
		return err
	}
	_, err := s.store.Write.Exec(`UPDATE TF_sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL AND token_hash<>?`,
		now, userID, secure.SHA256Hex(currentToken))
	return err
}

// ---------- 邮箱验证码 ----------

func (s *AuthService) IssueEmailCode(email, ip, purpose string) (map[string]any, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !contains([]string{"register", "change_email", "login"}, purpose) {
		return nil, respond.NewError("VALIDATION_FAILED", "验证用途无效", 422)
	}
	if !validEmail(email) || len(email) > 191 {
		return nil, respond.NewError("VALIDATION_FAILED", "请输入有效的邮箱地址", 422)
	}
	if !truthy(s.settings.Group("mail")["enabled"]) {
		return nil, respond.NewError("MAIL_DISABLED", "邮件服务尚未启用，请联系管理员", 409)
	}

	norm := NormalizeIdentity(email)
	if purpose == "register" {
		var exists int
		_ = s.store.Read.Get(&exists, `SELECT COUNT(1) FROM TF_users WHERE email_norm = ?`, norm)
		if exists > 0 {
			return nil, respond.NewError("USER_FIELD_DUPLICATE", "该邮箱已注册过账号，可直接登录或找回密码", 409)
		}
	}

	deliver := true
	var targetUserID *int64
	if purpose == "login" {
		var u struct {
			ID     int64  `db:"id"`
			Status string `db:"status"`
		}
		if err := s.store.Read.Get(&u, `SELECT id, status FROM TF_users WHERE email_norm = ?`, norm); err != nil || u.Status != "active" {
			deliver = false
		} else {
			targetUserID = &u.ID
		}
	}

	var latest *string
	_ = s.store.Read.Get(&latest, `SELECT created_at FROM TF_email_verification_codes
		WHERE email_norm = ? AND purpose = ? ORDER BY id DESC LIMIT 1`, norm, purpose)
	if latest != nil {
		if t, ok := database.Parse(*latest); ok && time.Since(t) < codeResend {
			return nil, respond.NewError("VERIFICATION_CODE_TOO_FREQUENT", "验证码发送过于频繁，请稍后再试", 429)
		}
	}
	hourAgo := database.Ts(time.Now().Add(-time.Hour))
	ipHash := secure.SHA256Hex(ip)
	var emailHourly, ipHourly int
	_ = s.store.Read.Get(&emailHourly, `SELECT COUNT(1) FROM TF_email_verification_codes
		WHERE email_norm = ? AND purpose = ? AND created_at >= ?`, norm, purpose, hourAgo)
	_ = s.store.Read.Get(&ipHourly, `SELECT COUNT(1) FROM TF_email_verification_codes
		WHERE ip_hash = ? AND created_at >= ?`, ipHash, hourAgo)
	if emailHourly >= codeHourlyEmail || ipHourly >= codeHourlyIP {
		return nil, respond.NewError("VERIFICATION_CODE_TOO_FREQUENT", "发送次数已达上限，请一小时后再试", 429)
	}

	code := randomCode()
	codeHash, err := s.hmacHex(code)
	if err != nil {
		return nil, err
	}
	now := database.Now()
	tx, err := s.store.Write.Beginx()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE TF_email_verification_codes SET consumed_at = ?
		WHERE email_norm = ? AND purpose = ? AND consumed_at IS NULL`, now, norm, purpose); err != nil {
		return nil, err
	}
	res, err := tx.Exec(`INSERT INTO TF_email_verification_codes
		(email, email_norm, purpose, code_hash, ip_hash, attempts, expires_at, created_at)
		VALUES(?,?,?,?,?,0,?,?)`,
		email, norm, purpose, codeHash, ipHash, database.Ts(time.Now().Add(codeTTL)), now)
	if err != nil {
		return nil, err
	}
	codeID, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if !deliver {
		_, _ = s.store.Write.Exec(`UPDATE TF_email_verification_codes SET consumed_at = ? WHERE id = ?`, now, codeID)
		return map[string]any{"expires_in": int(codeTTL.Seconds()), "resend_after": int(codeResend.Seconds())}, nil
	}

	title, preheader, message, subject, templateCode := emailCodeCopy(purpose)
	html, text := RenderNotification(NotificationData{
		Title:     title,
		Preheader: preheader,
		Greeting:  greetingFor(purpose),
		Message:   message,
		Facts: [][2]string{
			{"验证码", code},
			{"有效时间", "10 分钟"},
			{"安全提示", "验证码仅可使用一次"},
		},
		Accent: "#6366f1",
	})
	uid := int64(0)
	if targetUserID != nil {
		uid = *targetUserID
	}
	if err := s.mail.SendTransactional(email, subject, html, text, templateCode, uid); err != nil {
		_, _ = s.store.Write.Exec(`UPDATE TF_email_verification_codes SET consumed_at = ? WHERE id = ?`, database.Now(), codeID)
		return nil, err
	}
	return map[string]any{"expires_in": int(codeTTL.Seconds()), "resend_after": int(codeResend.Seconds())}, nil
}

func (s *AuthService) ConsumeEmailCode(email, code, purpose string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !regexpSixDigits.MatchString(code) {
		return respond.NewError("VERIFICATION_CODE_INVALID", "请输入邮箱收到的6位验证码", 422)
	}
	norm := NormalizeIdentity(email)
	var row struct {
		ID        int64  `db:"id"`
		CodeHash  string `db:"code_hash"`
		Attempts  int    `db:"attempts"`
		ExpiresAt string `db:"expires_at"`
	}
	err := s.store.Read.Get(&row, `SELECT id, code_hash, attempts, expires_at FROM TF_email_verification_codes
		WHERE email_norm = ? AND purpose = ? AND consumed_at IS NULL ORDER BY id DESC LIMIT 1`, norm, purpose)
	if err != nil {
		return respond.NewError("VERIFICATION_CODE_EXPIRED", "验证码已过期，请重新获取", 422)
	}
	if expires, ok := database.Parse(row.ExpiresAt); !ok || time.Now().After(expires) {
		return respond.NewError("VERIFICATION_CODE_EXPIRED", "验证码已过期，请重新获取", 422)
	}
	if row.Attempts >= codeMaxAttempts {
		return respond.NewError("VERIFICATION_CODE_LOCKED", "验证码错误次数过多，请重新获取", 429)
	}
	hash, err := s.hmacHex(code)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(row.CodeHash), []byte(hash)) {
		_, _ = s.store.Write.Exec(`UPDATE TF_email_verification_codes SET attempts = attempts + 1
			WHERE id = ? AND consumed_at IS NULL`, row.ID)
		return respond.NewError("VERIFICATION_CODE_INVALID", "验证码不正确，请检查后重试", 422)
	}
	// 原子标记已使用：并发提交同一个验证码时只允许一个请求成功。
	res, err := s.store.Write.Exec(`UPDATE TF_email_verification_codes SET consumed_at = ?
		WHERE id = ? AND consumed_at IS NULL`, database.Now(), row.ID)
	if err != nil {
		return respond.Internal("验证码状态更新失败")
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		return respond.NewError("VERIFICATION_CODE_EXPIRED", "验证码已使用或已过期，请重新获取", 422)
	}
	return nil
}

func (s *AuthService) IssueChangeEmailCode(userID int64, ip string) (map[string]any, error) {
	var email *string
	if err := s.store.Read.Get(&email, `SELECT email FROM TF_users WHERE id = ?`, userID); err != nil {
		return nil, respond.NotFound("用户不存在")
	}
	value := strings.ToLower(deref(email))
	if value == "" || !validEmail(value) {
		return nil, respond.NewError("EMAIL_REQUIRED", "当前账号尚未绑定邮箱，无法发送验证码", 422)
	}
	out, err := s.IssueEmailCode(value, ip, "change_email")
	if err != nil {
		return nil, err
	}
	out["email_masked"] = maskEmail(value)
	return out, nil
}

// ---------- 修改密码 / 找回密码 ----------

func (s *AuthService) IssuePasswordCode(userID int64) (map[string]any, error) {
	var user model.User
	if err := s.store.Read.Get(&user, `SELECT * FROM TF_users WHERE id = ?`, userID); err != nil {
		return nil, respond.NotFound("用户不存在")
	}
	email := strings.TrimSpace(deref(user.Email))
	if email == "" || !validEmail(email) {
		return nil, respond.NewError("EMAIL_REQUIRED", "请先在个人资料中设置有效邮箱", 422)
	}
	if !truthy(s.settings.Group("mail")["enabled"]) {
		return nil, respond.NewError("MAIL_DISABLED", "邮件服务尚未启用，请联系管理员", 409)
	}
	var latest *string
	_ = s.store.Read.Get(&latest, `SELECT created_at FROM TF_password_change_codes
		WHERE user_id = ? AND purpose = 'change' ORDER BY id DESC LIMIT 1`, userID)
	if latest != nil {
		if t, ok := database.Parse(*latest); ok && time.Since(t) < codeResend {
			return nil, respond.NewError("VERIFICATION_CODE_TOO_FREQUENT", "验证码发送过于频繁，请稍后再试", 429)
		}
	}
	var hourly int
	_ = s.store.Read.Get(&hourly, `SELECT COUNT(1) FROM TF_password_change_codes
		WHERE user_id = ? AND purpose = 'change' AND created_at >= ?`, userID, database.Ts(time.Now().Add(-time.Hour)))
	if hourly >= 5 {
		return nil, respond.NewError("VERIFICATION_CODE_LIMIT", "本小时验证码发送次数已达上限", 429)
	}

	code := randomCode()
	codeHash, err := s.hmacHex(code)
	if err != nil {
		return nil, err
	}
	now := database.Now()
	if _, err := s.store.Write.Exec(`UPDATE TF_password_change_codes SET consumed_at = ?
		WHERE user_id = ? AND purpose = 'change' AND consumed_at IS NULL`, now, userID); err != nil {
		return nil, err
	}
	res, err := s.store.Write.Exec(`INSERT INTO TF_password_change_codes
		(user_id, purpose, email, code_hash, attempts, expires_at, created_at) VALUES(?,?,?,?,0,?,?)`,
		userID, "change", strings.ToLower(email), codeHash, database.Ts(time.Now().Add(codeTTL)), now)
	if err != nil {
		return nil, err
	}
	codeID, _ := res.LastInsertId()
	html, text := RenderNotification(NotificationData{
		Title:     "修改登录密码",
		Preheader: "你的修改密码验证码已送达",
		Greeting:  user.Username,
		Message:   "你正在修改登录密码，请在 10 分钟内完成验证。如果不是本人操作，请忽略此邮件并检查账号安全。",
		Facts: [][2]string{
			{"验证码", code},
			{"有效时间", "10 分钟"},
			{"安全提示", "请勿转发给任何人"},
		},
		Accent: "#6366f1",
		Action: "返回个人设置",
	})
	if err := s.mail.SendTransactional(email, "修改登录密码验证码", html, text, "password_change_code", userID); err != nil {
		_, _ = s.store.Write.Exec(`UPDATE TF_password_change_codes SET consumed_at = ? WHERE id = ?`, database.Now(), codeID)
		return nil, err
	}
	return map[string]any{
		"email_masked": maskEmail(email),
		"expires_in":   int(codeTTL.Seconds()),
		"resend_after": int(codeResend.Seconds()),
	}, nil
}

func (s *AuthService) ChangePassword(userID int64, code, newPassword string) error {
	if !regexpSixDigits.MatchString(code) {
		return respond.NewError("VERIFICATION_CODE_INVALID", "请输入 6 位邮箱验证码", 422)
	}
	if len(newPassword) < 8 || len(newPassword) > 1024 {
		return respond.NewError("VALIDATION_FAILED", "新密码长度必须为8到1024个字符", 422)
	}
	return s.confirmPasswordCode(userID, "", code, newPassword, "change")
}

func (s *AuthService) IssuePasswordReset(email string) (map[string]any, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !validEmail(email) || len(email) > 191 {
		return map[string]any{"email_masked": maskEmail(email), "expires_in": int(codeTTL.Seconds()), "resend_after": int(codeResend.Seconds())},
			respond.NewError("VALIDATION_FAILED", "请输入有效的邮箱地址", 422)
	}
	if !truthy(s.settings.Group("mail")["enabled"]) {
		return nil, respond.NewError("MAIL_DISABLED", "邮件服务尚未启用，请联系管理员", 409)
	}
	response := map[string]any{
		"email_masked": maskEmail(email),
		"expires_in":   int(codeTTL.Seconds()),
		"resend_after": int(codeResend.Seconds()),
	}
	var user model.User
	if err := s.store.Read.Get(&user, `SELECT * FROM TF_users WHERE email_norm = ? AND status = 'active'`,
		NormalizeIdentity(email)); err != nil {
		time.Sleep(200 * time.Millisecond)
		return response, nil
	}
	var latest *string
	_ = s.store.Read.Get(&latest, `SELECT created_at FROM TF_password_change_codes
		WHERE user_id = ? AND purpose = 'reset' ORDER BY id DESC LIMIT 1`, user.ID)
	if latest != nil {
		if t, ok := database.Parse(*latest); ok && time.Since(t) < codeResend {
			return response, nil
		}
	}
	var hourly int
	_ = s.store.Read.Get(&hourly, `SELECT COUNT(1) FROM TF_password_change_codes
		WHERE user_id = ? AND purpose = 'reset' AND created_at >= ?`, user.ID, database.Ts(time.Now().Add(-time.Hour)))
	if hourly >= 5 {
		return response, nil
	}

	code := randomCode()
	codeHash, err := s.hmacHex(code)
	if err != nil {
		return nil, err
	}
	now := database.Now()
	if _, err := s.store.Write.Exec(`UPDATE TF_password_change_codes SET consumed_at = ?
		WHERE user_id = ? AND purpose = 'reset' AND consumed_at IS NULL`, now, user.ID); err != nil {
		return nil, err
	}
	res, err := s.store.Write.Exec(`INSERT INTO TF_password_change_codes
		(user_id, purpose, email, code_hash, attempts, expires_at, created_at) VALUES(?,?,?,?,0,?,?)`,
		user.ID, "reset", email, codeHash, database.Ts(time.Now().Add(codeTTL)), now)
	if err != nil {
		return nil, err
	}
	codeID, _ := res.LastInsertId()
	html, text := RenderNotification(NotificationData{
		Title:     "重置登录密码",
		Preheader: "你的密码重置验证码已送达",
		Greeting:  user.Username,
		Message:   "你正在通过邮箱找回登录密码，请在 10 分钟内完成验证。如果不是本人操作，请忽略此邮件。",
		Facts: [][2]string{
			{"验证码", code},
			{"有效时间", "10 分钟"},
			{"安全提示", "验证码仅可使用一次"},
		},
		Accent: "#6366f1",
		Action: "返回密码重置页面",
	})
	if err := s.mail.SendTransactional(email, "重置登录密码验证码", html, text, "password_reset_code", user.ID); err != nil {
		_, _ = s.store.Write.Exec(`UPDATE TF_password_change_codes SET consumed_at = ? WHERE id = ?`, database.Now(), codeID)
		return nil, err
	}
	return response, nil
}

func (s *AuthService) ResetPassword(email, code, newPassword string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !validEmail(email) || !regexpSixDigits.MatchString(code) {
		return respond.NewError("PASSWORD_RESET_INVALID", "邮箱或验证码不正确", 422)
	}
	if len(newPassword) < 8 || len(newPassword) > 1024 {
		return respond.NewError("VALIDATION_FAILED", "新密码长度必须为8到1024个字符", 422)
	}
	var userID int64
	if err := s.store.Read.Get(&userID, `SELECT id FROM TF_users WHERE email_norm = ? AND status = 'active'`,
		NormalizeIdentity(email)); err != nil {
		return respond.NewError("PASSWORD_RESET_INVALID", "邮箱或验证码不正确", 422)
	}
	return s.confirmPasswordCode(userID, email, code, newPassword, "reset")
}

func (s *AuthService) confirmPasswordCode(userID int64, email, code, newPassword, purpose string) error {
	var row struct {
		ID        int64  `db:"id"`
		CodeHash  string `db:"code_hash"`
		Attempts  int    `db:"attempts"`
		ExpiresAt string `db:"expires_at"`
		Email     string `db:"email"`
	}
	err := s.store.Read.Get(&row, `SELECT id, code_hash, attempts, expires_at, email FROM TF_password_change_codes
		WHERE user_id = ? AND purpose = ? AND consumed_at IS NULL ORDER BY id DESC LIMIT 1`, userID, purpose)
	if err != nil {
		return respond.NewError("VERIFICATION_CODE_EXPIRED", "验证码已过期，请重新获取", 422)
	}
	if expires, ok := database.Parse(row.ExpiresAt); !ok || time.Now().After(expires) {
		return respond.NewError("VERIFICATION_CODE_EXPIRED", "验证码已过期，请重新获取", 422)
	}
	if row.Attempts >= codeMaxAttempts {
		return respond.NewError("VERIFICATION_CODE_LOCKED", "验证码错误次数过多，请重新获取", 429)
	}
	hash, err := s.hmacHex(code)
	if err != nil {
		return err
	}
	invalidMsg := "邮箱验证码不正确"
	invalidCode := "VERIFICATION_CODE_INVALID"
	if purpose == "reset" {
		invalidMsg = "邮箱或验证码不正确"
		invalidCode = "PASSWORD_RESET_INVALID"
	}
	if !hmac.Equal([]byte(row.CodeHash), []byte(hash)) || (email != "" && !strings.EqualFold(row.Email, email)) {
		_, _ = s.store.Write.Exec(`UPDATE TF_password_change_codes SET attempts = attempts + 1
			WHERE id = ? AND consumed_at IS NULL`, row.ID)
		return respond.NewError(invalidCode, invalidMsg, 422)
	}
	newHash, err := secure.HashPassword(newPassword)
	if err != nil {
		return respond.Internal("密码加密失败")
	}
	now := database.Now()
	tx, err := s.store.Write.Beginx()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// 先原子占用验证码，避免同一个验证码被并发使用两次。
	res, err := tx.Exec(`UPDATE TF_password_change_codes SET consumed_at=?
		WHERE id=? AND consumed_at IS NULL`, now, row.ID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		return respond.NewError("VERIFICATION_CODE_EXPIRED", "验证码已使用或已过期，请重新获取", 422)
	}
	if _, err := tx.Exec(`UPDATE TF_users SET password_hash=?, password_migrated_at=?, updated_at=? WHERE id=?`,
		newHash, now, now, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE TF_sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, now, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func emailCodeCopy(purpose string) (title, preheader, message, subject, templateCode string) {
	switch purpose {
	case "change_email":
		return "更换邮箱验证", "你的更换邮箱验证码已送达",
			"你正在更换账号绑定的邮箱，请在 10 分钟内输入以下验证码确认本人操作。如果不是本人操作，请尽快修改密码并检查账号安全。",
			"更换邮箱验证码", "change_email_code"
	case "login":
		return "邮箱验证码登录", "你的登录验证码已送达",
			"你正在使用邮箱验证码登录天方云签，请在 10 分钟内输入以下验证码。如果不是本人操作，请忽略此邮件并检查账号安全。",
			"登录验证码", "login_email_code"
	default:
		return "注册邮箱验证", "你的注册验证码已送达",
			"欢迎注册！请在 10 分钟内输入以下验证码完成邮箱验证。如果不是本人操作，请忽略此邮件。",
			"注册邮箱验证码", "register_email_code"
	}
}

func greetingFor(purpose string) string {
	if purpose == "register" {
		return "新朋友"
	}
	return "用户"
}

// ---------- 人机验证 ----------

type challengeRecord struct {
	IDHash     string
	Purpose    string
	Answer     string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	VerifiedAt time.Time
	Attempts   int
	IPHash     string
	AgentHash  string
}

type challengeStore struct {
	store *database.Store
}

func newChallengeStore(store *database.Store) *challengeStore {
	return &challengeStore{store: store}
}

func (c *challengeStore) put(id string, rec *challengeRecord) error {
	_, err := c.store.Write.Exec(`INSERT INTO TF_challenges
		(id, id_hash, purpose, answer, issued_at, expires_at, verified_at, attempts, ip_hash, agent_hash, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		id, rec.IDHash, rec.Purpose, rec.Answer, database.Ts(rec.IssuedAt), database.Ts(rec.ExpiresAt),
		nilTime(rec.VerifiedAt), rec.Attempts, rec.IPHash, rec.AgentHash, database.Now())
	return err
}

// snapshot 返回挑战副本，调用方在锁外读取不会与数据库更新互相干扰。
func (c *challengeStore) snapshot(id string) (challengeRecord, bool) {
	var row struct {
		IDHash     string  `db:"id_hash"`
		Purpose    string  `db:"purpose"`
		Answer     string  `db:"answer"`
		IssuedAt   string  `db:"issued_at"`
		ExpiresAt  string  `db:"expires_at"`
		VerifiedAt *string `db:"verified_at"`
		Attempts   int     `db:"attempts"`
		IPHash     string  `db:"ip_hash"`
		AgentHash  string  `db:"agent_hash"`
	}
	if err := c.store.Read.Get(&row, `SELECT id_hash, purpose, answer, issued_at, expires_at, verified_at,
		attempts, ip_hash, agent_hash FROM TF_challenges WHERE id = ?`, id); err != nil {
		return challengeRecord{}, false
	}
	issued, ok := database.Parse(row.IssuedAt)
	if !ok {
		return challengeRecord{}, false
	}
	expires, ok := database.Parse(row.ExpiresAt)
	if !ok {
		return challengeRecord{}, false
	}
	rec := challengeRecord{
		IDHash: row.IDHash, Purpose: row.Purpose, Answer: row.Answer, IssuedAt: issued, ExpiresAt: expires,
		Attempts: row.Attempts, IPHash: row.IPHash, AgentHash: row.AgentHash,
	}
	if row.VerifiedAt != nil {
		if t, ok := database.Parse(*row.VerifiedAt); ok {
			rec.VerifiedAt = t
		}
	}
	return rec, true
}

func (c *challengeStore) markVerified(id string) bool {
	res, err := c.store.Write.Exec(`UPDATE TF_challenges SET verified_at = ?
		WHERE id = ? AND verified_at IS NULL AND expires_at > ?`, database.Now(), id, database.Now())
	if err != nil {
		return false
	}
	affected, _ := res.RowsAffected()
	return affected == 1
}

func (c *challengeStore) failAttempt(id string) {
	tx, err := c.store.Write.Beginx()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	_, _ = tx.Exec(`UPDATE TF_challenges SET attempts = attempts + 1 WHERE id = ?`, id)
	_, _ = tx.Exec(`DELETE FROM TF_challenges WHERE id = ? AND attempts >= 5`, id)
	_ = tx.Commit()
}

func (c *challengeStore) consume(id, purpose, agentHash string, now time.Time) bool {
	cutoff := database.Ts(now.Add(-verifiedTTL))
	res, err := c.store.Write.Exec(`DELETE FROM TF_challenges
		WHERE id = ? AND purpose = ? AND agent_hash = ? AND verified_at IS NOT NULL
		  AND verified_at >= ? AND expires_at > ?`,
		id, purpose, agentHash, cutoff, database.Ts(now))
	if err != nil {
		return false
	}
	affected, _ := res.RowsAffected()
	return affected == 1
}

func (c *challengeStore) remove(id string) {
	_, _ = c.store.Write.Exec(`DELETE FROM TF_challenges WHERE id = ?`, id)
}

const (
	challengeTTL   = 300 * time.Second
	verifiedTTL    = 120 * time.Second
	minChallengeMs = 300
)

func nilTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return database.Ts(t)
}

func (s *AuthService) IssueChallenge(purpose, ip, agent string) (map[string]any, error) {
	if !contains([]string{"login", "register"}, purpose) {
		return nil, respond.NewError("VALIDATION_FAILED", "人机验证用途无效", 422)
	}
	answer := captchaCode(captchaLength)
	id := secure.RandomHex(16)
	if err := s.challenge.put(id, &challengeRecord{
		IDHash:    secure.SHA256Hex(id),
		Purpose:   purpose,
		Answer:    answer,
		IssuedAt:  time.Now(),
		ExpiresAt: time.Now().Add(challengeTTL),
		IPHash:    secure.SHA256Hex(ip),
		AgentHash: secure.SHA256Hex(agent),
	}); err != nil {
		return nil, respond.Internal("创建人机验证失败")
	}
	return map[string]any{
		"challenge_id":  id,
		"expires_in":    int(challengeTTL.Seconds()),
		"captcha_image": captchaImage(answer),
	}, nil
}

// 说明：挑战绑定「ID + UA」，验证码答案只保存在服务端内存中。
// 反代/CDN 后面出口 IP 会随连接变化，因此不强制绑定 IP；IP 仍会记录供风控排查。
func (s *AuthService) VerifyChallenge(id, answer, ip, agent string) (map[string]any, error) {
	rec, ok := s.challenge.snapshot(id)
	if !ok || time.Now().After(rec.ExpiresAt) ||
		!hmac.Equal([]byte(rec.IDHash), []byte(secure.SHA256Hex(id))) ||
		!hmac.Equal([]byte(rec.AgentHash), []byte(secure.SHA256Hex(agent))) {
		s.challenge.remove(id)
		return nil, respond.NewError("HUMAN_CHALLENGE_EXPIRED", "验证已过期，请重试", 422)
	}
	serverElapsed := time.Since(rec.IssuedAt).Milliseconds()
	if serverElapsed < minChallengeMs || serverElapsed > 120000 {
		s.challenge.failAttempt(id)
		return nil, respond.NewError("HUMAN_CHALLENGE_INCOMPLETE", "验证已过期，请重新获取验证码", 422)
	}
	answer = strings.ToUpper(strings.TrimSpace(answer))
	if !hmac.Equal([]byte(answer), []byte(rec.Answer)) {
		s.challenge.failAttempt(id)
		return nil, respond.NewError("HUMAN_CHALLENGE_INCOMPLETE", "验证码不正确，请重试", 422)
	}
	if !s.challenge.markVerified(id) {
		return nil, respond.NewError("HUMAN_CHALLENGE_EXPIRED", "验证已过期，请重试", 422)
	}
	return map[string]any{
		"challenge_id": id,
		"verified":     true,
		"expires_in":   int(verifiedTTL.Seconds()),
	}, nil
}

func (s *AuthService) ConsumeChallenge(id, purpose, ip, agent string) error {
	// 校验与删除在同一个锁内完成，避免一个挑战被并发消费两次。
	if !s.challenge.consume(id, purpose, secure.SHA256Hex(agent), time.Now()) {
		return respond.NewError("HUMAN_VERIFICATION_REQUIRED", "请先完成人机验证", 422)
	}
	return nil
}

// ---------- 小工具 ----------

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func nilIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
