package service

import (
	"context"
	"errors"
	"log/slog"
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

const (
	QueueDefault      = "default"
	QueueTiebaRetry   = "tieba_retry"
	QueueBilibiliLive = "bilibili_live"
	MaxAttempts       = 24
)

type TaskService struct {
	store    *database.Store
	cfg      *config.Config
	settings *SettingsService
	accounts *AccountService
	registry *sign.Registry
	audit    *AuditService
	mail     *MailService
	logger   *slog.Logger
}

func NewTask(store *database.Store, cfg *config.Config, settings *SettingsService, accounts *AccountService,
	registry *sign.Registry, audit *AuditService, mail *MailService, logger *slog.Logger) *TaskService {
	return &TaskService{store: store, cfg: cfg, settings: settings, accounts: accounts,
		registry: registry, audit: audit, mail: mail, logger: logger}
}

// ---------- 创建 ----------

func (s *AccountService) createTask(userID, accountID int64, action, triggerType, scheduleDate string) (map[string]any, error) {
	return nil, respond.Internal("task service not wired")
}

func (s *TaskService) Create(userID, accountID int64, action, triggerType, scheduleDate, requestID string) (map[string]any, error) {
	acc, err := s.accounts.findOwned(userID, accountID)
	if err != nil {
		return nil, err
	}
	if acc.Status != "active" && acc.Status != "pending_verification" {
		return nil, respond.NewError("PLUGIN_ACCOUNT_DISABLED", "插件账号未启用", 409)
	}
	plugin, err := s.registry.Get(acc.PluginCode)
	if err != nil {
		return nil, toAPIError(err)
	}
	if !contains(plugin.SupportedActions(), action) {
		return nil, respond.NewError("PLUGIN_ACTION_UNSUPPORTED", "插件不支持该动作", 422)
	}

	// 贴吧补签链路：同一账号存在未结束任务时直接复用，避免并行重试链。
	if acc.PluginCode == "tieba" {
		var active model.SignTask
		err := s.store.Read.Get(&active, `SELECT * FROM TF_sign_tasks
			WHERE account_id=? AND plugin_code='tieba' AND action=? AND status IN ('pending','retrying','running')
			ORDER BY id DESC LIMIT 1`, accountID, action)
		if err == nil {
			out := s.present(&active)
			out["duplicated"] = true
			return out, nil
		}
	}

	if scheduleDate == "" {
		scheduleDate = time.Now().Format("2006-01-02")
	}
	idempotency := secure.SHA256Hex(acc.PluginCode + ":" + strconv.FormatInt(accountID, 10) + ":" + action + ":" + scheduleDate)
	var existing model.SignTask
	if err := s.store.Read.Get(&existing, `SELECT * FROM TF_sign_tasks WHERE idempotency_key = ?`, idempotency); err == nil {
		out := s.present(&existing)
		out["duplicated"] = true
		return out, nil
	}

	queue := QueueDefault
	if acc.PluginCode == "bilibili" && action == "live_fans_medal" {
		queue = QueueBilibiliLive
	}
	priority := 0
	if triggerType == "manual" {
		priority = 10
	}
	now := database.Now()
	taskNo := secure.RandomHex(16)
	snapshot := mustJSON(map[string]any{
		"external_user_id": acc.ExternalUserID,
		"display_name":     acc.DisplayName,
		"plugin_version":   plugin.Metadata().Version,
	})
	_, err = s.store.Write.Exec(`INSERT INTO TF_sign_tasks
		(task_no, request_id, user_id, account_id, plugin_code, queue_name, action, trigger_type,
		 idempotency_key, schedule_date, status, priority, attempts, max_attempts,
		 available_at, account_snapshot_json, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?, 'pending', ?,0,?,?,?,?,?)`,
		taskNo, nullableString(requestID), userID, accountID, acc.PluginCode, queue, action, triggerType,
		idempotency, scheduleDate, priority, MaxAttempts, now, snapshot, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			var dup model.SignTask
			if err := s.store.Read.Get(&dup, `SELECT * FROM TF_sign_tasks WHERE idempotency_key = ?`, idempotency); err == nil {
				out := s.present(&dup)
				out["duplicated"] = true
				return out, nil
			}
		}
		return nil, respond.Internal("创建签到任务失败")
	}
	var task model.SignTask
	if err := s.store.Read.Get(&task, `SELECT * FROM TF_sign_tasks WHERE task_no = ?`, taskNo); err != nil {
		return nil, respond.Internal("创建签到任务失败")
	}
	out := s.present(&task)
	out["duplicated"] = false
	return out, nil
}

// ---------- 查询 ----------

func (s *TaskService) List(userID int64, filters map[string]any) (map[string]any, error) {
	page := clampInt(asInt(filters["page"]), 1, 100000)
	if page < 1 {
		page = 1
	}
	perPage := asInt(filters["per_page"])
	if perPage == 0 {
		perPage = asInt(filters["limit"])
	}
	if perPage <= 0 {
		perPage = 20
	}
	perPage = clampInt(perPage, 1, 100)
	offset := (page - 1) * perPage

	where := []string{"user_id = ?"}
	args := []any{userID}
	if code := strings.TrimSpace(str(filters["plugin_code"])); code != "" {
		where = append(where, "plugin_code = ?")
		args = append(args, code)
	}
	switch status := strings.TrimSpace(str(filters["status"])); status {
	case "":
	case "completed":
		where = append(where, "status IN ('succeeded','partial')")
	default:
		where = append(where, "status = ?")
		args = append(args, status)
	}
	clause := strings.Join(where, " AND ")
	var total int
	_ = s.store.Read.Get(&total, `SELECT COUNT(1) FROM TF_sign_tasks WHERE `+clause, args...)
	rows := []model.SignTask{}
	if err := s.store.Read.Select(&rows, `SELECT * FROM TF_sign_tasks WHERE `+clause+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, perPage, offset)...); err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(rows))
	for i := range rows {
		items = append(items, s.present(&rows[i]))
	}
	return pageResult(items, total, page, perPage), nil
}

func (s *TaskService) Show(userID int64, taskNo string) (map[string]any, error) {
	task, err := s.findOwned(userID, taskNo)
	if err != nil {
		return nil, err
	}
	return s.present(task), nil
}

func (s *TaskService) Calendar(userID int64, month string) (map[string]any, error) {
	if !scheduleMonthPattern.MatchString(month) {
		return nil, respond.NewError("VALIDATION_FAILED", "月份格式必须为 YYYY-MM", 422)
	}
	start, _ := time.ParseInLocation("2006-01", month, time.Local)
	end := start.AddDate(0, 1, -1).Format("2006-01-02")
	type row struct {
		ScheduleDate string `db:"schedule_date"`
		Status       string `db:"status"`
		Total        int    `db:"total"`
	}
	rows := []row{}
	if err := s.store.Read.Select(&rows, `SELECT schedule_date, status, COUNT(*) AS total
		FROM TF_sign_tasks WHERE user_id=? AND schedule_date IS NOT NULL AND schedule_date BETWEEN ? AND ?
		GROUP BY schedule_date, status ORDER BY schedule_date`, userID, month+"-01", end); err != nil {
		return nil, err
	}
	days := map[string]map[string]any{}
	order := []string{}
	for _, r := range rows {
		day, ok := days[r.ScheduleDate]
		if !ok {
			day = map[string]any{"date": r.ScheduleDate, "total": 0, "completed": 0, "failed": 0, "pending": 0, "cancelled": 0}
			days[r.ScheduleDate] = day
			order = append(order, r.ScheduleDate)
		}
		day["total"] = day["total"].(int) + r.Total
		switch r.Status {
		case "succeeded", "partial":
			day["completed"] = day["completed"].(int) + r.Total
		case "failed":
			day["failed"] = day["failed"].(int) + r.Total
		case "cancelled":
			day["cancelled"] = day["cancelled"].(int) + r.Total
		default:
			day["pending"] = day["pending"].(int) + r.Total
		}
	}
	out := make([]map[string]any, 0, len(order))
	for _, d := range order {
		out = append(out, days[d])
	}
	return map[string]any{"month": month, "days": out}, nil
}

func (s *TaskService) Runs(userID int64, taskNo string, filters map[string]any) (map[string]any, error) {
	task, err := s.findOwned(userID, taskNo)
	if err != nil {
		return nil, err
	}
	page, perPage, offset := paginate(filters, 20)
	var total int
	_ = s.store.Read.Get(&total, `SELECT COUNT(1) FROM TF_sign_runs WHERE task_id = ?`, task.ID)
	items, err := s.store.Maps(`SELECT * FROM TF_sign_runs WHERE task_id = ?
		ORDER BY attempt_no DESC LIMIT ? OFFSET ?`, task.ID, perPage, offset)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if v, ok := item["summary_json"]; ok {
			item["summary_json"] = parseJSONText(v)
		}
	}
	return pageResult(items, total, page, perPage), nil
}

func (s *TaskService) Records(userID int64, taskNo string, filters map[string]any) (map[string]any, error) {
	task, err := s.findOwned(userID, taskNo)
	if err != nil {
		return nil, err
	}
	page := asInt(filters["page"])
	if page < 1 {
		page = 1
	}
	perPage := asInt(filters["per_page"])
	if perPage == 0 {
		perPage = asInt(filters["limit"])
	}
	if perPage <= 0 {
		perPage = 20
	}
	perPage = clampInt(perPage, 1, 500)
	offset := (page - 1) * perPage

	where := []string{"task_id = ?"}
	args := []any{task.ID}
	if status := strings.TrimSpace(str(filters["status"])); contains([]string{"running", "succeeded", "already_done", "skipped", "failed"}, status) {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if action := strings.TrimSpace(str(filters["action"])); action != "" && len([]rune(action)) <= 64 {
		where = append(where, "action = ?")
		args = append(args, action)
	}
	if attempt := asInt(filters["attempt_no"]); attempt > 0 {
		var runID int64
		_ = s.store.Read.Get(&runID, `SELECT id FROM TF_sign_runs WHERE task_id=? AND attempt_no=?`, task.ID, attempt)
		if runID == 0 {
			runID = -1
		}
		where = append(where, "run_id = ?")
		args = append(args, runID)
	}
	if keyword := strings.TrimSpace(str(filters["keyword"])); keyword != "" {
		like := "%" + escapeLike(truncate(keyword, 100)) + "%"
		where = append(where, "(target_name LIKE ? ESCAPE '\\' OR target_id LIKE ? ESCAPE '\\' OR message LIKE ? ESCAPE '\\' OR result_code LIKE ? ESCAPE '\\')")
		args = append(args, like, like, like, like)
	}
	clause := strings.Join(where, " AND ")
	var total int
	_ = s.store.Read.Get(&total, `SELECT COUNT(1) FROM TF_sign_records WHERE `+clause, args...)
	items, err := s.store.Maps(`SELECT * FROM TF_sign_records WHERE `+clause+
		` ORDER BY sequence_no, id LIMIT ? OFFSET ?`, append(args, perPage, offset)...)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		for _, field := range []string{"reward_json", "metrics_json", "safe_result_json"} {
			if v, ok := item[field]; ok {
				item[field] = parseJSONTextDefault(v)
			}
		}
	}
	return pageResult(items, total, page, perPage), nil
}

func (s *TaskService) Retry(userID int64, taskNo string) (map[string]any, error) {
	task, err := s.findOwned(userID, taskNo)
	if err != nil {
		return nil, err
	}
	if !contains([]string{"failed", "partial", "cancelled"}, task.Status) {
		return nil, respond.NewError("TASK_NOT_RETRYABLE", "当前任务状态不能重试", 409)
	}
	if task.Attempts >= task.MaxAttempts {
		return nil, respond.NewError("TASK_RETRY_EXHAUSTED", "任务重试次数已经用完", 409)
	}
	now := database.Now()
	_, err = s.store.Write.Exec(`UPDATE TF_sign_tasks SET status='pending', available_at=?, locked_at=NULL,
		locked_by=NULL, updated_at=? WHERE id=?`, now, now, task.ID)
	if err != nil {
		return nil, err
	}
	return s.Show(userID, taskNo)
}

func (s *TaskService) Cancel(userID int64, taskNo string) (map[string]any, error) {
	task, err := s.findOwned(userID, taskNo)
	if err != nil {
		return nil, err
	}
	if !contains([]string{"pending", "retrying"}, task.Status) {
		return nil, respond.NewError("TASK_NOT_CANCELLABLE", "当前任务状态不能取消", 409)
	}
	now := database.Now()
	if _, err := s.store.Write.Exec(`UPDATE TF_sign_tasks SET status='cancelled', finished_at=?, updated_at=? WHERE id=?`,
		now, now, task.ID); err != nil {
		return nil, err
	}
	return s.Show(userID, taskNo)
}

// FullRerun 管理员完整重跑：新建任务，保留原任务与记录。
func (s *TaskService) FullRerun(userID int64, taskNo string) (map[string]any, error) {
	source, err := s.findOwned(userID, taskNo)
	if err != nil {
		return nil, err
	}
	if contains([]string{"pending", "retrying", "running"}, source.Status) {
		return nil, respond.NewError("TASK_STILL_ACTIVE", "任务仍在执行或等待中，不能完整重跑", 409)
	}
	acc, err := s.accounts.findOwned(userID, source.AccountID)
	if err != nil {
		return nil, err
	}
	if acc.Status != "active" && acc.Status != "pending_verification" {
		return nil, respond.NewError("PLUGIN_ACCOUNT_DISABLED", "插件账号未启用，不能重新执行", 409)
	}
	plugin, err := s.registry.Get(source.PluginCode)
	if err != nil {
		return nil, toAPIError(err)
	}
	if !contains(plugin.SupportedActions(), source.Action) {
		return nil, respond.NewError("PLUGIN_ACTION_UNSUPPORTED", "插件已不再支持该任务动作", 422)
	}
	now := database.Now()
	newNo := secure.RandomHex(16)
	queue := QueueDefault
	if source.PluginCode == "bilibili" && source.Action == "live_fans_medal" {
		queue = QueueBilibiliLive
	}
	snapshot := mustJSON(map[string]any{
		"external_user_id":        acc.ExternalUserID,
		"display_name":            acc.DisplayName,
		"plugin_version":          plugin.Metadata().Version,
		"rerun_of":                source.TaskNo,
		"refresh_followed_forums": source.PluginCode == "tieba",
	})
	_, err = s.store.Write.Exec(`INSERT INTO TF_sign_tasks
		(task_no, user_id, account_id, plugin_code, queue_name, action, trigger_type, idempotency_key,
		 schedule_date, status, priority, attempts, max_attempts, available_at, account_snapshot_json, created_at, updated_at)
		VALUES(?,?,?,?,?,?, 'admin_rerun', ?,?, 'pending', 10, 0, ?, ?, ?, ?, ?)`,
		newNo, userID, source.AccountID, source.PluginCode, queue, source.Action,
		secure.SHA256Hex(source.TaskNo+":admin_rerun:"+newNo), time.Now().Format("2006-01-02"),
		MaxAttempts, now, snapshot, now, now)
	if err != nil {
		return nil, respond.Internal("创建重跑任务失败")
	}
	var task model.SignTask
	if err := s.store.Read.Get(&task, `SELECT * FROM TF_sign_tasks WHERE task_no = ?`, newNo); err != nil {
		return nil, respond.Internal("创建重跑任务失败")
	}
	out := s.present(&task)
	out["source_task_no"] = source.TaskNo
	return out, nil
}

// ---------- 调度 ----------

// DispatchDue 扫描到期账号并按日创建任务。
func (s *TaskService) DispatchDue(limit int) int {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows := []model.PluginAccount{}
	err := s.store.Read.Select(&rows, `SELECT a.* FROM TF_plugin_accounts a
		JOIN TF_users u ON u.id = a.user_id
		WHERE a.status IN ('active','pending_verification') AND a.deleted_at IS NULL
		  AND a.next_run_at IS NOT NULL AND a.next_run_at <= ?
		ORDER BY a.next_run_at, a.id LIMIT ?`, database.Now(), limit)
	if err != nil {
		s.logger.Error("dispatch query failed", slog.String("error", err.Error()))
		return 0
	}
	dispatched := 0
	for i := range rows {
		acc := &rows[i]
		if s.dispatchAccount(acc) {
			dispatched++
		}
	}
	return dispatched
}

func (s *TaskService) dispatchAccount(acc *model.PluginAccount) bool {
	plugin, err := s.registry.Get(acc.PluginCode)
	if err != nil {
		_, _ = s.store.Write.Exec(`UPDATE TF_plugin_accounts SET next_run_at=NULL, updated_at=? WHERE id=?`,
			database.Now(), acc.ID)
		return false
	}
	settings := normalizeLegacySettings(unmarshalMap(acc.SettingsJSON), acc.NextRunAt)
	seed := acc.PluginCode + ":" + strconv.FormatInt(acc.UserID, 10) + ":" + strconv.FormatInt(acc.ID, 10)
	nextRun := nextSchedule(settings, seed)

	action := strings.TrimSpace(str(settings["scheduled_action"]))
	if action == "" {
		actions := plugin.SupportedActions()
		if len(actions) > 0 {
			action = actions[0]
		}
	}
	var actions []string
	if action != "" && action != "live_fans_medal" {
		actions = append(actions, action)
	}
	if acc.PluginCode == "bilibili" && truthy(settings["live_fans_medal_enabled"]) {
		actions = append(actions, "live_fans_medal")
	}
	actions = unique(actions)

	for _, act := range actions {
		if _, err := s.Create(acc.UserID, acc.ID, act, "schedule", time.Now().Format("2006-01-02"), ""); err != nil {
			s.logger.Warn("schedule task create failed",
				slog.Int64("account", acc.ID), slog.String("action", act), slog.String("error", err.Error()))
		}
	}
	_, err = s.store.Write.Exec(`UPDATE TF_plugin_accounts SET next_run_at=?, updated_at=? WHERE id=?`,
		nullableString(nextRun), database.Now(), acc.ID)
	if err != nil {
		s.logger.Error("update next_run_at failed", slog.Int64("account", acc.ID), slog.String("error", err.Error()))
	}
	return len(actions) > 0
}

// RequeueFailedToday 每天 20 点后给当天失败的普通任务追加一次机会。
func (s *TaskService) RequeueFailedToday(limit int) int {
	if time.Now().Hour() < 20 {
		return 0
	}
	if limit <= 0 {
		limit = 200
	}
	rows := []model.SignTask{}
	if err := s.store.Read.Select(&rows, `SELECT * FROM TF_sign_tasks
		WHERE status='failed' AND attempts >= max_attempts AND max_attempts = ?
		  AND schedule_date = ? AND last_error_code NOT IN ('PLUGIN_CREDENTIAL_EXPIRED','PLUGIN_CREDENTIAL_INVALID')
		ORDER BY id LIMIT ?`, MaxAttempts, time.Now().Format("2006-01-02"), limit); err != nil {
		return 0
	}
	n := 0
	for i := range rows {
		now := database.Now()
		res, err := s.store.Write.Exec(`UPDATE TF_sign_tasks SET status='pending', available_at=?, max_attempts=max_attempts+1,
			finished_at=NULL, updated_at=? WHERE id=? AND status='failed'`, now, now, rows[i].ID)
		if err != nil {
			continue
		}
		if affected, _ := res.RowsAffected(); affected == 1 {
			n++
		}
	}
	return n
}

// ---------- 执行 ----------

// Claim 以单条原子语句领取任务，消除 PHP 版「先查后改」的竞态。
func (s *TaskService) Claim(workerID, queue string) (*model.SignTask, bool) {
	now := database.Now()
	var id int64
	var err error
	if queue == QueueTiebaRetry {
		err = s.store.Write.Get(&id, `UPDATE TF_sign_tasks SET status='running', attempts=attempts+1,
			locked_at=?, locked_by=?, started_at=COALESCE(started_at, ?), updated_at=?
			WHERE id = (SELECT id FROM TF_sign_tasks
				WHERE status='retrying' AND plugin_code='tieba' AND last_error_code='UPSTREAM_REQUEST_FAILED'
				  AND (available_at IS NULL OR available_at <= ?)
				ORDER BY priority DESC, id LIMIT 1)
			RETURNING id`, now, workerID, now, now, now)
	} else {
		err = s.store.Write.Get(&id, `UPDATE TF_sign_tasks SET status='running', attempts=attempts+1,
			locked_at=?, locked_by=?, started_at=COALESCE(started_at, ?), updated_at=?
			WHERE id = (SELECT id FROM TF_sign_tasks
				WHERE queue_name='default'
				  AND (available_at IS NULL OR available_at <= ?)
				  AND (status='pending' OR (status='retrying' AND NOT (plugin_code='tieba' AND last_error_code='UPSTREAM_REQUEST_FAILED')))
				ORDER BY priority DESC, id LIMIT 1)
			RETURNING id`, now, workerID, now, now, now)
	}
	if err != nil {
		return nil, false
	}
	var task model.SignTask
	if err := s.store.Read.Get(&task, `SELECT * FROM TF_sign_tasks WHERE id = ?`, id); err != nil {
		return nil, false
	}
	return &task, true
}

func (s *TaskService) ExecuteTask(task *model.SignTask, workerID string) {
	plugin, err := s.registry.Get(task.PluginCode)
	if err != nil {
		s.failTask(task, 0, err, time.Now())
		return
	}
	started := time.Now()
	now := database.Now()
	res, err := s.store.Write.Exec(`INSERT INTO TF_sign_runs
		(run_no, task_id, attempt_no, plugin_code, plugin_version, worker_id, status, started_at, created_at)
		VALUES(?,?,?,?,?,?, 'running', ?,?)`,
		secure.RandomHex(16), task.ID, task.Attempts, task.PluginCode, plugin.Metadata().Version, workerID, now, now)
	if err != nil {
		s.logger.Error("create run failed", slog.String("error", err.Error()))
		return
	}
	runID, _ := res.LastInsertId()

	acc, err := s.accounts.findOwned(task.UserID, task.AccountID)
	if err != nil {
		s.failTask(task, runID, err, started)
		return
	}
	if acc.Status != "active" && acc.Status != "pending_verification" {
		s.failTask(task, runID, respond.NewError("PLUGIN_ACCOUNT_DISABLED", "插件账号当前不可执行", 409), started)
		return
	}
	credentials, err := s.accounts.DecryptedCredential(acc)
	if err != nil {
		s.failTask(task, runID, err, started)
		return
	}
	settingsMap := unmarshalMap(acc.SettingsJSON)
	profile := unmarshalMap(acc.ProfileJSON)
	summary := unmarshalMap(task.SummaryJSON)

	options := map[string]any{"profile": profile}
	if task.PluginCode == "tieba" {
		if task.TriggerType == "admin_rerun" && task.Attempts == 1 {
			options["force_refresh_forums"] = true
		} else if task.LastErrorCode != nil && *task.LastErrorCode == "UPSTREAM_REQUEST_FAILED" &&
			str(summary["request_failed_date"]) == time.Now().Format("2006-01-02") {
			options["retry_forum_ids"] = summary["request_failed_forum_ids"]
		}
	}

	reporter := &progressReporter{svc: s, task: task, runID: runID}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, execErr := plugin.Execute(ctx, &sign.Context{
		UserID:      task.UserID,
		AccountID:   task.AccountID,
		Action:      task.Action,
		Credentials: credentials,
		Settings:    settingsMap,
		Options:     options,
		Report:      reporter.push,
	})
	if execErr != nil {
		s.failTask(task, runID, execErr, started)
		return
	}
	s.completeTask(task, runID, plugin, result, started, reporter)
}

func (s *TaskService) completeTask(task *model.SignTask, runID int64, plugin sign.Plugin,
	result *sign.Result, started time.Time, reporter *progressReporter) {
	counts := reporter.counts()
	if !reporter.used {
		counts = map[string]int{"total": 0, "success": 0, "already": 0, "skipped": 0, "failed": 0}
		sequence := 0
		for _, rec := range result.Records {
			s.saveRecord(task, runID, rec, sequence, counts)
			sequence++
		}
	}

	runStatus := "succeeded"
	if counts["failed"] > 0 {
		if counts["success"]+counts["already"] > 0 {
			runStatus = "partial"
		} else {
			runStatus = "failed"
		}
	}

	finishedAt := database.Now()
	duration := int(time.Since(started).Milliseconds())
	summary := result.Summary
	if summary == nil {
		summary = map[string]any{}
	}
	summary = RedactMap(summary)

	retryFailure := transientFailure(task.PluginCode, result)
	taskStatus := runStatus
	var availableAt any
	maxAttempts := task.MaxAttempts
	if retryFailure != nil {
		taskStatus = "retrying"
		delay := retryDelaySeconds(task.Attempts)
		availableAt = database.Ts(time.Now().Add(time.Duration(delay) * time.Second))
		maxAttempts = extendedAttemptLimit(task.Attempts, task.MaxAttempts)
		summary["auto_retry"] = map[string]any{
			"scheduled":    true,
			"available_at": availableAt,
			"reason":       retryFailure["code"],
		}
	}

	var errCode, errMessage any
	if retryFailure != nil {
		errCode = retryFailure["code"]
		errMessage = truncate(str(retryFailure["message"]), 500)
	}

	tx, err := s.store.Write.Beginx()
	if err != nil {
		s.logger.Error("complete tx begin failed", slog.String("error", err.Error()))
		return
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`UPDATE TF_sign_runs SET status=?, total_count=?, success_count=?, already_count=?,
		skipped_count=?, failed_count=?, error_code=?, error_message=?, summary_json=?, finished_at=?, duration_ms=?
		WHERE id=?`, runStatus, counts["total"], counts["success"], counts["already"], counts["skipped"],
		counts["failed"], errCode, errMessage, mustJSON(summary), finishedAt, duration, runID); err != nil {
		return
	}
	var finished any
	if taskStatus != "retrying" {
		finished = finishedAt
	}
	if _, err := tx.Exec(`UPDATE TF_sign_tasks SET status=?, max_attempts=?, total_count=?, success_count=?,
		already_count=?, skipped_count=?, failed_count=?, summary_json=?, last_error_code=?, last_error_message=?,
		available_at=?, locked_at=NULL, locked_by=NULL, finished_at=?, updated_at=? WHERE id=?`,
		taskStatus, maxAttempts, counts["total"], counts["success"], counts["already"], counts["skipped"],
		counts["failed"], mustJSON(summary), errCode, errMessage, availableAt, finished, finishedAt, task.ID); err != nil {
		return
	}

	accountErrCode := errCode
	if accountErrCode == nil && runStatus == "failed" {
		accountErrCode = "PLUGIN_EXECUTION_FAILED"
	}
	profileJSON := any(nil)
	if len(result.ProfilePatch) > 0 {
		var acc model.PluginAccount
		if err := tx.Get(&acc, `SELECT * FROM TF_plugin_accounts WHERE id=?`, task.AccountID); err == nil {
			merged := mergeMaps(unmarshalMap(acc.ProfileJSON), result.ProfilePatch)
			profileJSON = mustJSON(merged)
		}
	}
	if _, err := tx.Exec(`UPDATE TF_plugin_accounts SET last_run_at=?, last_error_code=?, last_error_message=?,
		profile_json=COALESCE(?, profile_json), updated_at=? WHERE id=?`,
		finishedAt, accountErrCode, errMessage, profileJSON, finishedAt, task.AccountID); err != nil {
		return
	}
	if err := tx.Commit(); err != nil {
		s.logger.Error("complete commit failed", slog.String("error", err.Error()))
		return
	}

	if len(result.Credentials) > 0 {
		if err := s.accounts.MergeRuntimeCredentials(task.AccountID, result.Credentials); err != nil {
			s.logger.Warn("merge refreshed credentials failed", slog.String("error", err.Error()))
		}
	}

	if s.audit != nil {
		s.audit.Record(task.UserID, "sign.task.finished", "sign_task", task.TaskNo, map[string]any{
			"status": taskStatus, "failed": counts["failed"], "total": counts["total"],
		})
	}
	_ = plugin
}

func (s *TaskService) failTask(task *model.SignTask, runID int64, execErr error, started time.Time) {
	code := "PLUGIN_EXECUTION_FAILED"
	message := execErr.Error()
	var apiErr *respond.APIError
	var signErr *sign.Error
	switch {
	case errors.As(execErr, &apiErr):
		code = apiErr.Code
		message = apiErr.Message
	case errors.As(execErr, &signErr):
		code = signErr.Code
		message = signErr.Message
	}
	message = RedactString(truncate(message, 500))
	credentialExpired := code == "PLUGIN_CREDENTIAL_EXPIRED" || code == "PLUGIN_CREDENTIAL_INVALID"
	continuous := task.PluginCode == "tieba" && code == "UPSTREAM_REQUEST_FAILED"
	ordinary := task.PluginCode != "tieba" && task.Attempts < task.MaxAttempts
	retryable := !credentialExpired && (continuous || ordinary)

	nextStatus := "failed"
	var availableAt any
	if retryable {
		nextStatus = "retrying"
		availableAt = database.Ts(time.Now().Add(time.Duration(retryDelaySeconds(task.Attempts)) * time.Second))
	}
	maxAttempts := task.MaxAttempts
	if continuous {
		maxAttempts = extendedAttemptLimit(task.Attempts, task.MaxAttempts)
	}
	finishedAt := database.Now()
	duration := int(time.Since(started).Milliseconds())

	tx, err := s.store.Write.Beginx()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	if runID > 0 {
		_, _ = tx.Exec(`UPDATE TF_sign_runs SET status='failed', failed_count=1, error_code=?, error_message=?,
			exception_class=?, finished_at=?, duration_ms=? WHERE id=?`,
			code, message, reflectType(execErr), finishedAt, duration, runID)
	}
	var finished any
	if nextStatus != "retrying" {
		finished = finishedAt
	}
	_, _ = tx.Exec(`UPDATE TF_sign_tasks SET status=?, max_attempts=?, failed_count=MAX(failed_count,1),
		last_error_code=?, last_error_message=?, available_at=?, locked_at=NULL, locked_by=NULL,
		finished_at=?, updated_at=? WHERE id=?`,
		nextStatus, maxAttempts, code, message, availableAt, finished, finishedAt, task.ID)

	accountErr := any(code)
	if !credentialExpired && nextStatus == "retrying" {
		accountErr = nil
	}
	_, _ = tx.Exec(`UPDATE TF_plugin_accounts SET last_run_at=?, last_error_code=?, last_error_message=?,
		status=CASE WHEN ? THEN 'credential_expired' ELSE status END, updated_at=? WHERE id=?`,
		finishedAt, accountErr, message, credentialExpired, finishedAt, task.AccountID)
	if err := tx.Commit(); err != nil {
		s.logger.Error("fail task commit failed", slog.String("error", err.Error()))
	}
}

// RecoverStale 处理 Worker 崩溃后遗留的 running 任务。
func (s *TaskService) RecoverStale(timeoutSeconds, limit int) int {
	if timeoutSeconds < 300 {
		timeoutSeconds = 300
	}
	if limit <= 0 {
		limit = 100
	}
	cutoff := database.Ts(time.Now().Add(-time.Duration(timeoutSeconds) * time.Second))
	rows := []model.SignTask{}
	if err := s.store.Read.Select(&rows, `SELECT * FROM TF_sign_tasks
		WHERE status='running' AND queue_name <> 'bilibili_live' AND locked_at IS NOT NULL AND locked_at < ?
		ORDER BY locked_at LIMIT ?`, cutoff, limit); err != nil {
		return 0
	}
	recovered := 0
	for i := range rows {
		task := &rows[i]
		continuous := task.PluginCode == "tieba" && task.LastErrorCode != nil && *task.LastErrorCode == "UPSTREAM_REQUEST_FAILED"
		ordinary := task.PluginCode != "tieba" && task.Attempts < task.MaxAttempts
		failed := !continuous && !ordinary
		now := database.Now()
		status := "retrying"
		if failed {
			status = "failed"
		}
		maxAttempts := task.MaxAttempts
		if continuous {
			maxAttempts = extendedAttemptLimit(task.Attempts, task.MaxAttempts)
		}
		var availableAt, finished any
		if !failed {
			availableAt = now
		} else {
			finished = now
		}
		res, err := s.store.Write.Exec(`UPDATE TF_sign_tasks SET status=?, max_attempts=?, available_at=?,
			locked_at=NULL, locked_by=NULL, last_error_code='WORKER_TIMEOUT',
			last_error_message='执行进程超时或已退出', finished_at=?, updated_at=?
			WHERE id=? AND status='running' AND locked_at=?`,
			status, maxAttempts, availableAt, finished, now, task.ID, task.LockedAt)
		if err != nil {
			continue
		}
		if affected, _ := res.RowsAffected(); affected != 1 {
			continue
		}
		_, _ = s.store.Write.Exec(`UPDATE TF_sign_runs SET status='failed', failed_count=MAX(failed_count,1),
			error_code='WORKER_TIMEOUT', error_message='执行进程超时或已退出', finished_at=? 
			WHERE task_id=? AND status='running'`, now, task.ID)
		recovered++
	}
	return recovered
}

// ---------- 记录写入 ----------

type progressReporter struct {
	svc    *TaskService
	task   *model.SignTask
	runID  int64
	used   bool
	mu     chan struct{}
	totals map[string]int
	seq    int
}

func (p *progressReporter) push(rec sign.Record) {
	if p.totals == nil {
		p.totals = map[string]int{"total": 0, "success": 0, "already": 0, "skipped": 0, "failed": 0}
	}
	p.used = true
	p.seq++
	p.svc.saveRecord(p.task, p.runID, rec, p.seq, p.totals)
}

func (p *progressReporter) counts() map[string]int {
	if p.totals == nil {
		return map[string]int{"total": 0, "success": 0, "already": 0, "skipped": 0, "failed": 0}
	}
	return p.totals
}

func (s *TaskService) saveRecord(task *model.SignTask, runID int64, rec sign.Record, sequence int, counts map[string]int) {
	status := rec.Status
	if status == "" {
		status = "succeeded"
	}
	counts["total"]++
	switch status {
	case "succeeded":
		counts["success"]++
	case "already_done":
		counts["already"]++
	case "skipped":
		counts["skipped"]++
	case "failed":
		counts["failed"]++
	}
	now := database.Now()
	_, err := s.store.Write.Exec(`INSERT INTO TF_sign_records
		(task_id, run_id, user_id, account_id, plugin_code, record_key, sequence_no, schema_version, action,
		 target_type, target_id, target_name, status, result_code, message, reward_json, metrics_json,
		 started_at, finished_at, created_at)
		VALUES(?,?,?,?,?,?,?,1,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(run_id, record_key) DO UPDATE SET
			status=excluded.status, result_code=excluded.result_code, message=excluded.message,
			reward_json=excluded.reward_json, metrics_json=excluded.metrics_json, finished_at=excluded.finished_at`,
		task.ID, runID, task.UserID, task.AccountID, task.PluginCode, rec.Key, sequence, rec.Action,
		nullableString(rec.TargetType), nullableString(rec.TargetID), nullableString(rec.TargetName),
		status, nullableString(rec.Code), nullableString(truncate(rec.Message, 500)),
		jsonOrNil(rec.Rewards), jsonOrNil(rec.Metrics), now, now, now)
	if err != nil {
		s.logger.Warn("save record failed", slog.String("error", err.Error()), slog.String("key", rec.Key))
	}
	_, _ = s.store.Write.Exec(`UPDATE TF_sign_tasks SET total_count=?, success_count=?, already_count=?,
		skipped_count=?, failed_count=?, updated_at=? WHERE id=?`,
		counts["total"], counts["success"], counts["already"], counts["skipped"], counts["failed"], now, task.ID)
}

// ---------- 展示 ----------

func (s *TaskService) present(task *model.SignTask) map[string]any {
	status := task.Status
	if status == "partial" {
		status = "completed"
	}
	var lastError any
	if task.LastErrorCode != nil {
		lastError = map[string]any{"code": *task.LastErrorCode, "message": deref(task.LastErrorMessage)}
	}
	return map[string]any{
		"task_no":       task.TaskNo,
		"account_id":    task.AccountID,
		"plugin_code":   task.PluginCode,
		"action":        task.Action,
		"trigger_type":  task.TriggerType,
		"status":        status,
		"schedule_date": task.ScheduleDate,
		"attempts":      task.Attempts,
		"max_attempts":  task.MaxAttempts,
		"counts": map[string]any{
			"total": task.TotalCount, "success": task.SuccessCount,
			"already": task.AlreadyCount, "skipped": task.SkippedCount, "failed": task.FailedCount,
		},
		"summary":      unmarshalMap(task.SummaryJSON),
		"last_error":   lastError,
		"scheduled_at": task.ScheduledAt,
		"started_at":   task.StartedAt,
		"finished_at":  task.FinishedAt,
		"created_at":   task.CreatedAt,
		"updated_at":   task.UpdatedAt,
	}
}

func (s *TaskService) findOwned(userID int64, taskNo string) (*model.SignTask, error) {
	var task model.SignTask
	err := s.store.Read.Get(&task, `SELECT * FROM TF_sign_tasks WHERE task_no=? AND user_id=?`, taskNo, userID)
	if err != nil {
		return nil, respond.NewError("SIGN_TASK_NOT_FOUND", "签到任务不存在", 404)
	}
	return &task, nil
}

func (s *TaskService) FindOwnedForAdmin(taskNo string) (*model.SignTask, error) {
	var task model.SignTask
	if err := s.store.Read.Get(&task, `SELECT * FROM TF_sign_tasks WHERE task_no=?`, taskNo); err != nil {
		return nil, respond.NewError("SIGN_TASK_NOT_FOUND", "签到任务不存在", 404)
	}
	return &task, nil
}

// ---------- 重试策略 ----------

func transientFailure(pluginCode string, result *sign.Result) map[string]any {
	if pluginCode != "tieba" || result == nil {
		return nil
	}
	for _, rec := range result.Records {
		if rec.Status == "failed" && rec.Code == "UPSTREAM_REQUEST_FAILED" {
			msg := strings.TrimSpace(rec.Message)
			if msg == "" {
				msg = "第三方平台请求失败，稍后自动补签"
			}
			return map[string]any{"code": rec.Code, "message": msg}
		}
	}
	return nil
}

func retryDelaySeconds(attempts int) int {
	delay := 60 * maxInt(1, attempts)
	if delay < 60 {
		delay = 60
	}
	if delay > 900 {
		delay = 900
	}
	return delay
}

func extendedAttemptLimit(attempts, current int) int {
	next := attempts + 1
	if next < current {
		next = current
	}
	if next > 65535 {
		next = 65535
	}
	return next
}

// ---------- 分页 ----------

func paginate(filters map[string]any, defaultPerPage int) (page, perPage, offset int) {
	page = asInt(filters["page"])
	if page < 1 {
		page = 1
	}
	perPage = asInt(filters["per_page"])
	if perPage == 0 {
		perPage = asInt(filters["limit"])
	}
	if perPage <= 0 {
		perPage = defaultPerPage
	}
	perPage = clampInt(perPage, 1, 100)
	offset = (page - 1) * perPage
	return
}

func pageResult(items []map[string]any, total, page, perPage int) map[string]any {
	totalPages := (total + perPage - 1) / perPage
	if totalPages < 1 {
		totalPages = 1
	}
	return map[string]any{
		"items":       items,
		"total":       total,
		"page":        page,
		"per_page":    perPage,
		"total_pages": totalPages,
		"limit":       perPage,
		"offset":      (page - 1) * perPage,
	}
}

func parseJSONText(v any) any {
	switch t := v.(type) {
	case string:
		return parseJSONTextDefault(t)
	case []byte:
		return parseJSONTextDefault(string(t))
	default:
		return v
	}
}

func parseJSONTextDefault(v any) any {
	s, ok := v.(string)
	if !ok {
		return map[string]any{}
	}
	out := map[string]any{}
	if err := unmarshalInto([]byte(s), &out); err != nil {
		return map[string]any{}
	}
	return out
}

func escapeLike(v string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(v)
}

func unique(list []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, v := range list {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func reflectType(v any) string {
	if v == nil {
		return ""
	}
	return typeName(v)
}
