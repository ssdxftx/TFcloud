package httpapi

import (
	"net/http"

	"tfcloud/internal/httpapi/respond"
	"tfcloud/internal/service"
)

// ---------- 平台目录 ----------

// platforms 支持 ?detail=1 一次返回凭据字段，避免前端再发 N 个详情请求。
func (s *Server) platforms(w http.ResponseWriter, r *http.Request) {
	if isTruthyParam(strOf(query(r)["detail"])) {
		respond.OK(w, r, s.Platform.ListDetailed())
		return
	}
	respond.OK(w, r, s.Platform.List())
}

func isTruthyParam(v string) bool {
	switch v {
	case "1", "true", "yes":
		return true
	}
	return false
}

func (s *Server) platformDetail(w http.ResponseWriter, r *http.Request) {
	out, err := s.Platform.Get(path(r, "platformCode"))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

// ---------- 平台授权（二维码 / 短信 / 账密） ----------

func (s *Server) platformQRStart(w http.ResponseWriter, r *http.Request) {
	respond.Fail(w, r, respond.NewError("PLATFORM_AUTH_METHOD_UNAVAILABLE",
		"该平台暂未开放扫码接入，请使用 Cookie 导入或账号密码方式", 501))
}

func (s *Server) platformQRPoll(w http.ResponseWriter, r *http.Request) {
	respond.Fail(w, r, respond.NewError("PLATFORM_AUTH_METHOD_UNAVAILABLE", "该平台暂未开放扫码接入", 501))
}

func (s *Server) platformPassword(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	code := path(r, "platformCode")
	credentials := map[string]any{
		"username": strOf(payload["username"]),
		"password": strOf(payload["password"]),
	}
	input := map[string]any{
		"plugin_code": code,
		"credentials": credentials,
		"settings":    payload["settings"],
	}
	account, err := s.Accounts.Create(userID(r), input)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.Audit.Record(userID(r), "account.create", "plugin_account", strOf(account["id"]), map[string]any{
		"plugin_code": code, "method": "password",
	}, respond.RequestID(r.Context()))
	respond.Created(w, r, map[string]any{
		"status":  "succeeded",
		"account": account,
		"message": "账号已绑定",
	})
}

func (s *Server) platformSMSSend(w http.ResponseWriter, r *http.Request) {
	respond.Fail(w, r, respond.NewError("PLATFORM_AUTH_METHOD_UNAVAILABLE", "该平台暂未开放短信登录", 501))
}

func (s *Server) platformSMSComplete(w http.ResponseWriter, r *http.Request) {
	respond.Fail(w, r, respond.NewError("PLATFORM_AUTH_METHOD_UNAVAILABLE", "该平台暂未开放短信登录", 501))
}

// ---------- 插件元数据（管理员） ----------

func (s *Server) plugins(w http.ResponseWriter, r *http.Request) {
	respond.OK(w, r, s.Admin.Plugins())
}

func (s *Server) pluginDetail(w http.ResponseWriter, r *http.Request) {
	code := path(r, "pluginCode")
	for _, item := range s.Admin.Plugins() {
		if strOf(item["code"]) == code {
			respond.OK(w, r, item)
			return
		}
	}
	respond.Fail(w, r, respond.NewError("PLUGIN_NOT_FOUND", "签到插件不存在", 404))
}

func (s *Server) pluginActions(w http.ResponseWriter, r *http.Request) {
	code := path(r, "pluginCode")
	for _, item := range s.Admin.Plugins() {
		if strOf(item["code"]) == code {
			respond.OK(w, r, map[string]any{"plugin_code": code, "actions": item["actions"]})
			return
		}
	}
	respond.Fail(w, r, respond.NewError("PLUGIN_NOT_FOUND", "签到插件不存在", 404))
}

// ---------- 平台账号 ----------

func (s *Server) accountsIndex(w http.ResponseWriter, r *http.Request) {
	items, err := s.Accounts.List(userID(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, items)
}

func (s *Server) accountsStore(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Accounts.Create(userID(r), payload)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.Audit.Record(userID(r), "account.create", "plugin_account", strOf(out["id"]),
		map[string]any{"plugin_code": strOf(payload["plugin_code"])}, respond.RequestID(r.Context()))
	respond.Created(w, r, out)
}

func (s *Server) accountsShow(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Accounts.Get(userID(r), id)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) accountsUpdate(w http.ResponseWriter, r *http.Request) {
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
	out, err := s.Accounts.Update(userID(r), id, payload)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.Audit.Record(userID(r), "account.update", "plugin_account", strOf(out["id"]), nil, respond.RequestID(r.Context()))
	respond.Message(w, r, out, "账号已更新")
}

func (s *Server) accountsDestroy(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	if err := s.Accounts.Delete(userID(r), id); err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.Audit.Record(userID(r), "account.delete", "plugin_account", strOf(id), nil, respond.RequestID(r.Context()))
	respond.Message(w, r, map[string]any{"deleted": true}, "账号已删除")
}

func (s *Server) accountsAction(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Accounts.TriggerAction(userID(r), id, path(r, "action"))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	s.Audit.Record(userID(r), "account.action", "plugin_account", strOf(id),
		map[string]any{"action": path(r, "action")}, respond.RequestID(r.Context()))
	respond.Created(w, r, out)
}

// ---------- 签到任务 ----------

func (s *Server) tasksIndex(w http.ResponseWriter, r *http.Request) {
	out, err := s.Tasks.List(userID(r), query(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) tasksStore(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	accountID := int64(intOf(payload["account_id"]))
	action := strOf(payload["action"])
	if accountID <= 0 {
		respond.Fail(w, r, respond.NewError("VALIDATION_FAILED", "account_id 不能为空", 422))
		return
	}
	out, err := s.Tasks.Create(userID(r), accountID, action, "manual", "", respond.RequestID(r.Context()))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Created(w, r, out)
}

func (s *Server) tasksCalendar(w http.ResponseWriter, r *http.Request) {
	month := firstNonEmptyStr(strOf(query(r)["month"]))
	out, err := s.Tasks.Calendar(userID(r), month)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) tasksShow(w http.ResponseWriter, r *http.Request) {
	out, err := s.Tasks.Show(userID(r), path(r, "taskNo"))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) tasksRuns(w http.ResponseWriter, r *http.Request) {
	out, err := s.Tasks.Runs(userID(r), path(r, "taskNo"), query(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) tasksRecords(w http.ResponseWriter, r *http.Request) {
	out, err := s.Tasks.Records(userID(r), path(r, "taskNo"), query(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) tasksRetry(w http.ResponseWriter, r *http.Request) {
	out, err := s.Tasks.Retry(userID(r), path(r, "taskNo"))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Message(w, r, out, "任务已重新排队")
}

func (s *Server) tasksCancel(w http.ResponseWriter, r *http.Request) {
	out, err := s.Tasks.Cancel(userID(r), path(r, "taskNo"))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Message(w, r, out, "任务已取消")
}

var _ = service.QueueDefault
