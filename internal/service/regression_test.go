package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tfcloud/internal/config"
	"tfcloud/internal/database"
	"tfcloud/internal/model"
	"tfcloud/internal/sign"
)

func newTestStore(t *testing.T) *database.Store {
	t.Helper()
	store, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

func insertTestUser(t *testing.T, store *database.Store) int64 {
	t.Helper()
	now := database.Now()
	res, err := store.Write.Exec(`INSERT INTO TF_users(username, username_norm, role, status, created_at, updated_at)
		VALUES('u1','u1','user','active',?,?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func insertTestAccount(t *testing.T, store *database.Store, userID int64) int64 {
	t.Helper()
	now := database.Now()
	res, err := store.Write.Exec(`INSERT INTO TF_plugin_accounts(user_id, plugin_code, status, created_at, updated_at)
		VALUES(?,'tieba','active',?,?)`, userID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func insertTestTask(t *testing.T, store *database.Store, userID, accountID int64, taskNo, queue string) int64 {
	t.Helper()
	now := database.Now()
	res, err := store.Write.Exec(`INSERT INTO TF_sign_tasks
		(task_no, user_id, account_id, plugin_code, queue_name, action, trigger_type, schedule_date,
		 status, available_at, created_at, updated_at)
		VALUES(?,?,?,'tieba',?,'daily_sign','schedule',?, 'pending', ?, ?, ?)`,
		taskNo, userID, accountID, queue, now[:10], now, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestClaimRoutesByQueue(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	accountID := insertTestAccount(t, store, userID)
	defaultID := insertTestTask(t, store, userID, accountID, "task-default", QueueDefault)
	liveID := insertTestTask(t, store, userID, accountID, "task-live", QueueBilibiliLive)

	svc := &TaskService{store: store}

	task, ok := svc.Claim("worker-sign", QueueDefault)
	if !ok || task.ID != defaultID {
		t.Fatalf("default worker 应领取 default 任务，实际 ok=%v id=%v", ok, task)
	}
	task, ok = svc.Claim("worker-live", QueueBilibiliLive)
	if !ok || task.ID != liveID {
		t.Fatalf("live worker 应领取 bilibili_live 任务，实际 ok=%v id=%v", ok, task)
	}
	if _, ok := svc.Claim("worker-live", QueueBilibiliLive); ok {
		t.Fatal("没有剩余直播任务时不应领取到其他队列任务")
	}
}

func TestDeleteUserRemovesOwnedData(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	accountID := insertTestAccount(t, store, userID)
	taskID := insertTestTask(t, store, userID, accountID, "task-delete", QueueDefault)
	now := database.Now()
	runRes, err := store.Write.Exec(`INSERT INTO TF_sign_runs(run_no, task_id, attempt_no, plugin_code, started_at, created_at)
		VALUES('run-1',?,1,'tieba',?,?)`, taskID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := runRes.LastInsertId()
	if _, err := store.Write.Exec(`INSERT INTO TF_sign_records
		(task_id, run_id, user_id, account_id, plugin_code, record_key, action, status, created_at)
		VALUES(?,?,?,?,'tieba','record-1','daily_sign','succeeded',?)`, taskID, runID, userID, accountID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write.Exec(`INSERT INTO TF_bilibili_live_sessions
		(session_no, task_id, run_id, user_id, account_id, expires_at, started_at, created_at, updated_at)
		VALUES('session-1',?,?,?,?,?,?,?,?)`, taskID, runID, userID, accountID, now, now, now, now); err != nil {
		t.Fatal(err)
	}

	admin := NewAdmin(store, &config.Config{}, nil, nil, nil, NewAudit(store), nil, nil, nil)
	if err := admin.DeleteUser(userID, 999); err != nil {
		t.Fatalf("删除用户失败: %v", err)
	}

	for _, table := range []string{"TF_users", "TF_plugin_accounts", "TF_sign_tasks", "TF_sign_runs", "TF_sign_records", "TF_bilibili_live_sessions"} {
		var count int
		if err := store.Read.Get(&count, `SELECT COUNT(1) FROM `+table); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s 应被级联清理，实际剩余 %d 行", table, count)
		}
	}
}

func TestReenableAccountSchedulesNextRun(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	accountID := insertTestAccount(t, store, userID)
	if _, err := store.Write.Exec(`UPDATE TF_plugin_accounts SET status='disabled', next_run_at=NULL WHERE id=?`, accountID); err != nil {
		t.Fatal(err)
	}

	svc := &AccountService{store: store}
	if _, err := svc.Update(userID, accountID, map[string]any{"status": "active"}); err != nil {
		t.Fatalf("启用账号失败: %v", err)
	}

	var nextRun *string
	if err := store.Read.Get(&nextRun, `SELECT next_run_at FROM TF_plugin_accounts WHERE id=?`, accountID); err != nil {
		t.Fatal(err)
	}
	if nextRun == nil || *nextRun == "" {
		t.Fatal("重新启用账号后应重新计算 next_run_at")
	}
}

func TestCancelOnlyAppliesToPendingTasks(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	accountID := insertTestAccount(t, store, userID)
	taskID := insertTestTask(t, store, userID, accountID, "task-cancel", QueueDefault)

	svc := &TaskService{store: store}
	if _, err := store.Write.Exec(`UPDATE TF_sign_tasks SET status='running' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(userID, "task-cancel"); err == nil {
		t.Fatal("running 状态的任务不应允许取消")
	}

	if _, err := store.Write.Exec(`UPDATE TF_sign_tasks SET status='pending' WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Cancel(userID, "task-cancel"); err != nil {
		t.Fatalf("pending 任务应允许取消: %v", err)
	}
}

func TestDispatchDueSkipsDisabledUser(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	accountID := insertTestAccount(t, store, userID)
	now := database.Now()
	if _, err := store.Write.Exec(`UPDATE TF_users SET status='disabled' WHERE id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write.Exec(`UPDATE TF_plugin_accounts SET next_run_at=? WHERE id=?`, now, accountID); err != nil {
		t.Fatal(err)
	}

	svc := &TaskService{store: store}
	if n := svc.DispatchDue(10); n != 0 {
		t.Fatalf("停用用户不应产生调度任务，实际 %d", n)
	}
	var tasks int
	if err := store.Read.Get(&tasks, `SELECT COUNT(1) FROM TF_sign_tasks`); err != nil {
		t.Fatal(err)
	}
	if tasks != 0 {
		t.Fatalf("停用用户不应产生任务，实际 %d 条", tasks)
	}
}

func TestMergeRetryCountsAccumulatesTiebaRetry(t *testing.T) {
	code := "UPSTREAM_REQUEST_FAILED"
	summary := `{"request_failed_date":"` + time.Now().Format("2006-01-02") + `"}`
	task := &model.SignTask{
		PluginCode: "tieba", LastErrorCode: &code, SummaryJSON: &summary,
		TotalCount: 10, SuccessCount: 9, FailedCount: 1,
	}
	merged, ok := mergeRetryCounts(task, map[string]int{"total": 1, "success": 1, "already": 0, "skipped": 0, "failed": 0})
	if !ok {
		t.Fatal("贴吧补签应触发累计计数合并")
	}
	if merged["total"] != 10 || merged["success"] != 10 || merged["failed"] != 0 {
		t.Fatalf("累计结果错误: %#v", merged)
	}
}

func TestQQVerificationContentReadableFromSecrets(t *testing.T) {
	store := newTestStore(t)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{5}, 32))
	cfg := &config.Config{AppKey: "base64:" + key, CredentialKeyVersion: 1}
	settings := NewSettings(store, cfg)

	if _, err := settings.Update("site", map[string]any{"qq_relay_verification_content": "verify-token"}, 0); err != nil {
		t.Fatalf("保存校验内容失败: %v", err)
	}
	public := settings.Group("site")
	if got := str(public["qq_relay_verification_content"]); got != "" {
		t.Fatalf("普通 Group 不应暴露 Secret，实际 %q", got)
	}
	group, err := settings.GroupWithSecrets("site")
	if err != nil {
		t.Fatal(err)
	}
	if got := str(group["qq_relay_verification_content"]); got != "verify-token" {
		t.Fatalf("GroupWithSecrets 应返回校验内容，实际 %q", got)
	}
}

func insertMailTask(t *testing.T, store *database.Store, taskNo, status string) int64 {
	t.Helper()
	now := database.Now()
	res, err := store.Write.Exec(`INSERT INTO TF_mail_tasks(task_no, template_code, subject, status, total_count, created_at, updated_at)
		VALUES(?, 'custom', 'test', ?, 1, ?, ?)`, taskNo, status, now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func insertMailRecord(t *testing.T, store *database.Store, taskID int64, status string, attempts int, updatedAt string) int64 {
	t.Helper()
	res, err := store.Write.Exec(`INSERT INTO TF_mail_records
		(task_id, message_no, recipient, recipient_hash, subject, status, attempts, max_attempts, created_at, updated_at)
		VALUES(?, ?, 'a@example.com', 'hash', 'test', ?, ?, 3, ?, ?)`,
		taskID, fmt.Sprintf("msg-%d-%s-%d", taskID, status, attempts), status, attempts, updatedAt, updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestRefreshTaskStatsMarksAllFailedAsFailed(t *testing.T) {
	store := newTestStore(t)
	taskID := insertMailTask(t, store, "mail-all-failed", "running")
	insertMailRecord(t, store, taskID, "failed", 3, database.Now())

	svc := &MailService{store: store}
	svc.refreshTaskStats()

	var status string
	if err := store.Read.Get(&status, `SELECT status FROM TF_mail_tasks WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("全部失败的任务应标记 failed，实际 %q", status)
	}
}

func TestMailRetryWithoutFailedRecordsKeepsStatus(t *testing.T) {
	store := newTestStore(t)
	taskID := insertMailTask(t, store, "mail-retry", "succeeded")
	insertMailRecord(t, store, taskID, "sent", 1, database.Now())

	svc := &MailService{store: store}
	out, err := svc.Retry("mail-retry")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := out["requeued"].(int64); n != 0 {
		t.Fatalf("没有失败记录时不应重新入队任何邮件，实际 %d", n)
	}
	var status string
	if err := store.Read.Get(&status, `SELECT status FROM TF_mail_tasks WHERE id=?`, taskID); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" {
		t.Fatalf("无失败记录时不应把任务改回 pending，实际 %q", status)
	}
}

func TestRecoverStaleSending(t *testing.T) {
	store := newTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := &MailService{store: store, logger: logger}
	old := database.Ts(time.Now().Add(-time.Hour))

	taskID := insertMailTask(t, store, "mail-stale", "running")
	recordID := insertMailRecord(t, store, taskID, "sending", 1, old)
	if n := svc.RecoverStaleSending(time.Minute, 10); n != 1 {
		t.Fatalf("应恢复 1 条 sending 记录，实际 %d", n)
	}
	var status string
	if err := store.Read.Get(&status, `SELECT status FROM TF_mail_records WHERE id=?`, recordID); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("未达最大次数的记录应回到 pending，实际 %q", status)
	}

	taskID2 := insertMailTask(t, store, "mail-stale-max", "running")
	recordID2 := insertMailRecord(t, store, taskID2, "sending", 3, old)
	if n := svc.RecoverStaleSending(time.Minute, 10); n != 1 {
		t.Fatalf("应恢复 1 条 sending 记录，实际 %d", n)
	}
	if err := store.Read.Get(&status, `SELECT status FROM TF_mail_records WHERE id=?`, recordID2); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("达到最大次数的记录应标记 failed，实际 %q", status)
	}
	var taskStatus string
	if err := store.Read.Get(&taskStatus, `SELECT status FROM TF_mail_tasks WHERE id=?`, taskID2); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "failed" {
		t.Fatalf("全部失败后任务应标记 failed，实际 %q", taskStatus)
	}
}

func TestPresentKeepsPartialStatus(t *testing.T) {
	svc := &TaskService{}
	out := svc.present(&model.SignTask{Status: "partial"})
	if out["status"] != "partial" {
		t.Fatalf("partial 状态应原样返回，实际 %v", out["status"])
	}
}

func TestPublicSiteIncludesCounts(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	accountID := insertTestAccount(t, store, userID)
	now := database.Now()
	if _, err := store.Write.Exec(`INSERT INTO TF_sign_tasks
		(task_no, user_id, account_id, plugin_code, queue_name, action, trigger_type, schedule_date, status, created_at, updated_at)
		VALUES('site-task', ?, ?, 'tieba', 'default', 'daily_sign', 'schedule', ?, 'succeeded', ?, ?)`,
		userID, accountID, now[:10], now, now); err != nil {
		t.Fatal(err)
	}

	settings := NewSettings(store, &config.Config{})
	site := settings.PublicSite()
	if got, _ := site["user_count"].(int); got != 1 {
		t.Fatalf("user_count 应为 1，实际 %v", site["user_count"])
	}
	if got, _ := site["today_signed_accounts"].(int); got != 1 {
		t.Fatalf("today_signed_accounts 应为 1，实际 %v", site["today_signed_accounts"])
	}
}

func TestLiveOutcome(t *testing.T) {
	cases := []struct {
		completed, partial, failed int
		status                     string
		success, failedCount       int
	}{
		{3, 0, 0, "succeeded", 3, 0},
		{2, 1, 0, "partial", 2, 1},
		{2, 0, 1, "partial", 2, 1},
		{0, 2, 1, "partial", 0, 3},
		{0, 0, 2, "failed", 0, 2},
	}
	for _, tc := range cases {
		status, success, failed := liveOutcome(tc.completed, tc.partial, tc.failed)
		if status != tc.status || success != tc.success || failed != tc.failedCount {
			t.Fatalf("liveOutcome(%d,%d,%d) = (%s,%d,%d)，期望 (%s,%d,%d)",
				tc.completed, tc.partial, tc.failed, status, success, failed, tc.status, tc.success, tc.failedCount)
		}
	}
}

func TestLiveSettingsAndDuration(t *testing.T) {
	settings := map[string]any{
		"live_heartbeat_count":     float64(123),
		"live_max_rooms":           float64(7),
		"live_max_failures":        float64(3),
		"live_skip_level_20_medal": true,
	}
	if got := liveTargetHeartbeats(settings); got != 123 {
		t.Fatalf("心跳数应读取 live_heartbeat_count，实际 %d", got)
	}
	if got := liveTargetHeartbeats(map[string]any{"live_target_heartbeats": "42"}); got != 42 {
		t.Fatalf("应兼容旧 live_target_heartbeats，实际 %d", got)
	}
	if got := liveMaxRooms(settings); got != 7 {
		t.Fatalf("直播房间数错误: %d", got)
	}
	if got := liveMaxFailures(settings); got != 3 {
		t.Fatalf("失败阈值错误: %d", got)
	}
	if got := liveSessionDuration(5, 70); got < 5*time.Hour {
		t.Fatalf("默认 5 房间 x70 次心跳的会话时长过短: %s", got)
	}
	if got := liveSessionDuration(100, 180); got != liveMaxDuration {
		t.Fatalf("会话时长应封顶 %s，实际 %s", liveMaxDuration, got)
	}
	if got := liveSessionDuration(1, 1); got != liveMinDuration {
		t.Fatalf("会话时长应有下限 %s，实际 %s", liveMinDuration, got)
	}
}

func TestMergeTaskStatusKeepsFailuresVisible(t *testing.T) {
	counts := map[string]int{"total": 5, "success": 0, "already": 0, "skipped": 0, "failed": 5}
	if got := mergeTaskStatus("succeeded", counts, true); got != "failed" {
		t.Fatalf("仍有失败且无成功时应为 failed，实际 %s", got)
	}
	counts["success"] = 1
	if got := mergeTaskStatus("succeeded", counts, true); got != "partial" {
		t.Fatalf("仍有失败但有成功时应为 partial，实际 %s", got)
	}
	if got := mergeTaskStatus("succeeded", counts, false); got != "succeeded" {
		t.Fatalf("非补签叠加不应改写状态，实际 %s", got)
	}
}

func TestCredentialFailureFromRecords(t *testing.T) {
	code, _ := credentialFailureFromRecords([]sign.Record{
		{Status: "succeeded"},
		{Status: "failed", Code: "PLUGIN_CREDENTIAL_EXPIRED", Message: "expired"},
	})
	if code != "PLUGIN_CREDENTIAL_EXPIRED" {
		t.Fatalf("应识别凭据失效记录，实际 %q", code)
	}
	if code, _ := credentialFailureFromRecords([]sign.Record{{Status: "failed", Code: "X"}}); code != "" {
		t.Fatalf("普通失败不应识别为凭据失效: %q", code)
	}
}

func TestRecoverStaleIncludesLiveTasks(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	accountID := insertTestAccount(t, store, userID)
	now := database.Now()
	old := database.Ts(time.Now().Add(-2 * time.Hour))
	if _, err := store.Write.Exec(`INSERT INTO TF_sign_tasks
		(task_no, user_id, account_id, plugin_code, queue_name, action, trigger_type, status,
		 attempts, max_attempts, locked_at, created_at, updated_at)
		VALUES('live-stale', ?, ?, 'bilibili', 'bilibili_live', 'live_fans_medal', 'schedule', 'running', 1, 24, ?, ?, ?)`,
		userID, accountID, old, now, now); err != nil {
		t.Fatal(err)
	}

	svc := &TaskService{store: store}
	if n := svc.RecoverStale(300, 10); n != 1 {
		t.Fatalf("应回收 1 个过期直播任务，实际 %d", n)
	}
	var status string
	if err := store.Read.Get(&status, `SELECT status FROM TF_sign_tasks WHERE task_no='live-stale'`); err != nil {
		t.Fatal(err)
	}
	if status != "retrying" {
		t.Fatalf("过期直播任务应回到 retrying，实际 %q", status)
	}
}

func TestValidateSettingsRequiresFixedTime(t *testing.T) {
	if err := validateSettings("tieba", map[string]any{"schedule_mode": "fixed"}); err == nil {
		t.Fatal("fixed 模式缺少 schedule_time 时应校验失败")
	}
	if err := validateSettings("tieba", map[string]any{"schedule_mode": "fixed", "schedule_time": "07:30"}); err != nil {
		t.Fatalf("合法 fixed 计划不应报错: %v", err)
	}
	if err := validateSettings("tieba", map[string]any{"schedule_mode": "auto"}); err != nil {
		t.Fatalf("auto 模式不需要 schedule_time: %v", err)
	}
}

func TestDeleteAccountCancelsPendingTasks(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	accountID := insertTestAccount(t, store, userID)
	insertTestTask(t, store, userID, accountID, "account-delete-task", QueueDefault)

	svc := &AccountService{store: store}
	if err := svc.Delete(userID, accountID); err != nil {
		t.Fatalf("删除账号失败: %v", err)
	}
	var status string
	if err := store.Read.Get(&status, `SELECT status FROM TF_sign_tasks WHERE task_no='account-delete-task'`); err != nil {
		t.Fatal(err)
	}
	if status != "cancelled" {
		t.Fatalf("删除账号后待执行任务应取消，实际 %q", status)
	}
}

func TestPageResultReportsActualOffset(t *testing.T) {
	out := pageResult([]map[string]any{}, 100, 2, 20, 30)
	if out["offset"] != 30 {
		t.Fatalf("分页 offset 应为 30，实际 %v", out["offset"])
	}
	if out["page"] != 2 {
		t.Fatalf("分页 page 应为 2，实际 %v", out["page"])
	}
}

type failingPlugin struct{}

func (failingPlugin) Metadata() sign.Metadata { return sign.Metadata{Code: "tieba", Name: "tieba"} }
func (failingPlugin) CredentialRules() []sign.CredentialRule {
	return []sign.CredentialRule{{Key: "bduss", Required: true}}
}
func (failingPlugin) SupportedActions() []string { return []string{"daily_sign"} }
func (failingPlugin) ValidateAccount(map[string]any) (*sign.Profile, error) {
	return nil, sign.Fail("PLUGIN_CREDENTIAL_INVALID", "凭据无效", 422)
}
func (failingPlugin) Execute(context.Context, *sign.Context) (*sign.Result, error) {
	return nil, nil
}

type refreshingPlugin struct{}

func (refreshingPlugin) Metadata() sign.Metadata { return sign.Metadata{Code: "tieba", Name: "tieba"} }
func (refreshingPlugin) CredentialRules() []sign.CredentialRule {
	return []sign.CredentialRule{{Key: "bduss", Required: true}}
}
func (refreshingPlugin) SupportedActions() []string { return []string{"daily_sign"} }
func (refreshingPlugin) ValidateAccount(creds map[string]any) (*sign.Profile, error) {
	profile, _, err := (refreshingPlugin{}).ValidateAccountWithRefresh(creds)
	return profile, err
}
func (refreshingPlugin) ValidateAccountWithRefresh(map[string]any) (*sign.Profile, map[string]any, error) {
	return &sign.Profile{ExternalID: "ext-1", DisplayName: "刷新账号"}, map[string]any{"access_token": "refreshed-token"}, nil
}
func (refreshingPlugin) Execute(context.Context, *sign.Context) (*sign.Result, error) {
	return nil, nil
}

func TestAccountUpdateIsAtomicOnCredentialFailure(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	accountID := insertTestAccount(t, store, userID)
	svc := &AccountService{store: store, registry: sign.NewRegistry(failingPlugin{})}

	_, err := svc.Update(userID, accountID, map[string]any{
		"display_name": "new-name",
		"status":       "disabled",
		"settings":     map[string]any{"schedule_mode": "auto"},
		"credentials":  map[string]any{"bduss": "anything"},
	})
	if err == nil {
		t.Fatal("凭据校验失败时应返回错误")
	}

	var row struct {
		DisplayName string  `db:"display_name"`
		Status      string  `db:"status"`
		Settings    *string `db:"settings_json"`
	}
	if err := store.Read.Get(&row, `SELECT display_name, status, settings_json FROM TF_plugin_accounts WHERE id=?`, accountID); err != nil {
		t.Fatal(err)
	}
	if row.DisplayName != "" || row.Status != "active" || row.Settings != nil {
		t.Fatalf("凭据失败时不应部分写入: %#v", row)
	}
}

func TestCreatePersistsRefreshedCredentials(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
	cfg := &config.Config{AppKey: "base64:" + key, FingerprintKey: "fp", CredentialKeyVersion: 1}
	svc := NewAccount(store, cfg, nil, sign.NewRegistry(refreshingPlugin{}))

	out, err := svc.Create(userID, map[string]any{
		"plugin_code": "tieba",
		"credentials": map[string]any{"bduss": "raw"},
	})
	if err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	accountID, _ := out["id"].(int64)
	creds, err := svc.DecryptedCredential(&model.PluginAccount{ID: accountID, PluginCode: "tieba"})
	if err != nil {
		t.Fatal(err)
	}
	if creds["access_token"] != "refreshed-token" {
		t.Fatalf("刷新后的令牌应写入凭据，实际 %#v", creds)
	}
}

func TestRetryClearsPreviousResultFields(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	accountID := insertTestAccount(t, store, userID)
	now := database.Now()
	if _, err := store.Write.Exec(`INSERT INTO TF_sign_tasks
		(task_no, user_id, account_id, plugin_code, queue_name, action, trigger_type, schedule_date, status,
		 attempts, max_attempts, finished_at, last_error_code, last_error_message, created_at, updated_at)
		VALUES('retry-task', ?, ?, 'tieba', 'default', 'daily_sign', 'schedule', ?, 'failed', 1, 24, ?, ?, ?, ?, ?)`,
		userID, accountID, now[:10], now, "UPSTREAM_REQUEST_FAILED", "boom", now, now); err != nil {
		t.Fatal(err)
	}

	svc := &TaskService{store: store}
	if _, err := svc.Retry(userID, "retry-task"); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	var row struct {
		Status     string  `db:"status"`
		FinishedAt *string `db:"finished_at"`
		ErrorCode  *string `db:"last_error_code"`
		ErrorMsg   *string `db:"last_error_message"`
	}
	if err := store.Read.Get(&row, `SELECT status, finished_at, last_error_code, last_error_message
		FROM TF_sign_tasks WHERE task_no='retry-task'`); err != nil {
		t.Fatal(err)
	}
	if row.Status != "pending" || row.FinishedAt != nil || row.ErrorCode != nil || row.ErrorMsg != nil {
		t.Fatalf("重试应清理上一次结果字段: %#v", row)
	}
}

func TestUserSearchEscapesWildcards(t *testing.T) {
	store := newTestStore(t)
	now := database.Now()
	for _, name := range []string{"100%user", "100xuser"} {
		if _, err := store.Write.Exec(`INSERT INTO TF_users(username, username_norm, role, status, created_at, updated_at)
			VALUES(?, ?, 'user', 'active', ?, ?)`, name, NormalizeIdentity(name), now, now); err != nil {
			t.Fatal(err)
		}
	}

	svc := &AdminService{store: store}
	out, err := svc.Users(map[string]any{"keyword": "100%"})
	if err != nil {
		t.Fatal(err)
	}
	if total, _ := out["total"].(int); total != 1 {
		t.Fatalf("百分号应按字面量匹配，实际命中 %d 条", total)
	}
}

func TestDailySummaryQueuesOncePerDay(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	accountID := insertTestAccount(t, store, userID)
	insertTestTask(t, store, userID, accountID, "daily-summary-task", QueueDefault)
	if _, err := store.Write.Exec(`UPDATE TF_users SET email='u1@example.com', email_norm='u1@example.com',
		daily_sign_email=1 WHERE id=?`, userID); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	settings := NewSettings(store, cfg)
	if _, err := settings.Update("mail", map[string]any{
		"enabled": true, "daily_summary_hour": time.Now().Hour(),
	}, userID); err != nil {
		t.Fatal(err)
	}
	mail := NewMail(store, cfg, settings, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if n := mail.SendDailySummaries(); n != 1 {
		t.Fatalf("首次应排队一封汇总邮件，实际 %d", n)
	}
	if n := mail.SendDailySummaries(); n != 0 {
		t.Fatalf("同一天不应重复排队，实际 %d", n)
	}
	var tasks, events int
	if err := store.Read.Get(&tasks, `SELECT COUNT(1) FROM TF_mail_tasks WHERE template_code='daily_sign_summary'`); err != nil {
		t.Fatal(err)
	}
	if err := store.Read.Get(&events, `SELECT COUNT(1) FROM TF_notification_events WHERE event_type='daily_sign_summary'`); err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || events != 1 {
		t.Fatalf("汇总任务与事件应各有一条，实际 tasks=%d events=%d", tasks, events)
	}
}

func TestAuditRecordKeepsRequestID(t *testing.T) {
	store := newTestStore(t)
	audit := NewAudit(store)
	audit.Record(1, "test.action", "test", "1", nil, "req-123")
	var requestID *string
	if err := store.Read.Get(&requestID, `SELECT request_id FROM TF_audit_logs WHERE action='test.action'`); err != nil {
		t.Fatal(err)
	}
	if requestID == nil || *requestID != "req-123" {
		t.Fatalf("审计请求 ID 未保存: %#v", requestID)
	}
}

func TestMailBodyForStorageKeepsTextOnlyBody(t *testing.T) {
	body := mailBodyForStorage("", "第一行\n<script>alert(1)</script>")
	if body == "" {
		t.Fatal("纯文本邮件也必须生成可发送的正文")
	}
	if !strings.Contains(body, "第一行") || strings.Contains(body, "<script>") {
		t.Fatalf("纯文本正文应被保留并转义，实际 %q", body)
	}
	if got := mailBodyForStorage("<b>hi</b>", "ignored"); got != "<b>hi</b>" {
		t.Fatalf("HTML 正文应原样保留，实际 %q", got)
	}
}

func TestAdminDisableRevokesSessions(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	now := database.Now()
	if _, err := store.Write.Exec(`INSERT INTO TF_sessions(user_id, token_hash, expires_at, created_at)
		VALUES(?, 'token-1', ?, ?)`, userID, database.Ts(time.Now().Add(time.Hour)), now); err != nil {
		t.Fatal(err)
	}
	svc := NewAdmin(store, &config.Config{}, nil, nil, nil, NewAudit(store), nil, nil, nil)
	if _, err := svc.UpdateUser(userID, map[string]any{"status": "disabled"}, 999); err != nil {
		t.Fatalf("停用用户失败: %v", err)
	}
	var revoked *string
	if err := store.Read.Get(&revoked, `SELECT revoked_at FROM TF_sessions WHERE user_id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if revoked == nil {
		t.Fatal("停用用户后应撤销现有会话")
	}
}

func TestRevokeUserSessionsReturnsCount(t *testing.T) {
	store := newTestStore(t)
	userID := insertTestUser(t, store)
	now := database.Now()
	for _, token := range []string{"t1", "t2"} {
		if _, err := store.Write.Exec(`INSERT INTO TF_sessions(user_id, token_hash, expires_at, created_at)
			VALUES(?, ?, ?, ?)`, userID, token, database.Ts(time.Now().Add(time.Hour)), now); err != nil {
			t.Fatal(err)
		}
	}
	auth := NewAuth(store, &config.Config{}, nil, nil)
	n, err := auth.RevokeUserSessions(userID)
	if err != nil || n != 2 {
		t.Fatalf("应返回撤销数量 2，实际 %d, %v", n, err)
	}
}

func insertTestAdmin(t *testing.T, store *database.Store, name string) int64 {
	t.Helper()
	now := database.Now()
	res, err := store.Write.Exec(`INSERT INTO TF_users(username, username_norm, role, status, created_at, updated_at)
		VALUES(?, ?, 'admin', 'active', ?, ?)`, name, NormalizeIdentity(name), now, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestCannotDemoteLastActiveAdmin(t *testing.T) {
	store := newTestStore(t)
	first := insertTestAdmin(t, store, "admin-one")
	second := insertTestAdmin(t, store, "admin-two")
	svc := NewAdmin(store, &config.Config{}, nil, nil, nil, NewAudit(store), nil, nil, nil)

	if _, err := svc.UpdateUser(first, map[string]any{"role": "user"}, second); err != nil {
		t.Fatalf("降级非最后一个管理员应成功: %v", err)
	}
	if _, err := svc.UpdateUser(second, map[string]any{"role": "user"}, first); err == nil {
		t.Fatal("不能降级最后一个活跃管理员")
	}
	var role, status string
	if err := store.Read.Get(&role, `SELECT role FROM TF_users WHERE id=?`, second); err != nil {
		t.Fatal(err)
	}
	if err := store.Read.Get(&status, `SELECT status FROM TF_users WHERE id=?`, second); err != nil {
		t.Fatal(err)
	}
	if role != "admin" || status != "active" {
		t.Fatalf("最后一个管理员应保持 admin/active，实际 %s/%s", role, status)
	}
}

func TestPublicSiteReportsMailEnabled(t *testing.T) {
	store := newTestStore(t)
	settings := NewSettings(store, &config.Config{})
	if _, err := settings.Update("mail", map[string]any{"enabled": true}, 0); err != nil {
		t.Fatalf("启用邮件失败: %v", err)
	}
	site := settings.PublicSite()
	if site["mail_enabled"] != true {
		t.Fatalf("PublicSite 应输出 mail_enabled，实际 %v", site["mail_enabled"])
	}
	if offset, _ := site["timezone_offset"].(string); offset == "" {
		t.Fatal("PublicSite 应输出 timezone_offset")
	}
	if today, _ := site["today"].(string); today == "" {
		t.Fatal("PublicSite 应输出 today")
	}
}
