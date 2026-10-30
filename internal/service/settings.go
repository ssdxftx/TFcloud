package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"tfcloud/internal/config"
	"tfcloud/internal/database"
	"tfcloud/internal/httpapi/respond"
	"tfcloud/internal/secure"
)

type Field struct {
	Name    string
	Type    string // string|bool|int|enum|url|url_optional|email_optional
	Default any
	Max     int
	Min     int
	Options []string
	Secret  bool
}

type SettingsService struct {
	store *database.Store
	cfg   *config.Config
}

func NewSettings(store *database.Store, cfg *config.Config) *SettingsService {
	return &SettingsService{store: store, cfg: cfg}
}

func (s *SettingsService) definitions(group string) ([]Field, bool) {
	defs, ok := settingDefinitions[group]
	return defs, ok
}

func (s *SettingsService) Group(group string) map[string]any {
	defs, ok := s.definitions(group)
	if !ok {
		return map[string]any{}
	}
	rows := map[string]string{}
	var stored []struct {
		Key   string  `db:"setting_key"`
		Value *string `db:"setting_value"`
	}
	if err := s.store.Read.Select(&stored,
		`SELECT setting_key, setting_value FROM TF_settings WHERE setting_key LIKE ?`, group+".%"); err == nil {
		for _, r := range stored {
			if r.Value != nil {
				rows[r.Key] = *r.Value
			}
		}
	}
	out := make(map[string]any, len(defs)*2)
	for _, f := range defs {
		key := group + "." + f.Name
		raw, exists := rows[key]
		if f.Secret {
			out[f.Name] = ""
			out[f.Name+"_configured"] = exists
			continue
		}
		if !exists {
			out[f.Name] = f.Default
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			out[f.Name] = f.Default
			continue
		}
		out[f.Name] = v
	}
	return out
}

func (s *SettingsService) GroupWithSecrets(group string) (map[string]any, error) {
	out := s.Group(group)
	defs, ok := s.definitions(group)
	if !ok {
		return nil, respond.NotFound("配置分组不存在")
	}
	for _, f := range defs {
		if !f.Secret {
			continue
		}
		configured, _ := out[f.Name+"_configured"].(bool)
		if !configured {
			continue
		}
		var raw *string
		_ = s.store.Read.Get(&raw, `SELECT setting_value FROM TF_settings WHERE setting_key = ?`, group+"."+f.Name)
		if raw == nil {
			continue
		}
		plain, err := s.decodeSecret(*raw, "setting:"+group+"."+f.Name)
		if err != nil {
			continue
		}
		out[f.Name] = plain
	}
	return out, nil
}

func (s *SettingsService) Update(group string, input map[string]any, userID int64) (map[string]any, error) {
	defs, ok := s.definitions(group)
	if !ok {
		return nil, respond.NotFound("配置分组不存在")
	}
	now := database.Now()
	tx, err := s.store.Write.Beginx()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, f := range defs {
		value, present := input[f.Name]
		if !present {
			continue
		}
		if f.Secret {
			if str, _ := value.(string); strings.TrimSpace(str) == "" {
				continue
			}
		}
		validated, err := validateSetting(f.Name, value, f)
		if err != nil {
			return nil, err
		}
		key := group + "." + f.Name
		var encoded string
		if f.Secret {
			encoded, err = s.encodeSecret(validated.(string), "setting:"+key)
			if err != nil {
				return nil, err
			}
		} else {
			buf, _ := json.Marshal(validated)
			encoded = string(buf)
		}
		isSecret := 0
		if f.Secret {
			isSecret = 1
		}
		if _, err := tx.Exec(`INSERT INTO TF_settings(setting_key, setting_value, is_secret, updated_by, updated_at)
			VALUES(?,?,?,?,?)
			ON CONFLICT(setting_key) DO UPDATE SET setting_value=excluded.setting_value,
				is_secret=excluded.is_secret, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
			key, encoded, isSecret, userID, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Group(group), nil
}

// PublicSite 返回给前端 /api/site 的裁剪结果。
func (s *SettingsService) PublicSite() map[string]any {
	site := s.Group("site")
	mailGroup := s.Group("mail")

	legacy := strings.TrimSpace(str(site["announcement"]))
	if strings.TrimSpace(str(site["home_announcement"])) == "" {
		site["home_announcement"] = legacy
	}
	if strings.TrimSpace(str(site["dashboard_announcement"])) == "" {
		site["dashboard_announcement"] = legacy
	}
	site["announcement"] = site["dashboard_announcement"]

	var minCreated *string
	_ = s.store.Read.Get(&minCreated, `SELECT MIN(created_at) FROM TF_users`)
	runningSince := time.Now()
	if minCreated != nil {
		if parsed, ok := database.Parse(*minCreated); ok {
			runningSince = parsed
		}
	}
	nowDay := time.Now().Truncate(24 * time.Hour)
	sinceDay := runningSince.Truncate(24 * time.Hour)
	site["running_since"] = runningSince.Format("2006-01-02")
	site["running_days"] = int(nowDay.Sub(sinceDay).Hours()/24) + 1
	if days, _ := site["running_days"].(int); days < 1 {
		site["running_days"] = 1
	}

	var userCount, signedAccounts, signedUsers int
	_ = s.store.Read.Get(&userCount, `SELECT COUNT(1) FROM TF_users`)
	today := time.Now().Format("2006-01-02")
	_ = s.store.Read.Get(&signedAccounts, `SELECT COUNT(DISTINCT account_id) FROM TF_sign_tasks
		WHERE schedule_date = ? AND status IN ('succeeded','partial')`, today)
	_ = s.store.Read.Get(&signedUsers, `SELECT COUNT(DISTINCT user_id) FROM TF_sign_tasks
		WHERE schedule_date = ? AND status IN ('succeeded','partial')`, today)
	site["user_count"] = userCount
	site["today_signed_accounts"] = signedAccounts
	site["today_signed_users"] = signedUsers

	relayAppReady := str(site["qq_relay_application_status"]) == "active" &&
		strings.TrimSpace(str(site["qq_relay_client_id"])) != "" &&
		strings.TrimSpace(str(site["qq_relay_login_url"])) != "" &&
		truthy(site["qq_relay_client_secret_configured"])
	relayLegacyReady := strings.TrimSpace(str(site["qq_relay_authorize_url"])) != "" &&
		truthy(site["qq_relay_decode_key_configured"]) &&
		truthy(site["qq_relay_app_secret_configured"])
	var providerReady bool
	if str(site["qq_login_provider"]) == "relay" {
		providerReady = relayAppReady || relayLegacyReady
	} else {
		providerReady = strings.TrimSpace(str(site["qq_app_id"])) != "" && truthy(site["qq_app_key_configured"])
	}
	site["qq_login_enabled"] = truthy(site["qq_login_enabled"]) &&
		strings.TrimSpace(str(site["qq_callback_url"])) != "" && providerReady
	site["registration_email_verification"] = truthy(site["registration_email_verification"])
	site["mail_enabled"] = truthy(mailGroup["enabled"])
	site["timezone_offset"] = time.Now().Format("-07:00")
	site["today"] = time.Now().Format("2006-01-02")

	keys := []string{
		"name", "title", "description", "base_url", "logo_url",
		"home_background_url", "home_background_type", "contact_email",
		"icp_number", "announcement", "home_announcement", "dashboard_announcement",
		"maintenance_mode", "registration_enabled",
		"login_challenge_enabled", "registration_challenge_enabled",
		"registration_email_verification", "mail_enabled", "timezone_offset", "today",
		"qq_login_enabled", "qq_auto_register", "running_since", "running_days",
		"user_count", "today_signed_accounts", "today_signed_users",
	}
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		out[k] = site[k]
	}
	return out
}

func (s *SettingsService) encodeSecret(value, aad string) (string, error) {
	key, err := secure.DecodeKey(s.cfg.SecretKey())
	if err != nil {
		return "", err
	}
	plain, _ := json.Marshal(map[string]any{"value": value})
	cipher, nonce, err := secure.Seal(key, plain, []byte(aad))
	if err != nil {
		return "", err
	}
	buf, _ := json.Marshal(map[string]any{
		"cipher":      base64.StdEncoding.EncodeToString(cipher),
		"nonce":       base64.StdEncoding.EncodeToString(nonce),
		"key_version": s.cfg.CredentialKeyVersion,
	})
	return string(buf), nil
}

func (s *SettingsService) decodeSecret(encoded, aad string) (string, error) {
	var payload struct {
		Cipher string `json:"cipher"`
		Nonce  string `json:"nonce"`
	}
	if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
		return "", err
	}
	key, err := secure.DecodeKey(s.cfg.SecretKey())
	if err != nil {
		return "", err
	}
	cipher, err := base64.StdEncoding.DecodeString(payload.Cipher)
	if err != nil {
		return "", err
	}
	nonce, err := base64.StdEncoding.DecodeString(payload.Nonce)
	if err != nil {
		return "", err
	}
	plain, err := secure.Open(key, cipher, nonce, []byte(aad))
	if err != nil {
		return "", err
	}
	var out struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(plain, &out); err != nil {
		return "", err
	}
	return out.Value, nil
}

func validateSetting(name string, value any, f Field) (any, error) {
	switch f.Type {
	case "bool":
		switch v := value.(type) {
		case bool:
			return v, nil
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "1", "true", "yes", "on":
				return true, nil
			case "0", "false", "no", "off":
				return false, nil
			}
		}
		return nil, respond.NewError("VALIDATION_FAILED", name+" 必须是布尔值", 422)
	case "int":
		var n int
		switch v := value.(type) {
		case float64:
			n = int(v)
		case int:
			n = v
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, respond.NewError("VALIDATION_FAILED", name+" 数值无效", 422)
			}
			n = parsed
		default:
			return nil, respond.NewError("VALIDATION_FAILED", name+" 数值无效", 422)
		}
		if n < f.Min || (f.Max > 0 && n > f.Max) {
			return nil, respond.NewError("VALIDATION_FAILED", name+" 数值无效", 422)
		}
		return n, nil
	case "enum":
		str := strings.TrimSpace(str(value))
		for _, opt := range f.Options {
			if opt == str {
				return str, nil
			}
		}
		return nil, respond.NewError("VALIDATION_FAILED", name+" 选项无效", 422)
	case "url":
		str := strings.TrimSpace(str(value))
		if !validURL(str) {
			return nil, respond.NewError("VALIDATION_FAILED", name+" 必须是有效网址", 422)
		}
		return str, nil
	case "url_optional":
		str := strings.TrimSpace(str(value))
		if str != "" && !validURL(str) {
			return nil, respond.NewError("VALIDATION_FAILED", name+" 必须是有效网址", 422)
		}
		return str, nil
	case "email_optional":
		str := strings.TrimSpace(str(value))
		if str != "" && !validEmail(str) {
			return nil, respond.NewError("VALIDATION_FAILED", name+" 必须是有效邮箱", 422)
		}
		return str, nil
	default:
		str := strings.TrimSpace(str(value))
		if f.Max > 0 && utf8.RuneCountInString(str) > f.Max {
			return nil, respond.NewError("VALIDATION_FAILED", name+" 内容过长", 422)
		}
		return str, nil
	}
}

var urlPattern = regexp.MustCompile(`^https?://[^\s/$.?#].[^\s]*$`)

func validURL(v string) bool {
	if !urlPattern.MatchString(v) {
		return false
	}
	_, err := url.Parse(v)
	return err == nil
}

func validEmail(v string) bool {
	addr, err := mail.ParseAddress(v)
	return err == nil && addr.Address == v && strings.Contains(v, "@")
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	default:
		return fmt.Sprint(t)
	}
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "1", "true", "yes", "on":
			return true
		}
		return false
	case float64:
		return t != 0
	case int:
		return t != 0
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	default:
		return 0
	}
}
