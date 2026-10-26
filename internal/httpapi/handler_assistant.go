package httpapi

import (
	"encoding/json"
	"net/http"

	"tfcloud/internal/httpapi/respond"
)

// ---------- 智能助手 ----------

func (s *Server) assistantStatus(w http.ResponseWriter, r *http.Request) {
	respond.OK(w, r, s.Assistant.Status())
}

func (s *Server) assistantMessages(w http.ResponseWriter, r *http.Request) {
	out, err := s.Assistant.History(userID(r), query(r))
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}

func (s *Server) assistantSend(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	out, err := s.Assistant.Send(userID(r), payload)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Created(w, r, out)
}

// assistantStream 输出 NDJSON：每行一个 {event, data}。
func (s *Server) assistantStream(w http.ResponseWriter, r *http.Request) {
	payload, err := body(r)
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		respond.Fail(w, r, respond.Internal("当前服务不支持流式响应"))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	emit := func(event string, data any) {
		_ = enc.Encode(map[string]any{"event": event, "data": data})
		flusher.Flush()
	}
	if err := s.Assistant.Stream(r.Context(), userID(r), payload, emit); err != nil {
		emit("error", map[string]any{"code": "ASSISTANT_REQUEST_FAILED", "message": err.Error()})
	}
}

func (s *Server) assistantReset(w http.ResponseWriter, r *http.Request) {
	if err := s.Assistant.Reset(userID(r)); err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.Message(w, r, map[string]any{"reset": true}, "对话已清空")
}

func (s *Server) adminAssistantTest(w http.ResponseWriter, r *http.Request) {
	out, err := s.Assistant.TestConnection()
	if err != nil {
		respond.Fail(w, r, err)
		return
	}
	respond.OK(w, r, out)
}
