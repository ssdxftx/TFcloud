package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"log/slog"
	"tfcloud/internal/config"
	"tfcloud/internal/database"
	"tfcloud/internal/httpapi/middleware"
	"tfcloud/internal/httpapi/respond"
	"tfcloud/internal/service"
	"tfcloud/internal/sign"
)

type Server struct {
	Cfg      *config.Config
	Store    *database.Store
	Auth     *service.AuthService
	Settings *service.SettingsService
	Install  *service.InstallService
	Accounts *service.AccountService
	Tasks    *service.TaskService
	Admin    *service.AdminService
	Mail     *service.MailService
	Platform *service.PlatformService
	Audit    *service.AuditService
	Registry *sign.Registry
	Logger   *slog.Logger
}

// body 解析 JSON 请求体，空体返回空 map。
func body(r *http.Request) (map[string]any, error) {
	defer r.Body.Close()
	out := map[string]any{}
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 4<<20))
	if err := dec.Decode(&out); err != nil {
		if err.Error() == "EOF" {
			return out, nil
		}
		return nil, respond.NewError("INVALID_ARGUMENT", "请求体不是合法的 JSON", 400)
	}
	return out, nil
}

func query(r *http.Request) map[string]any {
	out := map[string]any{}
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

func userID(r *http.Request) int64 {
	if p := middleware.FromContext(r.Context()); p != nil {
		return p.UserID
	}
	return 0
}

func pathInt(r *http.Request, key string) (int64, error) {
	raw := chi.URLParam(r, key)
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, respond.NewError("INVALID_ARGUMENT", "路径参数无效", 400)
	}
	return id, nil
}

func path(r *http.Request, key string) string {
	return strings.TrimSpace(chi.URLParam(r, key))
}
