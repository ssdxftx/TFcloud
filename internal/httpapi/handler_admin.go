package httpapi

import (
	"net/http"

	"tfcloud/internal/httpapi/respond"
)

// ---------- 概览 ----------

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) {
	out, err := s.Admin.Overview()
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) adminVersion(w http.ResponseWriter, r *http.Request) {
	respond.OK(w, r, s.Admin.Version())
}

// ---------- 设置 ----------

func (s *Server) adminSettings(w http.ResponseWriter, r *http.Request) {
	respond.OK(w, r, s.Admin.Settings())
}

func (s *Server) adminSettingsGroup(w http.ResponseWriter, r *http.Request) {
	group := path(r, "group")
	all := s.Admin.Settings()
	value, ok := all[group]
	if !ok {
		respond.Fail(w, r, respond.NewError("SETTINGS_GROUP_NOT_FOUND", "配置分组不存在", 404))
		return
	}
	respond.OK(w, r, value)
}

func (s *Server) adminSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	group := path(r, "group")
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Settings.Update(group, payload, userID(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.Audit.Record(userID(r), "admin.settings.update", "settings", group, map[string]any{"keys": len(payload)}, respond.RequestID(r.Context()))
	respond.Message(w, r, out, "配置已保存")
}

func (s *Server) adminQqRelayApply(w http.ResponseWriter, r *http.Request) {
	respond.Fail(w, r, respond.NewError("QQ_RELAY_UNAVAILABLE", "QQ 登录中转申请尚未移植", 501))
}

func (s *Server) adminQqRelayVerify(w http.ResponseWriter, r *http.Request) {
	respond.Fail(w, r, respond.NewError("QQ_RELAY_UNAVAILABLE", "QQ 登录中转校验尚未移植", 501))
}

func (s *Server) adminQqRelayConfigure(w http.ResponseWriter, r *http.Request) {
	respond.Fail(w, r, respond.NewError("QQ_RELAY_UNAVAILABLE", "QQ 登录中转配置尚未移植", 501))
}

// ---------- 用户 ----------

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	out, err := s.Admin.Users(query(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) adminUserCreate(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Admin.CreateUser(payload, userID(r), respond.RequestID(r.Context()))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Created(w, r, out)
}

func (s *Server) adminUserShow(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Admin.UserByID(id)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) adminUserUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Admin.UpdateUser(id, payload, userID(r), respond.RequestID(r.Context()))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Message(w, r, out, "用户已更新")
}

func (s *Server) adminUserDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	if err := s.Admin.DeleteUser(id, userID(r), respond.RequestID(r.Context())); err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Message(w, r, map[string]any{"deleted": true}, "用户已删除")
}

func (s *Server) adminUserResetPassword(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	if err := s.Admin.ResetUserPassword(id, strOf(payload["password"]), userID(r), respond.RequestID(r.Context())); err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Message(w, r, map[string]any{"reset": true}, "密码已重置，该用户需重新登录")
}

func (s *Server) adminUserRevokeSessions(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	n, err := s.Auth.RevokeUserSessions(id)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.Audit.Record(userID(r), "admin.user.revoke_sessions", "user", strOf(id), nil, respond.RequestID(r.Context()))
	respond.Message(w, r, map[string]any{"revoked_count": n}, "该用户的登录会话已全部失效")
}

// ---------- 后台任务 ----------

func (s *Server) adminTasks(w http.ResponseWriter, r *http.Request) {
	out, err := s.Admin.Tasks(query(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) adminTaskShow(w http.ResponseWriter, r *http.Request) {
	out, err := s.Admin.Task(path(r, "taskNo"))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) adminTaskRuns(w http.ResponseWriter, r *http.Request) {
	out, err := s.Admin.TaskRuns(path(r, "taskNo"), query(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) adminTaskRecords(w http.ResponseWriter, r *http.Request) {
	out, err := s.Admin.TaskRecords(path(r, "taskNo"), query(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) adminTaskRetry(w http.ResponseWriter, r *http.Request) {
	out, err := s.Admin.RetryTask(path(r, "taskNo"), userID(r), respond.RequestID(r.Context()))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Message(w, r, out, "已创建完整重跑任务")
}

func (s *Server) adminTaskCancel(w http.ResponseWriter, r *http.Request) {
	out, err := s.Admin.CancelTask(path(r, "taskNo"), userID(r), respond.RequestID(r.Context()))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Message(w, r, out, "任务已取消")
}

// ---------- 后台账号 ----------

func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request) {
	out, err := s.Admin.Accounts(query(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) adminAccountUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Admin.UpdateAccount(id, payload, userID(r), respond.RequestID(r.Context()))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Message(w, r, out, "账号已更新")
}

func (s *Server) adminAccountDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	if err := s.Admin.DeleteAccount(id, userID(r), respond.RequestID(r.Context())); err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Message(w, r, map[string]any{"deleted": true}, "账号已删除")
}

func (s *Server) adminAccountAction(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Admin.AccountAction(id, path(r, "action"), userID(r), respond.RequestID(r.Context()))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Created(w, r, out)
}

// ---------- 审计 ----------

func (s *Server) adminAuditLogs(w http.ResponseWriter, r *http.Request) {
	out, err := s.Audit.List(query(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	// 前端按数组做游标追加，这里保持与旧接口一致的数组形状。
	items, _ := out["items"].([]map[string]any)
	respond.OK(w, r, items)
}

// ---------- 邮件 ----------

func (s *Server) adminMailSummary(w http.ResponseWriter, r *http.Request) {
	out, err := s.Mail.Summary()
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) adminMailTasks(w http.ResponseWriter, r *http.Request) {
	limit, offset := listPaging(query(r), 20, 200)
	out, err := s.Mail.Tasks(limit, offset)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) adminMailTaskRecords(w http.ResponseWriter, r *http.Request) {
	out, err := s.Mail.Records(path(r, "taskNo"))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) adminMailRecords(w http.ResponseWriter, r *http.Request) {
	q := query(r)
	limit, offset := listPaging(q, 20, 200)
	out, err := s.Mail.AllRecords(strOf(q["keyword"]), strOf(q["status"]), strOf(q["template_code"]), limit, offset)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

// listPaging 同时兼容 page/per_page 与 limit/offset 两种分页参数。
func listPaging(q map[string]any, defaultLimit, maxLimit int) (limit, offset int) {
	limit = intOf(q["limit"])
	if limit <= 0 {
		limit = intOf(q["per_page"])
	}
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	offset = intOf(q["offset"])
	if offset < 0 {
		offset = 0
	}
	if offset == 0 {
		page := intOf(q["page"])
		if page > 1 {
			offset = (page - 1) * limit
		}
	}
	return limit, offset
}

func (s *Server) adminMailRecordDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	if err := s.Mail.DeleteRecord(id); err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Message(w, r, map[string]any{"deleted": true}, "记录已删除")
}

func (s *Server) adminMailRetry(w http.ResponseWriter, r *http.Request) {
	out, err := s.Mail.Retry(path(r, "taskNo"))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.Audit.Record(userID(r), "admin.mail.retry", "mail_task", path(r, "taskNo"), nil, respond.RequestID(r.Context()))
	respond.Message(w, r, out, "失败邮件已重新入队")
}

func (s *Server) adminMailCompose(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Mail.Compose(payload, userID(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.Audit.Record(userID(r), "admin.mail.compose", "mail_task", strOf(out["task_no"]),
		map[string]any{"total_count": out["total_count"], "subject": out["subject"]}, respond.RequestID(r.Context()))
	respond.Created(w, r, out)
}

func (s *Server) adminMailTest(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	recipient := strOf(payload["recipient"])
	if err := s.Mail.Test(recipient); err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.Audit.Record(userID(r), "admin.mail.test", "mail", "", map[string]any{"recipient": recipient}, respond.RequestID(r.Context()))
	respond.Message(w, r, map[string]any{"sent": true}, "测试邮件已发送")
}
