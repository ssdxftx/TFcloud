package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"tfcloud/internal/database"
	"tfcloud/internal/model"
	"tfcloud/internal/plugins"
	"tfcloud/internal/secure"
	"tfcloud/internal/sign"
)

// LiveService 负责 B 站粉丝牌挂机（/xlive 心跳）。
// 与 PHP 版的多表状态机相比，这里用单个 goroutine 串行推进一次会话：
// 发现勋章 → 逐个进入直播间 → 按服务端下发间隔发心跳 → 收尾写记录。
// 会话与房间进度仍然落库，后台可观测，进程重启后由 worker 重新领取。
type LiveService struct {
	store    *database.Store
	accounts *AccountService
	tasks    *TaskService
	http     *sign.HTTP
	logger   *slog.Logger
	signer   plugins.LiveHeartbeatSigner
}

func NewLive(store *database.Store, accounts *AccountService, tasks *TaskService, logger *slog.Logger) *LiveService {
	return &LiveService{store: store, accounts: accounts, tasks: tasks, http: sign.NewHTTP(), logger: logger}
}

const (
	liveDefaultHeartbeats = 70
	liveDefaultMaxRooms   = 5
	liveDefaultFailures   = 5
	liveMinDuration       = 40 * time.Minute
	liveMaxDuration       = 8 * time.Hour
	liveHeartbeatInterval = 60 * time.Second
	liveRecoverIdle       = 10 * time.Minute
	liveRecoverFallback   = 15 * time.Minute
)

func liveTargetHeartbeats(settings map[string]any) int {
	target := asInt(settings["live_heartbeat_count"])
	if target <= 0 {
		target = asInt(settings["live_target_heartbeats"])
	}
	if target <= 0 {
		target = liveDefaultHeartbeats
	}
	return clampInt(target, 1, 180)
}

func liveMaxRooms(settings map[string]any) int {
	rooms := asInt(settings["live_max_rooms"])
	if rooms <= 0 {
		rooms = liveDefaultMaxRooms
	}
	return clampInt(rooms, 1, 50)
}

func liveMaxFailures(settings map[string]any) int {
	failures := asInt(settings["live_max_failures"])
	if failures <= 0 {
		failures = liveDefaultFailures
	}
	return clampInt(failures, 1, 20)
}

// liveSessionDuration 按「每房间目标心跳」估算会话时长，并设置上下限。
// 实际心跳间隔由服务端返回，60 秒是保守估算。
func liveSessionDuration(rooms, target int) time.Duration {
	if rooms < 1 {
		rooms = 1
	}
	if target < 1 {
		target = 1
	}
	duration := time.Duration(rooms*target)*liveHeartbeatInterval + 10*time.Minute
	if duration < liveMinDuration {
		duration = liveMinDuration
	}
	if duration > liveMaxDuration {
		duration = liveMaxDuration
	}
	return duration
}

func (s *LiveService) bilibiliCookie(creds map[string]any) string {
	parts := []string{
		"DedeUserID=" + sign.ValueString(creds["dede_user_id"]),
		"SESSDATA=" + sign.ValueString(creds["sessdata"]),
		"bili_jct=" + sign.ValueString(creds["bili_jct"]),
	}
	for _, key := range []string{"buvid3", "buvid4", "dede_user_id_ckmd5"} {
		if v := strings.TrimSpace(sign.ValueString(creds[key])); v != "" {
			name := key
			if key == "dede_user_id_ckmd5" {
				name = "DedeUserID__ckMd5"
			}
			parts = append(parts, name+"="+v)
		}
	}
	return strings.Join(parts, "; ")
}

type liveMedal struct {
	RoomID     int64
	RUID       int64
	MedalName  string
	MedalLevel int
	RoomName   string
	ParentArea int64
	AreaID     int64
}

func (s *LiveService) discover(ctx context.Context, creds map[string]any) ([]liveMedal, error) {
	mid := sign.ValueString(creds["dede_user_id"])
	resp, err := s.http.Get(ctx, "https://api.live.bilibili.com/xlive/web-ucenter/user/MedalWall", sign.RequestOptions{
		Query:   url.Values{"target_id": {mid}, "page": {"1"}, "page_size": {"50"}},
		Cookie:  s.bilibiliCookie(creds),
		Referer: "https://live.bilibili.com/",
		Headers: map[string]string{"Origin": "https://live.bilibili.com", "User-Agent": "Mozilla/5.0"},
	})
	if err != nil {
		return nil, err
	}
	if liveUpstreamUnavailable(resp) {
		return nil, sign.Fail("UPSTREAM_REQUEST_FAILED", "获取粉丝牌列表失败：上游服务暂时不可用", 502)
	}
	if resp.JSON == nil {
		return nil, sign.Fail("BILIBILI_RESPONSE_INVALID", "获取粉丝牌列表失败：响应格式错误", 502)
	}
	code := sign.ValueInt(resp.JSON["code"])
	if code == -101 {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "哔哩哔哩登录状态已经失效", 422)
	}
	if code != 0 {
		return nil, sign.Fail("BILIBILI_MEDAL_FAILED", firstText(resp.JSON["message"], "获取粉丝牌列表失败"), 502)
	}
	data := sign.Map(resp.JSON["data"])
	list := sign.Slice(data["list"])
	out := make([]liveMedal, 0, len(list))
	for _, item := range list {
		m := sign.Map(item)
		if m == nil {
			continue
		}
		medal := sign.Map(m["medal"])
		roomID := sign.ValueInt(firstNonNil(m["room_id"], m["roomid"]))
		if roomID == 0 && medal != nil {
			roomID = sign.ValueInt(medal["room_id"])
		}
		if roomID == 0 {
			continue
		}
		ruid := sign.ValueInt(firstNonNil(m["target_id"], m["uid"]))
		if ruid == 0 && medal != nil {
			ruid = sign.ValueInt(medal["target_id"])
		}
		out = append(out, liveMedal{
			RoomID:     int64(roomID),
			RUID:       int64(ruid),
			MedalName:  firstText(medal["medal_name"], m["medal_name"]),
			MedalLevel: sign.ValueInt(firstNonNil(m["level"], medal["level"])),
			RoomName:   firstText(m["room_name"], medal["anchor_uname"]),
		})
	}
	if len(out) == 0 {
		return nil, sign.Fail("BILIBILI_MEDAL_EMPTY", "当前账号没有可挂机的粉丝牌", 422)
	}
	return out, nil
}

type roomHeartbeatParams struct {
	SecretKey  string
	SecretRule []int
	Timestamp  int64
	Interval   int
}

// enterRoom 调 x25Kn/E 进入直播间并取回心跳签名参数。
func (s *LiveService) enterRoom(ctx context.Context, creds map[string]any, room liveMedal, device string, sequence int) (*roomHeartbeatParams, error) {
	mid := sign.ValueString(creds["dede_user_id"])
	form := url.Values{
		"id":         {fmt.Sprintf("[%d,%d,0,%d]", room.ParentArea, room.AreaID, room.RoomID)},
		"ruid":       {strconv.FormatInt(room.RUID, 10)},
		"ts":         {strconv.FormatInt(time.Now().UnixMilli(), 10)},
		"is_patch":   {"0"},
		"heart_beat": {"[]"},
		"ua":         {"Mozilla/5.0"},
		"csrf_token": {sign.ValueString(creds["bili_jct"])},
		"csrf":       {sign.ValueString(creds["bili_jct"])},
		"visit_id":   {""},
		"device":     {device},
		"mid":        {mid},
	}
	resp, err := s.http.PostForm(ctx, "https://live-trace.bilibili.com/xlive/data-interface/v1/x25Kn/E", sign.RequestOptions{
		Form:    form,
		Cookie:  s.bilibiliCookie(creds),
		Referer: "https://live.bilibili.com/" + strconv.FormatInt(room.RoomID, 10),
		Headers: map[string]string{"Origin": "https://live.bilibili.com", "User-Agent": "Mozilla/5.0"},
	})
	if err != nil {
		return nil, err
	}
	if liveUpstreamUnavailable(resp) {
		return nil, sign.Fail("UPSTREAM_REQUEST_FAILED", "进入直播间失败：上游服务暂时不可用", 502)
	}
	if resp.JSON == nil {
		return nil, sign.Fail("BILIBILI_RESPONSE_INVALID", "进入直播间失败：响应格式错误", 502)
	}
	code := sign.ValueInt(resp.JSON["code"])
	if code == -101 {
		return nil, sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "哔哩哔哩登录状态已经失效", 422)
	}
	if code != 0 {
		return nil, sign.Fail("BILIBILI_LIVE_ENTER_FAILED", firstText(resp.JSON["message"], "进入直播间失败"), 502)
	}
	data := sign.Map(resp.JSON["data"])
	params := &roomHeartbeatParams{Interval: 60}
	params.SecretKey = sign.ValueString(data["secret_key"])
	params.Timestamp = int64(sign.ValueInt(data["timestamp"]))
	if v := asInt(data["heartbeat_interval"]); v > 0 {
		params.Interval = v
	}
	if rules := sign.Slice(data["secret_rule"]); rules != nil {
		for _, r := range rules {
			params.SecretRule = append(params.SecretRule, sign.ValueInt(r))
		}
	}
	return params, nil
}

func (s *LiveService) heartbeat(ctx context.Context, creds map[string]any, room liveMedal,
	params *roomHeartbeatParams, device string, sequence int) error {
	ts := time.Now().UnixMilli()
	payload := map[string]any{
		"platform":  "web",
		"parent_id": room.ParentArea,
		"area_id":   room.AreaID,
		"seq_id":    sequence,
		"room_id":   room.RoomID,
		"buvid":     sign.ValueString(creds["live_buvid"]),
		"uuid":      strings.TrimPrefix(device, "["),
		"ets":       params.Timestamp,
		"time":      60,
		"ts":        ts,
	}
	raw, _ := json.Marshal(payload)
	signature, err := s.signer.Sign(string(raw), params.SecretRule, params.SecretKey)
	if err != nil {
		return sign.Fail("BILIBILI_LIVE_SIGN_FAILED", err.Error(), 502)
	}
	form := url.Values{
		"s":          {signature},
		"id":         {fmt.Sprintf("[%d,%d,%d,%d]", room.ParentArea, room.AreaID, sequence, room.RoomID)},
		"ets":        {strconv.FormatInt(params.Timestamp, 10)},
		"benchmark":  {params.SecretKey},
		"time":       {"60"},
		"ts":         {strconv.FormatInt(ts, 10)},
		"ua":         {"Mozilla/5.0"},
		"csrf_token": {sign.ValueString(creds["bili_jct"])},
		"csrf":       {sign.ValueString(creds["bili_jct"])},
		"visit_id":   {""},
		"device":     {device},
	}
	resp, err := s.http.PostForm(ctx, "https://live-trace.bilibili.com/xlive/data-interface/v1/x25Kn/X", sign.RequestOptions{
		Form:    form,
		Cookie:  s.bilibiliCookie(creds),
		Referer: "https://live.bilibili.com/" + strconv.FormatInt(room.RoomID, 10),
		Headers: map[string]string{"Origin": "https://live.bilibili.com", "User-Agent": "Mozilla/5.0"},
	})
	if err != nil {
		return err
	}
	if liveUpstreamUnavailable(resp) {
		return sign.Fail("UPSTREAM_REQUEST_FAILED", "心跳失败：上游服务暂时不可用", 502)
	}
	if resp.JSON == nil {
		return sign.Fail("BILIBILI_RESPONSE_INVALID", "心跳失败：响应格式错误", 502)
	}
	code := sign.ValueInt(resp.JSON["code"])
	if code == -101 {
		return sign.Fail("PLUGIN_CREDENTIAL_EXPIRED", "哔哩哔哩登录状态已经失效", 422)
	}
	if code != 0 {
		return sign.Fail("BILIBILI_LIVE_HEARTBEAT_FAILED", firstText(resp.JSON["message"], "心跳失败"), 502)
	}
	return nil
}

func liveUpstreamUnavailable(resp *sign.Response) bool {
	if resp == nil {
		return true
	}
	return resp.Status == 0 || resp.Status == 429 || resp.Status >= 500
}

func (s *LiveService) roomInfo(ctx context.Context, creds map[string]any, roomID int64) (parentArea, areaID int64, name string) {
	resp, err := s.http.Get(ctx, "https://api.live.bilibili.com/room/v1/Room/get_info", sign.RequestOptions{
		Query:   url.Values{"room_id": {strconv.FormatInt(roomID, 10)}},
		Cookie:  s.bilibiliCookie(creds),
		Referer: "https://live.bilibili.com/" + strconv.FormatInt(roomID, 10),
	})
	if err != nil || resp.JSON == nil {
		return 0, 0, ""
	}
	data := sign.Map(resp.JSON["data"])
	return int64(sign.ValueInt(data["parent_area_id"])), int64(sign.ValueInt(data["area_id"])), sign.ValueString(data["title"])
}

// RunSession 执行一次完整的挂机任务（由 live worker 调用）。
func (s *LiveService) RunSession(ctx context.Context, task *model.SignTask, workerID string) {
	started := time.Now()
	runID, err := s.startRun(task, workerID, started)
	if err != nil {
		s.logger.Error("create live run failed", slog.String("error", err.Error()))
		s.finish(task, nil, 0, "failed", 0, 0, 1, "LIVE_RUN_CREATE_FAILED", "创建直播执行记录失败")
		return
	}
	var ownerStatus string
	if err := s.store.Read.Get(&ownerStatus, `SELECT status FROM TF_users WHERE id = ?`, task.UserID); err != nil || ownerStatus != "active" {
		s.finish(task, nil, runID, "failed", 0, 0, 1, "USER_DISABLED", "任务所属用户已停用")
		return
	}
	acc, err := s.accounts.findOwned(task.UserID, task.AccountID)
	if err != nil {
		s.finish(task, nil, runID, "failed", 0, 0, 1, "PLUGIN_ACCOUNT_DISABLED", err.Error())
		return
	}
	if acc.Status != "active" && acc.Status != "pending_verification" {
		s.finish(task, nil, runID, "failed", 0, 0, 1, "PLUGIN_ACCOUNT_DISABLED", "插件账号当前不可执行")
		return
	}
	creds, err := s.accounts.DecryptedCredential(acc)
	if err != nil {
		s.finish(task, nil, runID, "failed", 0, 0, 1, "PLUGIN_CREDENTIAL_MISSING", err.Error())
		return
	}
	settings := unmarshalMap(acc.SettingsJSON)
	target := liveTargetHeartbeats(settings)
	maxRooms := liveMaxRooms(settings)
	maxFailures := liveMaxFailures(settings)

	discoverCtx, discoverCancel := context.WithTimeout(ctx, liveMinDuration)
	medals, err := s.discover(discoverCtx, creds)
	discoverCancel()
	if err != nil {
		s.finish(task, nil, runID, "failed", 0, 0, 1, errorCode(err), err.Error())
		return
	}
	if truthy(settings["live_skip_level_20_medal"]) {
		filtered := medals[:0]
		for _, medal := range medals {
			if medal.MedalLevel < 20 {
				filtered = append(filtered, medal)
			}
		}
		medals = filtered
	}
	if len(medals) > maxRooms {
		medals = medals[:maxRooms]
	}
	if len(medals) == 0 {
		s.finish(task, nil, runID, "succeeded", 0, 0, 0, "", "没有符合条件的粉丝牌，已跳过本次挂机")
		return
	}

	duration := liveSessionDuration(len(medals), target)
	sessionCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	now := database.Now()
	sessionNo := secure.RandomHex(16)
	res, err := s.store.Write.Exec(`INSERT INTO TF_bilibili_live_sessions
		(session_no, task_id, run_id, user_id, account_id, status, target_heartbeat_count, max_failures,
		 max_rooms, settings_json, expires_at, started_at, created_at, updated_at)
		VALUES(?,?,?,?,?, 'running', ?,?,?,?,?,?,?,?)`,
		sessionNo, task.ID, runID, task.UserID, task.AccountID, target, maxFailures,
		maxRooms, mustJSON(settings), database.Ts(time.Now().Add(duration)), now, now, now)
	if err != nil {
		s.finish(task, nil, runID, "failed", 0, 0, 1, "LIVE_SESSION_FAILED", "创建挂机会话失败")
		return
	}
	sessionID, _ := res.LastInsertId()

	completedRooms, partialRooms, failedRooms := 0, 0, 0
	var failures []string
	liveCredentialCode := ""

	for idx, medal := range medals {
		if sessionCtx.Err() != nil {
			remaining := len(medals) - idx
			failedRooms += remaining
			failures = append(failures, fmt.Sprintf("%d 个直播间未处理：会话超时", remaining))
			break
		}
		parentArea, areaID, title := s.roomInfo(sessionCtx, creds, medal.RoomID)
		medal.ParentArea, medal.AreaID = parentArea, areaID
		if title != "" {
			medal.RoomName = title
		}
		deviceBytes := secure.RandomBytes(16)
		device := `["` + sign.ValueString(creds["live_buvid"]) + `","` + fmt.Sprintf("%x-%x-%x-%x-%x",
			deviceBytes[0:4], deviceBytes[4:6], deviceBytes[6:8], deviceBytes[8:10], deviceBytes[10:16]) + `"]`

		roomStarted := time.Now()
		rr, err := s.store.Write.Exec(`INSERT INTO TF_bilibili_live_room_sessions
			(session_id, room_id, ruid, medal_target_id, parent_area_id, area_id, room_name, medal_name, medal_level,
			 status, sequence_no, heartbeat_count, started_at, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?, 'entering', ?,0,?,?,?)`,
			sessionID, medal.RoomID, medal.RUID, medal.RUID, medal.ParentArea, medal.AreaID,
			medal.RoomName, medal.MedalName, medal.MedalLevel, idx, roomStarted.Format(database.TimeLayout), now, now)
		roomRowID := int64(0)
		if err == nil {
			roomRowID, _ = rr.LastInsertId()
		}

		params, err := s.enterRoom(sessionCtx, creds, medal, device, idx)
		if err != nil {
			failedRooms++
			failures = append(failures, medal.MedalName+": "+err.Error())
			if code := errorCode(err); liveCredentialCode == "" && isCredentialFailureCode(code) {
				liveCredentialCode = code
			}
			s.updateRoom(roomRowID, map[string]any{"status": "failed", "last_error_code": errorCode(err),
				"last_error_message": truncate(err.Error(), 500), "next_heartbeat_at": nil, "finished_at": database.Now()})
			if failedRooms >= maxFailures {
				remaining := len(medals) - idx - 1
				if remaining > 0 {
					failedRooms += remaining
					failures = append(failures, fmt.Sprintf("失败达到阈值 %d，剩余 %d 个直播间未处理", maxFailures, remaining))
				}
				break
			}
			continue
		}

		count := 0
		var heartbeatErr error
		for count < target {
			if sessionCtx.Err() != nil {
				heartbeatErr = sessionCtx.Err()
				break
			}
			if err := s.heartbeat(sessionCtx, creds, medal, params, device, idx); err != nil {
				heartbeatErr = err
				if code := errorCode(err); liveCredentialCode == "" && isCredentialFailureCode(code) {
					liveCredentialCode = code
				}
				break
			}
			count++
			s.writeProgress(sessionID, roomRowID, count)
			if count >= target {
				break
			}
			select {
			case <-sessionCtx.Done():
				heartbeatErr = sessionCtx.Err()
			case <-time.After(time.Duration(params.Interval) * time.Second):
			}
		}

		status := "completed"
		if count < target {
			status = "partial"
			if count == 0 {
				status = "failed"
			}
			if heartbeatErr != nil {
				failures = append(failures, medal.MedalName+": "+heartbeatErr.Error())
			}
		}
		s.updateRoom(roomRowID, map[string]any{"status": status, "heartbeat_count": count,
			"next_heartbeat_at": nil, "finished_at": database.Now()})
		switch {
		case count >= target:
			completedRooms++
		case count > 0:
			partialRooms++
		default:
			failedRooms++
		}
		if failedRooms >= maxFailures {
			remaining := len(medals) - idx - 1
			if remaining > 0 {
				failedRooms += remaining
				failures = append(failures, fmt.Sprintf("失败达到阈值 %d，剩余 %d 个直播间未处理", maxFailures, remaining))
			}
			break
		}
	}

	status, successCount, failedCount := liveOutcome(completedRooms, partialRooms, failedRooms)
	summary := fmt.Sprintf("%d 个直播间完成，%d 个部分完成，%d 个失败", completedRooms, partialRooms, failedRooms)
	message := "粉丝牌挂机完成：" + summary
	code := ""
	if status == "failed" {
		code = "BILIBILI_LIVE_FAILED"
		message = "粉丝牌挂机失败：" + summary
		if len(failures) > 0 {
			message = failures[0]
		}
	} else if status == "partial" {
		code = "BILIBILI_LIVE_PARTIAL"
		message = "粉丝牌挂机部分完成：" + summary
	}
	if liveCredentialCode != "" && status != "succeeded" {
		code = liveCredentialCode
		if len(failures) > 0 {
			message = failures[0]
		}
	}
	finishSession := map[string]any{
		"status": "finished", "summary_json": mustJSON(map[string]any{
			"rooms_total": len(medals), "rooms_succeeded": completedRooms, "rooms_partial": partialRooms,
			"rooms_failed": failedRooms, "target_heartbeats": target, "max_failures": maxFailures,
			"duration_ms": time.Since(started).Milliseconds(),
		}),
		"finished_at": database.Now(),
	}
	s.updateSession(sessionID, finishSession)
	s.finish(task, &sessionID, runID, status, successCount, 0, failedCount, code, message)
}

func liveOutcome(completed, partial, failed int) (status string, success, failedCount int) {
	failedCount = partial + failed
	switch {
	case completed > 0 && failedCount > 0:
		return "partial", completed, failedCount
	case completed > 0:
		return "succeeded", completed, 0
	case partial > 0:
		return "partial", 0, failedCount
	default:
		return "failed", 0, failedCount
	}
}

func (s *LiveService) startRun(task *model.SignTask, workerID string, started time.Time) (int64, error) {
	ts := database.Ts(started)
	res, err := s.store.Write.Exec(`INSERT INTO TF_sign_runs
		(run_no, task_id, attempt_no, plugin_code, worker_id, status, started_at, created_at)
		VALUES(?,?,?,?,?, 'running', ?,?)`,
		secure.RandomHex(16), task.ID, task.Attempts, task.PluginCode, workerID, ts, ts)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *LiveService) writeProgress(sessionID, roomRowID int64, count int) {
	if roomRowID == 0 {
		return
	}
	tx, err := s.store.Write.Beginx()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	_, _ = tx.Exec(`UPDATE TF_bilibili_live_room_sessions SET heartbeat_count=?, status='running',
		next_heartbeat_at=?, updated_at=? WHERE id=?`,
		count, database.Ts(time.Now().Add(liveHeartbeatInterval)), database.Now(), roomRowID)
	_, _ = tx.Exec(`UPDATE TF_bilibili_live_sessions SET updated_at=? WHERE id=?`, database.Now(), sessionID)
	_ = tx.Commit()
}

func (s *LiveService) updateRoom(id int64, fields map[string]any) {
	if id == 0 {
		return
	}
	sets := []string{"updated_at = ?"}
	args := []any{database.Now()}
	for k, v := range fields {
		sets = append(sets, k+" = ?")
		args = append(args, v)
	}
	args = append(args, id)
	_, _ = s.store.Write.Exec(`UPDATE TF_bilibili_live_room_sessions SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
}

func (s *LiveService) updateSession(id int64, fields map[string]any) {
	sets := []string{"updated_at = ?"}
	args := []any{database.Now()}
	for k, v := range fields {
		sets = append(sets, k+" = ?")
		args = append(args, v)
	}
	args = append(args, id)
	_, _ = s.store.Write.Exec(`UPDATE TF_bilibili_live_sessions SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
}

func (s *LiveService) finish(task *model.SignTask, sessionID *int64, runID int64, status string, success, already, failed int,
	code, message string) {
	finished := database.Now()
	runStatus := "succeeded"
	if status == "failed" {
		runStatus = "failed"
	} else if status == "partial" {
		runStatus = "partial"
	}
	tx, err := s.store.Write.Beginx()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()

	recordStatus := status
	if recordStatus != "succeeded" && recordStatus != "partial" {
		recordStatus = "failed"
	}
	total := success + failed
	var errCode, errMsg any
	if status != "succeeded" {
		errCode = firstNonEmpty(code, "BILIBILI_LIVE_FAILED")
		errMsg = truncate(message, 500)
	}
	_, _ = tx.Exec(`UPDATE TF_sign_tasks SET status=?, total_count=?, success_count=?, already_count=?, failed_count=?,
		last_error_code=?, last_error_message=?, locked_at=NULL, locked_by=NULL, finished_at=?, updated_at=? WHERE id=?`,
		runStatus, total, success, already, failed, errCode, errMsg, finished, finished, task.ID)
	_, _ = tx.Exec(`UPDATE TF_plugin_accounts SET last_run_at=?, last_error_code=?, last_error_message=?,
		status=CASE WHEN ? THEN 'credential_expired' ELSE status END, updated_at=? WHERE id=?`,
		finished, errCode, errMsg, status != "succeeded" && isCredentialFailureCode(code), finished, task.AccountID)
	if runID > 0 {
		_, _ = tx.Exec(`UPDATE TF_sign_runs SET status=?, total_count=?, success_count=?, already_count=?,
			failed_count=?, error_code=?, error_message=?, summary_json=?, finished_at=? WHERE id=?`,
			runStatus, total, success, already, failed, errCode, errMsg,
			mustJSON(map[string]any{"succeeded": success, "failed": failed, "total": total}), finished, runID)
		recordKey := "live_fans_medal:summary"
		_, _ = tx.Exec(`INSERT INTO TF_sign_records
			(task_id, run_id, user_id, account_id, plugin_code, record_key, sequence_no, schema_version, action,
			 target_type, target_name, status, result_code, message, metrics_json, finished_at, created_at)
			VALUES(?,?,?,?, 'bilibili', ?,0,1, 'live_fans_medal', 'live', '粉丝牌挂机', ?,?,?,?,?,?)`,
			task.ID, runID, task.UserID, task.AccountID, recordKey, recordStatus,
			nullableString(code), truncate(message, 500),
			mustJSON(map[string]any{"succeeded": success, "failed": failed, "total": total}), finished, finished)
	}
	_ = tx.Commit()

	if s.tasks != nil {
		s.tasks.logger.Info("bilibili live session finished",
			slog.String("task", task.TaskNo), slog.String("status", status),
			slog.Int("success", success), slog.Int("failed", failed))
	}
	_ = sessionID
	_ = base64.StdEncoding
}

func errorCode(err error) string {
	if e, ok := err.(*sign.Error); ok {
		return e.Code
	}
	return "PLUGIN_EXECUTION_FAILED"
}
