package service

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"tfcloud/internal/config"
	"tfcloud/internal/database"
	"tfcloud/internal/httpapi/respond"
	"tfcloud/internal/model"
	"tfcloud/internal/secure"
	"tfcloud/internal/sign"
)

type AccountService struct {
	store    *database.Store
	cfg      *config.Config
	settings *SettingsService
	registry *sign.Registry
	tasks    *TaskService
}

func NewAccount(store *database.Store, cfg *config.Config, settings *SettingsService, registry *sign.Registry) *AccountService {
	return &AccountService{store: store, cfg: cfg, settings: settings, registry: registry}
}

// SetTaskService 断开循环依赖：账号服务需要用任务服务创建执行任务。
func (s *AccountService) SetTaskService(tasks *TaskService) { s.tasks = tasks }

// ---------- 查询 ----------

func (s *AccountService) List(userID int64) ([]map[string]any, error) {
	rows := []struct {
		model.PluginAccount
		CredentialID *int64 `db:"credential_id"`
	}{}
	err := s.store.Read.Select(&rows, `SELECT a.*, c.id AS credential_id
		FROM TF_plugin_accounts a
		LEFT JOIN TF_plugin_credentials c ON c.account_id = a.id
		WHERE a.user_id = ? AND a.deleted_at IS NULL
		ORDER BY a.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.present(&row.PluginAccount, row.CredentialID != nil))
	}
	return out, nil
}

func (s *AccountService) Get(userID, accountID int64) (map[string]any, error) {
	acc, err := s.findOwned(userID, accountID)
	if err != nil {
		return nil, err
	}
	var credentialID *int64
	_ = s.store.Read.Get(&credentialID, `SELECT id FROM TF_plugin_credentials WHERE account_id = ?`, accountID)
	return s.present(acc, credentialID != nil), nil
}

func (s *AccountService) present(acc *model.PluginAccount, credentialConfigured bool) map[string]any {
	settings := normalizeLegacySettings(unmarshalMap(acc.SettingsJSON), acc.NextRunAt)
	profile := unmarshalMap(acc.ProfileJSON)
	avatar := strings.TrimSpace(str(profile["avatar"]))
	expired := acc.Status == "credential_expired" ||
		(acc.LastErrorCode != nil && (*acc.LastErrorCode == "PLUGIN_CREDENTIAL_EXPIRED" || *acc.LastErrorCode == "PLUGIN_CREDENTIAL_INVALID"))

	loginStatus := "normal"
	if expired {
		loginStatus = "expired"
	}
	var lastError any
	if acc.LastErrorCode != nil {
		lastError = map[string]any{"code": *acc.LastErrorCode, "message": deref(acc.LastErrorMessage)}
	}
	var avatarURL any
	if avatar != "" {
		avatarURL = avatar
	}
	return map[string]any{
		"id":                    acc.ID,
		"plugin_code":           acc.PluginCode,
		"external_user_id":      acc.ExternalUserID,
		"display_name":          acc.DisplayName,
		"avatar_url":            avatarURL,
		"status":                acc.Status,
		"login_status":          loginStatus,
		"settings":              settings,
		"credential_configured": credentialConfigured,
		"last_verified_at":      acc.LastVerifiedAt,
		"last_run_at":           acc.LastRunAt,
		"next_run_at":           acc.NextRunAt,
		"last_error":            lastError,
		"created_at":            acc.CreatedAt,
		"updated_at":            acc.UpdatedAt,
	}
}

func (s *AccountService) findOwned(userID, accountID int64) (*model.PluginAccount, error) {
	var acc model.PluginAccount
	err := s.store.Read.Get(&acc, `SELECT * FROM TF_plugin_accounts
		WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, accountID, userID)
	if err != nil {
		return nil, respond.NewError("PLUGIN_ACCOUNT_NOT_FOUND", "平台账号不存在", 404)
	}
	return &acc, nil
}

// ---------- 创建 / 更新 / 删除 ----------

func (s *AccountService) Create(userID int64, input map[string]any) (map[string]any, error) {
	var exists int
	if err := s.store.Read.Get(&exists, `SELECT COUNT(1) FROM TF_users WHERE id = ?`, userID); err != nil || exists == 0 {
		return nil, respond.NotFound("用户不存在")
	}
	pluginCode := strings.TrimSpace(str(input["plugin_code"]))
	credentials := sign.Map(input["credentials"])
	if pluginCode == "" || len(credentials) == 0 {
		return nil, respond.NewError("VALIDATION_FAILED", "plugin_code 和 credentials 不能为空", 422)
	}
	plugin, err := s.registry.Get(pluginCode)
	if err != nil {
		return nil, toAPIError(err)
	}
	if err := assertCredentialShape(plugin, credentials); err != nil {
		return nil, err
	}
	if raw, ok := input["settings"]; ok && raw != nil {
		if _, isMap := raw.(map[string]any); !isMap {
			return nil, respond.NewError("VALIDATION_FAILED", "settings 必须是对象", 422)
		}
	}
	settingsMap := sign.Map(input["settings"])
	if settingsMap == nil {
		settingsMap = map[string]any{}
	}
	if err := validateSettingsPayload(settingsMap); err != nil {
		return nil, err
	}

	var profile *sign.Profile
	if refreshing, ok := plugin.(sign.RefreshingValidator); ok {
		var refreshed map[string]any
		profile, refreshed, err = refreshing.ValidateAccountWithRefresh(credentials)
		if err == nil && refreshed != nil {
			credentials = mergeMaps(credentials, refreshed)
		}
	} else {
		profile, err = plugin.ValidateAccount(credentials)
	}
	if err != nil {
		return nil, toAPIError(err)
	}
	if err := validateSettings(pluginCode, settingsMap); err != nil {
		return nil, err
	}

	now := database.Now()
	tx, err := s.store.Write.Beginx()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	displayName := strings.TrimSpace(str(input["display_name"]))
	if displayName == "" {
		displayName = profile.DisplayName
	}
	externalID := strings.TrimSpace(profile.ExternalID)
	var external any
	if externalID != "" {
		external = externalID
	}
	profileJSON := mustJSON(mergeMaps(profile.Extra, map[string]any{"display_name": profile.DisplayName}))

	res, err := tx.Exec(`INSERT INTO TF_plugin_accounts
		(user_id, plugin_code, external_user_id, display_name, status, settings_json, profile_json,
		 last_verified_at, created_at, updated_at)
		VALUES(?,?,?,?, 'active', ?,?,?,?,?)`,
		userID, pluginCode, external, truncate(displayName, 191), mustJSON(settingsMap), profileJSON, now, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, respond.NewError("PLUGIN_ACCOUNT_DUPLICATE", "该平台账号已绑定", 409)
		}
		return nil, respond.Internal("创建平台账号失败")
	}
	accountID, _ := res.LastInsertId()

	nextRunAt := nextSchedule(settingsMap, pluginCode+":"+strconv.FormatInt(userID, 10)+":"+strconv.FormatInt(accountID, 10))
	if nextRunAt != "" {
		_, _ = tx.Exec(`UPDATE TF_plugin_accounts SET next_run_at = ?, updated_at = ? WHERE id = ?`, nextRunAt, now, accountID)
	}
	if err := s.writeCredential(tx, pluginCode, accountID, credentials); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, respond.Internal("创建平台账号失败")
	}
	return s.Get(userID, accountID)
}

func (s *AccountService) Update(userID, accountID int64, input map[string]any) (map[string]any, error) {
	acc, err := s.findOwned(userID, accountID)
	if err != nil {
		return nil, err
	}
	now := database.Now()
	seed := acc.PluginCode + ":" + strconv.FormatInt(userID, 10) + ":" + strconv.FormatInt(accountID, 10)

	var displayName *string
	if raw, ok := input["display_name"]; ok {
		name := truncate(strings.TrimSpace(str(raw)), 191)
		displayName = &name
	}

	var status *string
	if raw, ok := input["status"]; ok {
		value := strings.TrimSpace(str(raw))
		if !contains([]string{"active", "disabled", "pending_verification", "credential_expired"}, value) {
			return nil, respond.NewError("VALIDATION_FAILED", "账号状态无效", 422)
		}
		status = &value
	}

	var settingsMap map[string]any
	var settingsJSON *string
	if raw, ok := input["settings"]; ok && raw != nil {
		value, isMap := raw.(map[string]any)
		if !isMap {
			return nil, respond.NewError("VALIDATION_FAILED", "settings 必须是对象", 422)
		}
		if err := validateSettingsPayload(value); err != nil {
			return nil, err
		}
		if err := validateSettings(acc.PluginCode, value); err != nil {
			return nil, err
		}
		encoded := mustJSON(value)
		settingsMap = value
		settingsJSON = &encoded
	}

	var credentials map[string]any
	var profile *sign.Profile
	if raw, ok := input["credentials"]; ok && raw != nil {
		value, isMap := raw.(map[string]any)
		if !isMap || len(value) == 0 {
			return nil, respond.NewError("VALIDATION_FAILED", "credentials 必须是对象", 422)
		}
		plugin, err := s.registry.Get(acc.PluginCode)
		if err != nil {
			return nil, toAPIError(err)
		}
		if err := assertCredentialShape(plugin, value); err != nil {
			return nil, err
		}
		if refreshing, ok := plugin.(sign.RefreshingValidator); ok {
			var refreshed map[string]any
			profile, refreshed, err = refreshing.ValidateAccountWithRefresh(value)
			if err == nil && refreshed != nil {
				value = mergeMaps(value, refreshed)
			}
		} else {
			profile, err = plugin.ValidateAccount(value)
		}
		if err != nil {
			return nil, toAPIError(err)
		}
		credentials = value
	}

	tx, err := s.store.Write.Beginx()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if displayName != nil {
		if _, err := tx.Exec(`UPDATE TF_plugin_accounts SET display_name=?, updated_at=? WHERE id=?`, *displayName, now, accountID); err != nil {
			return nil, err
		}
	}
	if status != nil {
		var nextRunAt any
		if *status == "active" || *status == "pending_verification" {
			settings := normalizeLegacySettings(unmarshalMap(acc.SettingsJSON), acc.NextRunAt)
			nextRunAt = nullableString(nextSchedule(settings, seed))
		}
		if _, err := tx.Exec(`UPDATE TF_plugin_accounts SET status=?, next_run_at=?, updated_at=? WHERE id=?`,
			*status, nextRunAt, now, accountID); err != nil {
			return nil, err
		}
	}
	if settingsJSON != nil {
		next := nextSchedule(normalizeLegacySettings(settingsMap, acc.NextRunAt), seed)
		if _, err := tx.Exec(`UPDATE TF_plugin_accounts SET settings_json=?, next_run_at=?, updated_at=? WHERE id=?`,
			*settingsJSON, nullableString(next), now, accountID); err != nil {
			return nil, err
		}
	}
	if credentials != nil {
		if err := s.writeCredential(tx, acc.PluginCode, accountID, credentials); err != nil {
			return nil, err
		}
		profileJSON := mustJSON(mergeMaps(profile.Extra, map[string]any{"display_name": profile.DisplayName}))
		effectiveSettings := unmarshalMap(acc.SettingsJSON)
		if settingsMap != nil {
			effectiveSettings = settingsMap
		}
		normalized := normalizeLegacySettings(effectiveSettings, acc.NextRunAt)
		nextRunCandidate := nullableString(nextSchedule(normalized, seed))
		if _, err := tx.Exec(`UPDATE TF_plugin_accounts SET
			status=CASE WHEN ? THEN status ELSE 'active' END, last_verified_at=?, last_error_code=NULL,
			last_error_message=NULL, display_name=CASE WHEN display_name='' THEN ? ELSE display_name END,
			external_user_id=?, profile_json=?,
			next_run_at=CASE WHEN next_run_at IS NULL OR next_run_at <= ? THEN ? ELSE next_run_at END,
			updated_at=? WHERE id=?`,
			status != nil, now, truncate(profile.DisplayName, 191), nullableString(profile.ExternalID), profileJSON,
			now, nextRunCandidate, now, accountID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Get(userID, accountID)
}

func (s *AccountService) Delete(userID, accountID int64) error {
	if _, err := s.findOwned(userID, accountID); err != nil {
		return err
	}
	now := database.Now()
	tx, err := s.store.Write.Beginx()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE TF_sign_tasks SET status='cancelled', finished_at=?, updated_at=?
		WHERE account_id=? AND status IN ('pending','retrying')`, now, now, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE TF_plugin_accounts SET deleted_at=?, status='disabled', next_run_at=NULL, updated_at=?
		WHERE id=?`, now, now, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM TF_plugin_credentials WHERE account_id=?`, accountID); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------- 凭据 ----------

func credentialType(types []string) string {
	if len(types) == 0 {
		return "cookie"
	}
	return types[0]
}

func (s *AccountService) writeCredential(tx *sqlx.Tx, pluginCode string, accountID int64, credentials map[string]any) error {
	key, err := secure.DecodeKey(s.cfg.SecretKey())
	if err != nil {
		return respond.NewError("APP_KEY_MISSING", "应用密钥未配置", 500)
	}
	credType := "cookie"
	if plugin, err := s.registry.Get(pluginCode); err == nil {
		credType = credentialType(plugin.Metadata().CredentialTypes)
	}
	aad := pluginCode + ":" + strconv.FormatInt(accountID, 10) + ":" + credType
	plain, err := marshalNoEscape(credentials)
	if err != nil {
		return err
	}
	cipherText, nonce, err := secure.Seal(key, []byte(plain), []byte(aad))
	if err != nil {
		return respond.Internal("凭据加密失败")
	}
	fingerprint, err := secure.Fingerprint(s.cfg.FingerprintSecret(), credentials)
	if err != nil {
		return respond.NewError("CREDENTIAL_KEY_MISSING", "凭据指纹密钥尚未配置", 500)
	}
	now := database.Now()
	_, err = tx.Exec(`INSERT INTO TF_plugin_credentials
		(account_id, credential_type, payload_cipher, nonce, key_version, fingerprint, verified_at, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(account_id, credential_type) DO UPDATE SET
			payload_cipher=excluded.payload_cipher, nonce=excluded.nonce, key_version=excluded.key_version,
			fingerprint=excluded.fingerprint, verified_at=excluded.verified_at, rotated_at=excluded.updated_at,
			updated_at=excluded.updated_at`,
		accountID, credType, cipherText, nonce, s.cfg.CredentialKeyVersion, fingerprint, now, now, now)
	if err != nil {
		return respond.Internal("保存平台凭据失败")
	}
	return nil
}

func (s *AccountService) DecryptedCredential(acc *model.PluginAccount) (map[string]any, error) {
	var cred model.Credential
	if err := s.store.Read.Get(&cred, `SELECT * FROM TF_plugin_credentials WHERE account_id = ? ORDER BY id DESC LIMIT 1`,
		acc.ID); err != nil {
		return nil, respond.NewError("PLUGIN_CREDENTIAL_MISSING", "平台凭据缺失，请重新授权", 409)
	}
	key, err := secure.DecodeKey(s.cfg.SecretKey())
	if err != nil {
		return nil, respond.NewError("APP_KEY_MISSING", "应用密钥未配置", 500)
	}
	aad := acc.PluginCode + ":" + strconv.FormatInt(acc.ID, 10) + ":" + cred.CredentialType
	plain, err := secure.Open(key, cred.PayloadCipher, cred.Nonce, []byte(aad))
	if err != nil {
		return nil, respond.NewError("CREDENTIAL_DECRYPT_FAILED", "平台凭据无法解密", 500)
	}
	out := map[string]any{}
	if err := unmarshalInto(plain, &out); err != nil {
		return nil, respond.NewError("CREDENTIAL_FORMAT_INVALID", "平台凭据格式无效", 500)
	}
	return out, nil
}

// MergeRuntimeCredentials 供插件在执行后回写刷新令牌，仅允许白名单字段。
func (s *AccountService) MergeRuntimeCredentials(accountID int64, patch map[string]any) error {
	allowed := map[string]bool{
		"access_token": true, "refresh_token": true, "access_token_expires_at": true,
		"token": true, "token_updated_at": true, "app_token": true, "login_token": true,
		"user_id": true, "device_id": true, "display_name": true,
	}
	filtered := map[string]any{}
	for k, v := range patch {
		if allowed[k] {
			filtered[k] = v
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	var acc model.PluginAccount
	if err := s.store.Read.Get(&acc, `SELECT * FROM TF_plugin_accounts WHERE id = ?`, accountID); err != nil {
		return respond.NotFound("平台账号不存在")
	}
	current, err := s.DecryptedCredential(&acc)
	if err != nil {
		return err
	}
	merged := mergeMaps(current, filtered)
	tx, err := s.store.Write.Beginx()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.writeCredential(tx, acc.PluginCode, accountID, merged); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------- 动作 ----------

func (s *AccountService) TriggerAction(userID, accountID int64, action string) (map[string]any, error) {
	acc, err := s.findOwned(userID, accountID)
	if err != nil {
		return nil, err
	}
	plugin, err := s.registry.Get(acc.PluginCode)
	if err != nil {
		return nil, toAPIError(err)
	}
	if action == "" {
		action = strings.TrimSpace(str(unmarshalMap(acc.SettingsJSON)["scheduled_action"]))
	}
	if action == "" {
		actions := plugin.SupportedActions()
		if len(actions) == 0 {
			return nil, respond.NewError("PLUGIN_ACTION_UNSUPPORTED", "插件没有可执行动作", 422)
		}
		action = actions[0]
	}
	if !contains(plugin.SupportedActions(), action) {
		return nil, respond.NewError("PLUGIN_ACTION_UNSUPPORTED", "插件不支持该动作", 422)
	}
	if s.tasks == nil {
		return nil, respond.Internal("任务服务未初始化")
	}
	return s.tasks.Create(userID, accountID, action, "manual", time.Now().Format("2006-01-02"), "")
}

// ---------- 校验辅助 ----------

func assertCredentialShape(plugin sign.Plugin, credentials map[string]any) error {
	var missing []string
	for _, rule := range plugin.CredentialRules() {
		if !rule.Required {
			continue
		}
		if strings.TrimSpace(sign.ValueString(credentials[rule.Key])) == "" {
			missing = append(missing, rule.Key)
		}
	}
	if len(missing) > 0 {
		return respond.NewError("PLUGIN_CREDENTIAL_INVALID", "缺少平台凭据："+strings.Join(missing, ", "), 422)
	}
	total := 0
	for k, v := range credentials {
		s := sign.ValueString(v)
		if len(s) > 8192 {
			return respond.NewError("PLUGIN_CREDENTIAL_TOO_LARGE", "平台凭据内容过大", 422)
		}
		total += len(k) + len(s)
	}
	if total > 64<<10 {
		return respond.NewError("PLUGIN_CREDENTIAL_TOO_LARGE", "平台凭据总大小过大", 422)
	}
	return nil
}

const maxAccountSettingsBytes = 64 << 10

func validateSettingsPayload(settings map[string]any) error {
	encoded, err := marshalNoEscape(settings)
	if err != nil {
		return respond.NewError("VALIDATION_FAILED", "settings 无法序列化", 422)
	}
	if len(encoded) > maxAccountSettingsBytes {
		return respond.NewError("VALIDATION_FAILED", "settings 内容过大", 422)
	}
	return nil
}

var scheduleTimePattern = regexp.MustCompile(`^(?:[01]\d|2[0-3]):[0-5]\d$`)

func validateSettings(pluginCode string, settings map[string]any) error {
	if v, ok := settings["schedule_enabled"]; ok {
		if _, isBool := v.(bool); !isBool {
			return respond.NewError("VALIDATION_FAILED", "schedule_enabled 必须是布尔值", 422)
		}
	}
	if mode, ok := settings["schedule_mode"]; ok {
		value := strings.TrimSpace(str(mode))
		if value != "auto" && value != "fixed" {
			return respond.NewError("VALIDATION_FAILED", "schedule_mode 必须是 auto 或 fixed", 422)
		}
		if value == "fixed" && !scheduleTimePattern.MatchString(strings.TrimSpace(str(settings["schedule_time"]))) {
			return respond.NewError("SCHEDULE_TIME_INVALID", "固定计划必须填写 HH:MM 格式的执行时间", 422)
		}
	}
	if t, ok := settings["schedule_time"]; ok && strings.TrimSpace(str(t)) != "" {
		if !scheduleTimePattern.MatchString(strings.TrimSpace(str(t))) {
			return respond.NewError("SCHEDULE_TIME_INVALID", "schedule_time 必须使用 HH:MM 格式", 422)
		}
	}
	if pluginCode == "sport" {
		for _, key := range []string{"steps", "min_steps", "max_steps"} {
			if v, ok := settings[key]; ok {
				n := asInt(v)
				if n < 1 || n > 98800 {
					return respond.NewError("VALIDATION_FAILED", "运动步数必须在 1 到 98800 之间", 422)
				}
			}
		}
	}
	return nil
}

func mergeMaps(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(patch))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range patch {
		if v == nil {
			continue
		}
		out[k] = v
	}
	return out
}

func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func toAPIError(err error) error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*sign.Error); ok {
		return respond.NewError(e.Code, e.Message, e.Status)
	}
	if _, ok := err.(*respond.APIError); ok {
		return err
	}
	return respond.NewError("PLUGIN_EXECUTION_FAILED", err.Error(), 502)
}
