package respond

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

type Envelope struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data"`
	RequestID string `json:"request_id"`
}

type ctxKey string

const requestIDKey ctxKey = "request_id"

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// APIError 是业务错误，Handler 直接返回，中间件统一转成信封。
type APIError struct {
	Code    string
	Message string
	Status  int
	Data    any
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func NewError(code, message string, status int) *APIError {
	return &APIError{Code: code, Message: message, Status: status}
}

func BadRequest(message string) *APIError { return NewError("INVALID_ARGUMENT", message, 400) }
func Unauthorized(message string) *APIError {
	return NewError("UNAUTHENTICATED", message, 401)
}
func Forbidden(message string) *APIError { return NewError("ADMIN_REQUIRED", message, 403) }
func NotFound(message string) *APIError  { return NewError("RESOURCE_NOT_FOUND", message, 404) }
func Conflict(code, message string) *APIError {
	return NewError(code, message, 409)
}
func Internal(message string) *APIError { return NewError("INTERNAL_ERROR", message, 500) }

func JSON(w http.ResponseWriter, status int, payload Envelope) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(payload)
}

func OK(w http.ResponseWriter, r *http.Request, data any) {
	JSON(w, http.StatusOK, Envelope{Code: "SUCCESS", Message: "success", Data: data, RequestID: RequestID(r.Context())})
}

func Created(w http.ResponseWriter, r *http.Request, data any) {
	JSON(w, http.StatusCreated, Envelope{Code: "SUCCESS", Message: "created", Data: data, RequestID: RequestID(r.Context())})
}

func Message(w http.ResponseWriter, r *http.Request, data any, msg string) {
	JSON(w, http.StatusOK, Envelope{Code: "SUCCESS", Message: msg, Data: data, RequestID: RequestID(r.Context())})
}

func Fail(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		apiErr = Internal("服务器内部错误")
	}
	JSON(w, apiErr.Status, Envelope{
		Code:      apiErr.Code,
		Message:   apiErr.Message,
		Data:      apiErr.Data,
		RequestID: RequestID(r.Context()),
	})
}
