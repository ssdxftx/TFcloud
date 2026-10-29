package service

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"tfcloud/internal/config"
	"tfcloud/internal/database"
	"tfcloud/internal/httpapi/respond"
	"tfcloud/internal/model"
	"tfcloud/internal/secure"
	"tfcloud/internal/sign"
)

type AdminService struct {
	store    *database.Store
	cfg      *config.Config
	settings *SettingsService
	accounts *AccountService
	tasks    *TaskService
	audit    *AuditService
	registry *sign.Registry
	platform *PlatformService
	mail     *MailService
}

func NewAdmin(store *database.Store, cfg *config.Config, settings *SettingsService, accounts *AccountService,
	tasks *TaskService, audit *AuditService, registry *sign.Registry, platform *PlatformService, mail *MailService) *AdminService {
	return &AdminService{store: store, cfg: cfg, settings: settings, accounts: accounts, tasks: tasks,
		audit: audit, registry: registry, platform: platform, mail: mail}
}

func (s *AdminService) Overview() (map[string]any, error) {
	var totalUsers, activeUsers int
	_ = s.store.Read.Get(&totalUsers, `SELECT COUNT(1) FROM TF_users`)
	_ = s.store.Read.Get(&activeUsers, `SELECT COUNT(1) FROM TF_users WHERE status='active'`)

	accounts, err := s.store.Maps(`SELECT plugin_code, status, COUNT(*) AS total
		FROM TF_plugin_accounts WHERE deleted_at IS NULL GROUP BY plugin_code, status`)
	if err != nil {
		return nil, err
	}
	today := time.Now().Format("2006-01-02")
	rows := []struct {
		Status string `db:"status"`
		Total  int    `db:"total"`
	}{}
	_ = s.store.Read.Select(&rows, `SELECT status, COUNT(*) AS total FROM TF_sign_tasks
		WHERE schedule_date = ? GROUP BY status`, today)
	tasksToday := map[string]int{}
	for _, r := range rows {
		status := r.Status
		if status == "partial" {
			status = "succeeded"
		}
		tasksToday[status] += r.Total
	}
	var pending, running, failed24 int
	_ = s.store.Read.Get(&pending, `SELECT COUNT(1) FROM TF_sign_tasks WHERE status IN ('pending','retrying')`)
	_ = s.store.Read.Get(&running, `SELECT COUNT(1) FROM TF_sign_tasks WHERE status='running'`)
	_ = s.store.Read.Get(&failed24, `SELECT COUNT(1) FROM TF_sign_tasks WHERE status='failed' AND updated_at >= ?`,
		database.Ts(time.Now().Add(-24*time.Hour)))

	return map[string]any{
		"users":           map[string]any{"total": totalUsers, "active": activeUsers},
		"plugin_accounts": accounts,
		"tasks_today":     tasksToday,
		"queue":           map[string]any{"pending": pending, "running": running, "failed_24h": failed24},
	}, nil
}

// ---------- 用户管理 ----------

func (s *AdminService) Users(filters map[string]any) (map[string]any, error) {
	page, perPage, offset := paginate(filters, 20)
	where := []string{"1=1"}
	args := []any{}
	if keyword := strings.TrimSpace(str(filters["keyword"])); keyword != "" {
		like := "%" + escapeLike(keyword) + "%"
		where = append(where, "(username LIKE ? ESCAPE '\\' OR display_name LIKE ? ESCAPE '\\' OR email LIKE ? ESCAPE '\\' OR qq LIKE ? ESCAPE '\\')")
		args = append(args, like, like, like, like)
	}
	if role := strings.TrimSpace(str(filters["role"])); role != "" {
		where = append(where, "role = ?")
		args = append(args, role)
	}
	if status := strings.TrimSpace(str(filters["status"])); status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	clause := strings.Join(where, " AND ")
	var total int
	_ = s.store.Read.Get(&total, `SELECT COUNT(1) FROM TF_users WHERE `+clause, args...)
	items, err := s.store.Maps(`SELECT id, username, display_name, email, qq, role, status,
		daily_sign_email, push_channel, last_login_at, last_login_ip, created_at, updated_at
		FROM TF_users WHERE `+clause+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, perPage, offset)...)
	if err != nil {
		return nil, err
	}
	return pageResult(items, total, page, perPage, offset), nil
}

func (s *AdminService) CreateUser(input map[string]any, adminID int64, requestID ...string) (map[string]any, error) {
	username := strings.TrimSpace(str(input["username"]))
	password := str(input["password"])
	if len([]rune(username)) < 2 || len([]rune(username)) > 64 {
		return nil, respond.NewError("VALIDATION_FAILED", "用户名长度必须为2到64个字符", 422)
	}
	if len(password) < 8 || len(password) > 1024 {
		return nil, respond.NewError("VALIDATION_FAILED", "密码长度必须为8到1024个字符", 422)
	}
	role := firstNonEmpty(strings.TrimSpace(str(input["role"])), "user")
	if role != "user" && role != "admin" {
		return nil, respond.NewError("VALIDATION_FAILED", "角色无效", 422)
	}
	status := firstNonEmpty(strings.TrimSpace(str(input["status"])), "active")
	if status != "active" && status != "disabled" {
		return nil, respond.NewError("VALIDATION_FAILED", "状态无效", 422)
	}
	email := strings.ToLower(strings.TrimSpace(str(input["email"])))
	if email != "" && (!validEmail(email) || len(email) > 191) {
		return nil, respond.NewError("VALIDATION_FAILED", "邮箱格式不正确或过长", 422)
	}
	qq := strings.TrimSpace(str(input["qq"]))
	if len(qq) > 32 {
		return nil, respond.NewError("VALIDATION_FAILED", "QQ号过长", 422)
	}
	hash, err := secure.HashPassword(password)
	if err != nil {
		return nil, respond.Internal("密码加密失败")
	}
	now := database.Now()
	_, err = s.store.Write.Exec(`INSERT INTO TF_users
		(username, username_norm, password_hash, display_name, email, email_norm, qq, role, status,
		 daily_sign_email, push_channel, password_migrated_at, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,0,'none',?,?,?)`,
		username, NormalizeIdentity(username), hash, strings.TrimSpace(str(input["display_name"])),
		nullableString(strings.ToLower(email)), nullableString(NormalizeIdentity(email)), nullableString(qq),
		role, status, now, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, respond.NewError("USER_FIELD_DUPLICATE", "用户名、邮箱或QQ号已被使用", 409)
		}
		return nil, respond.Internal("创建用户失败")
	}
	var id int64
	_ = s.store.Read.Get(&id, `SELECT id FROM TF_users WHERE username_norm = ?`, NormalizeIdentity(username))
	s.audit.Record(adminID, "admin.user.create", "user", strconvID(id), map[string]any{"username": username}, firstString(requestID))
	return s.userRow(id)
}

func (s *AdminService) UpdateUser(id int64, input map[string]any, adminID int64, requestID ...string) (map[string]any, error) {
	var current struct {
		Role   string `db:"role"`
		Status string `db:"status"`
	}
	now := database.Now()
	sets := []string{"updated_at = ?"}
	args := []any{now}
	if v, ok := input["display_name"]; ok {
		sets = append(sets, "display_name = ?")
		args = append(args, truncate(strings.TrimSpace(str(v)), 191))
	}
	if v, ok := input["email"]; ok {
		email := strings.ToLower(strings.TrimSpace(str(v)))
		if email != "" && (!validEmail(email) || len(email) > 191) {
			return nil, respond.NewError("VALIDATION_FAILED", "邮箱格式不正确或过长", 422)
		}
		sets = append(sets, "email = ?", "email_norm = ?")
		args = append(args, nullableString(email), nullableString(NormalizeIdentity(email)))
	}
	if v, ok := input["qq"]; ok {
		qq := strings.TrimSpace(str(v))
		if len(qq) > 32 {
			return nil, respond.NewError("VALIDATION_FAILED", "QQ号过长", 422)
		}
		sets = append(sets, "qq = ?")
		args = append(args, nullableString(qq))
	}
	var roleChange, statusChange *string
	if v, ok := input["role"]; ok {
		role := strings.TrimSpace(str(v))
		if role != "user" && role != "admin" {
			return nil, respond.NewError("VALIDATION_FAILED", "角色无效", 422)
		}
		roleChange = &role
		sets = append(sets, "role = ?")
		args = append(args, role)
	}
	if v, ok := input["status"]; ok {
		status := strings.TrimSpace(str(v))
		if !contains([]string{"active", "disabled"}, status) {
			return nil, respond.NewError("VALIDATION_FAILED", "状态无效", 422)
		}
		statusChange = &status
		sets = append(sets, "status = ?")
		args = append(args, status)
	}

	tx, err := s.store.Write.Beginx()
	if err != nil {
		return nil, respond.Internal("更新用户失败")
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Get(&current, `SELECT role, status FROM TF_users WHERE id = ?`, id); err != nil {
		return nil, respond.NotFound("用户不存在")
	}
	finalRole, finalStatus := current.Role, current.Status
	if roleChange != nil {
		finalRole = *roleChange
	}
	if statusChange != nil {
		finalStatus = *statusChange
	}
	if current.Role == "admin" && current.Status == "active" &&
		(finalRole != "admin" || finalStatus != "active") {
		var admins int
		if err := tx.Get(&admins, `SELECT COUNT(1) FROM TF_users WHERE role='admin' AND status='active'`); err != nil {
			return nil, respond.Internal("更新用户失败")
		}
		if admins <= 1 {
			return nil, respond.NewError("VALIDATION_FAILED", "不能停用或降级最后一个管理员", 422)
		}
	}
	args = append(args, id)
	if _, err := tx.Exec(`UPDATE TF_users SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, respond.NewError("USER_FIELD_DUPLICATE", "邮箱或QQ号已被使用", 409)
		}
		return nil, respond.Internal("更新用户失败")
	}
	if statusChange != nil && *statusChange == "disabled" {
		if _, err := tx.Exec(`UPDATE TF_sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, now, id); err != nil {
			return nil, respond.Internal("更新用户失败")
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, respond.Internal("更新用户失败")
	}
	s.audit.Record(adminID, "admin.user.update", "user", strconvID(id), nil, firstString(requestID))
	return s.userRow(id)
}

func (s *AdminService) DeleteUser(id, adminID int64, requestID ...string) error {
	if id == adminID {
		return respond.NewError("VALIDATION_FAILED", "不能删除当前登录的管理员", 422)
	}
	tx, err := s.store.Write.Beginx()
	if err != nil {
		return respond.Internal("删除用户失败")
	}
	defer func() { _ = tx.Rollback() }()
	var role string
	if err := tx.Get(&role, `SELECT role FROM TF_users WHERE id = ?`, id); err != nil {
		return respond.NewError("VALIDATION_FAILED", "该用户不存在或不允许删除管理员", 409)
	}
	if role == "admin" {
		return respond.NewError("VALIDATION_FAILED", "该用户不存在或不允许删除管理员", 409)
	}
	if _, err := tx.Exec(`DELETE FROM TF_sign_tasks WHERE user_id = ?`, id); err != nil {
		return respond.Internal("删除用户失败")
	}
	if _, err := tx.Exec(`DELETE FROM TF_plugin_accounts WHERE user_id = ?`, id); err != nil {
		return respond.Internal("删除用户失败")
	}
	res, err := tx.Exec(`DELETE FROM TF_users WHERE id = ? AND role <> 'admin'`, id)
	if err != nil {
		return respond.Internal("删除用户失败")
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		return respond.NewError("VALIDATION_FAILED", "该用户不存在或不允许删除管理员", 409)
	}
	if err := tx.Commit(); err != nil {
		return respond.Internal("删除用户失败")
	}
	s.audit.Record(adminID, "admin.user.delete", "user", strconvID(id), nil, firstString(requestID))
	return nil
}

func (s *AdminService) ResetUserPassword(id int64, password string, adminID int64, requestID ...string) error {
	if len(password) < 8 || len(password) > 1024 {
		return respond.NewError("VALIDATION_FAILED", "密码长度必须为8到1024个字符", 422)
	}
	hash, err := secure.HashPassword(password)
	if err != nil {
		return respond.Internal("密码加密失败")
	}
	now := database.Now()
	tx, err := s.store.Write.Beginx()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.Exec(`UPDATE TF_users SET password_hash=?, password_migrated_at=?, updated_at=? WHERE id=?`,
		hash, now, now, id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		return respond.NotFound("用户不存在")
	}
	if _, err := tx.Exec(`UPDATE TF_sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, now, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.audit.Record(adminID, "admin.user.reset_password", "user", strconvID(id), nil, firstString(requestID))
	return nil
}

// UserByID 供详情接口使用，避免走列表再过滤。
func (s *AdminService) UserByID(id int64) (map[string]any, error) { return s.userRow(id) }

func (s *AdminService) userRow(id int64) (map[string]any, error) {
	item, err := s.store.Map(`SELECT id, username, display_name, email, qq, role, status,
		daily_sign_email, push_channel, last_login_at, last_login_ip, created_at, updated_at
		FROM TF_users WHERE id = ?`, id)
	if err != nil {
		return nil, respond.NotFound("用户不存在")
	}
	return item, nil
}

// ---------- 任务管理 ----------

func (s *AdminService) Tasks(filters map[string]any) (map[string]any, error) {
	page, perPage, offset := paginate(filters, 20)
	where := []string{"1=1"}
	args := []any{}
	if code := strings.TrimSpace(str(filters["plugin_code"])); code != "" {
		where = append(where, "t.plugin_code = ?")
		args = append(args, code)
	}
	if status := strings.TrimSpace(str(filters["status"])); status != "" {
		if status == "completed" {
			where = append(where, "t.status IN ('succeeded','partial')")
		} else {
			where = append(where, "t.status = ?")
			args = append(args, status)
		}
	}
	if uid := asInt(filters["user_id"]); uid > 0 {
		where = append(where, "t.user_id = ?")
		args = append(args, uid)
	}
	clause := strings.Join(where, " AND ")
	var total int
	_ = s.store.Read.Get(&total, `SELECT COUNT(1) FROM TF_sign_tasks t WHERE `+clause, args...)
	rows := []struct {
		model.SignTask
		Username string `db:"username"`
		Nickname string `db:"nickname"`
	}{}
	if err := s.store.Read.Select(&rows, `SELECT t.*, u.username AS username, a.display_name AS nickname
		FROM TF_sign_tasks t JOIN TF_users u ON u.id=t.user_id
		LEFT JOIN TF_plugin_accounts a ON a.id=t.account_id
		WHERE `+clause+` ORDER BY t.id DESC LIMIT ? OFFSET ?`, append(args, perPage, offset)...); err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(rows))
	for i := range rows {
		item := s.tasks.present(&rows[i].SignTask)
		item["username"] = rows[i].Username
		item["user_id"] = rows[i].SignTask.UserID
		item["account_name"] = rows[i].Nickname
		items = append(items, item)
	}
	return pageResult(items, total, page, perPage, offset), nil
}

func (s *AdminService) Task(taskNo string) (map[string]any, error) {
	task, err := s.tasks.FindOwnedForAdmin(taskNo)
	if err != nil {
		return nil, err
	}
	item := s.tasks.present(task)
	var username string
	_ = s.store.Read.Get(&username, `SELECT username FROM TF_users WHERE id=?`, task.UserID)
	item["username"] = username
	item["user_id"] = task.UserID
	return item, nil
}

func (s *AdminService) TaskRuns(taskNo string, filters map[string]any) (map[string]any, error) {
	task, err := s.tasks.FindOwnedForAdmin(taskNo)
	if err != nil {
		return nil, err
	}
	return s.tasks.Runs(task.UserID, task.TaskNo, filters)
}

func (s *AdminService) TaskRecords(taskNo string, filters map[string]any) (map[string]any, error) {
	task, err := s.tasks.FindOwnedForAdmin(taskNo)
	if err != nil {
		return nil, err
	}
	return s.tasks.Records(task.UserID, task.TaskNo, filters)
}

func (s *AdminService) RetryTask(taskNo string, adminID int64, requestID ...string) (map[string]any, error) {
	task, err := s.tasks.FindOwnedForAdmin(taskNo)
	if err != nil {
		return nil, err
	}
	out, err := s.tasks.FullRerun(task.UserID, taskNo)
	if err != nil {
		return nil, err
	}
	s.audit.Record(adminID, "admin.task.rerun", "sign_task", taskNo, nil, firstString(requestID))
	return out, nil
}

func (s *AdminService) CancelTask(taskNo string, adminID int64, requestID ...string) (map[string]any, error) {
	task, err := s.tasks.FindOwnedForAdmin(taskNo)
	if err != nil {
		return nil, err
	}
	out, err := s.tasks.Cancel(task.UserID, task.TaskNo)
	if err != nil {
		return nil, err
	}
	s.audit.Record(adminID, "admin.task.cancel", "sign_task", taskNo, nil, firstString(requestID))
	return out, nil
}

// ---------- 后台账号管理 ----------

func (s *AdminService) Accounts(filters map[string]any) (map[string]any, error) {
	page, perPage, offset := paginate(filters, 20)
	where := []string{"a.deleted_at IS NULL"}
	args := []any{}
	if code := strings.TrimSpace(str(filters["plugin_code"])); code != "" {
		where = append(where, "a.plugin_code = ?")
		args = append(args, code)
	}
	if status := strings.TrimSpace(str(filters["status"])); status != "" {
		where = append(where, "a.status = ?")
		args = append(args, status)
	}
	if keyword := strings.TrimSpace(str(filters["keyword"])); keyword != "" {
		like := "%" + escapeLike(keyword) + "%"
		where = append(where, "(a.display_name LIKE ? ESCAPE '\\' OR a.external_user_id LIKE ? ESCAPE '\\' OR u.username LIKE ? ESCAPE '\\')")
		args = append(args, like, like, like)
	}
	clause := strings.Join(where, " AND ")
	var total int
	_ = s.store.Read.Get(&total, `SELECT COUNT(1) FROM TF_plugin_accounts a JOIN TF_users u ON u.id=a.user_id WHERE `+clause, args...)
	rows := []struct {
		model.PluginAccount
		Username     string `db:"username"`
		CredentialID *int64 `db:"credential_id"`
	}{}
	if err := s.store.Read.Select(&rows, `SELECT a.*, u.username AS username, c.id AS credential_id
		FROM TF_plugin_accounts a JOIN TF_users u ON u.id=a.user_id
		LEFT JOIN TF_plugin_credentials c ON c.account_id=a.id
		WHERE `+clause+` ORDER BY a.id DESC LIMIT ? OFFSET ?`, append(args, perPage, offset)...); err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(rows))
	for i := range rows {
		item := s.accounts.present(&rows[i].PluginAccount, rows[i].CredentialID != nil)
		item["username"] = rows[i].Username
		item["user_id"] = rows[i].PluginAccount.UserID
		items = append(items, item)
	}
	return pageResult(items, total, page, perPage, offset), nil
}

func (s *AdminService) UpdateAccount(id int64, input map[string]any, adminID int64, requestID ...string) (map[string]any, error) {
	var acc model.PluginAccount
	if err := s.store.Read.Get(&acc, `SELECT * FROM TF_plugin_accounts WHERE id = ? AND deleted_at IS NULL`, id); err != nil {
		return nil, respond.NotFound("平台账号不存在")
	}
	out, err := s.accounts.Update(acc.UserID, id, input)
	if err != nil {
		return nil, err
	}
	s.audit.Record(adminID, "admin.account.update", "plugin_account", strconvID(id), nil, firstString(requestID))
	return out, nil
}

func (s *AdminService) DeleteAccount(id, adminID int64, requestID ...string) error {
	var acc model.PluginAccount
	if err := s.store.Read.Get(&acc, `SELECT * FROM TF_plugin_accounts WHERE id = ? AND deleted_at IS NULL`, id); err != nil {
		return respond.NotFound("平台账号不存在")
	}
	if err := s.accounts.Delete(acc.UserID, id); err != nil {
		return err
	}
	s.audit.Record(adminID, "admin.account.delete", "plugin_account", strconvID(id), nil, firstString(requestID))
	return nil
}

func (s *AdminService) AccountAction(id int64, action string, adminID int64, requestID ...string) (map[string]any, error) {
	var acc model.PluginAccount
	if err := s.store.Read.Get(&acc, `SELECT * FROM TF_plugin_accounts WHERE id = ? AND deleted_at IS NULL`, id); err != nil {
		return nil, respond.NotFound("平台账号不存在")
	}
	out, err := s.accounts.TriggerAction(acc.UserID, id, action)
	if err != nil {
		return nil, err
	}
	s.audit.Record(adminID, "admin.account.action", "plugin_account", strconvID(id), map[string]any{"action": action}, firstString(requestID))
	return out, nil
}

// ---------- 插件与设置 ----------

func (s *AdminService) Plugins() []map[string]any {
	out := []map[string]any{}
	for _, plugin := range s.registry.All() {
		meta := plugin.Metadata()
		rules := []map[string]any{}
		for _, rule := range plugin.CredentialRules() {
			rules = append(rules, map[string]any{"key": rule.Key, "required": rule.Required})
		}
		out = append(out, map[string]any{
			"code":                  meta.Code,
			"name":                  meta.Name,
			"version":               meta.Version,
			"description":           meta.Description,
			"implementation_status": meta.Status,
			"healthy":               meta.Status == "ready",
			"health_message":        "",
			"credential_types":      meta.CredentialTypes,
			"credential_rules":      rules,
			"actions":               plugin.SupportedActions(),
		})
	}
	return out
}

func (s *AdminService) Settings() map[string]any {
	return map[string]any{
		"site": s.settings.Group("site"),
		"mail": s.settings.Group("mail"),
	}
}

// ---------- 版本 ----------

func (s *AdminService) Version() map[string]any {
	current := readLocalVersion()
	latest := current
	reachable := false
	client := &http.Client{Timeout: 6 * time.Second}
	req, err := http.NewRequest(http.MethodGet, s.cfg.UpdateVersionURL, nil)
	if err == nil {
		req.Header.Set("User-Agent", "tfcloud-go")
		if s.cfg.UpdateGithubToken != "" {
			req.Header.Set("Authorization", "Bearer "+s.cfg.UpdateGithubToken)
		}
		if resp, err := client.Do(req); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				if body, err := io.ReadAll(io.LimitReader(resp.Body, 4096)); err == nil {
					if v := strings.TrimSpace(string(body)); v != "" {
						latest = v
						reachable = true
					}
				}
			}
		}
	}
	status := "unavailable"
	var checkedAt, checkError any
	if reachable {
		checkedAt = time.Now().Format(time.RFC3339)
		switch {
		case latest == current:
			status = "up_to_date"
		case versionGreater(latest, current):
			status = "update_available"
		default:
			status = "ahead"
		}
	} else {
		checkError = "远程版本检查失败"
	}
	return map[string]any{
		"program": map[string]any{
			"name":        "天方云签",
			"description": "基于 Go 与 React 构建的模块化自动签到管理平台。",
			"features": []string{
				"多平台自动签到与任务编排",
				"粉丝牌直播挂机与邮件通知",
				"凭据加密存储与完整审计日志",
			},
		},
		"author": map[string]any{
			"name":     "龙辉",
			"qq":       "1790716272",
			"qq_group": "701550577",
			"blog_url": "https://blog.eirds.cn/",
		},
		"copyright": map[string]any{
			"owner":       "龙辉",
			"start_year":  2016,
			"website":     "www.yunsign.net",
			"website_url": "https://www.yunsign.net",
			"notice":      "天方云签 · 龙辉 版权所有",
		},
		"version": map[string]any{
			"current":          current,
			"latest":           latest,
			"status":           status,
			"update_available": status == "update_available",
			"checked_at":       checkedAt,
			"error":            checkError,
			"update_page_url":  s.cfg.UpdatePageURL,
		},
		"repository_url": "https://github.com/longfie/TFcloud",
	}
}

// versionGreater 只比较前导数字段；任一侧无法解析时视为不可比较（返回 false）。
func versionGreater(candidate, current string) bool {
	a, okA := versionParts(candidate)
	b, okB := versionParts(current)
	if !okA || !okB {
		return false
	}
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func versionParts(v string) ([]int, bool) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(v, "v"))
	end := 0
	for end < len(trimmed) && (trimmed[end] >= '0' && trimmed[end] <= '9' || trimmed[end] == '.') {
		end++
	}
	if end == 0 {
		return nil, false
	}
	parts := strings.Split(strings.Trim(trimmed[:end], "."), ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func readLocalVersion() string {
	for _, path := range []string{"VERSION", "/app/VERSION"} {
		if data, err := os.ReadFile(path); err == nil {
			if v := strings.TrimSpace(string(data)); v != "" {
				return v
			}
		}
	}
	return "2.0.0-go"
}

func strconvID(id int64) string { return strconv.FormatInt(id, 10) }
