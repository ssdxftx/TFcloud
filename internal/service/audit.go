package service

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"tfcloud/internal/database"
)

var scheduleMonthPattern = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// ---------- 审计 ----------

type AuditService struct {
	store *database.Store
}

func NewAudit(store *database.Store) *AuditService { return &AuditService{store: store} }

func (s *AuditService) Record(userID int64, action, resourceType, resourceID string, context map[string]any, requestID ...string) {
	if s == nil || s.store == nil {
		return
	}
	_, _ = s.store.Write.Exec(`INSERT INTO TF_audit_logs
		(user_id, request_id, action, resource_type, resource_id, context_json, created_at)
		VALUES(?,?,?,?,?,?,?)`,
		nullableID(userID), nullableString(firstString(requestID)), truncate(action, 128),
		nullableString(resourceType), nullableString(resourceID), jsonOrNil(context), database.Now())
}

func firstString(values []string) string {
	if len(values) > 0 {
		return strings.TrimSpace(values[0])
	}
	return ""
}

func (s *AuditService) RecordWithIP(userID int64, action, resourceType, resourceID, ip string, context map[string]any) {
	if s == nil || s.store == nil {
		return
	}
	_, _ = s.store.Write.Exec(`INSERT INTO TF_audit_logs
		(user_id, action, resource_type, resource_id, ip_address, context_json, created_at)
		VALUES(?,?,?,?,?,?,?)`,
		nullableID(userID), truncate(action, 128), nullableString(resourceType), nullableString(resourceID),
		nullableString(ip), jsonOrNil(context), database.Now())
}

func (s *AuditService) List(filters map[string]any) (map[string]any, error) {
	page, perPage, offset := paginate(filters, 20)
	where := []string{"1=1"}
	args := []any{}
	if action := strings.TrimSpace(str(filters["action"])); action != "" {
		where = append(where, "action LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(action)+"%")
	}
	if userID := asInt(filters["user_id"]); userID > 0 {
		where = append(where, "user_id = ?")
		args = append(args, userID)
	}
	if beforeID := asInt(filters["before_id"]); beforeID > 0 {
		where = append(where, "id < ?")
		args = append(args, beforeID)
	}
	clause := strings.Join(where, " AND ")
	var total int
	_ = s.store.Read.Get(&total, `SELECT COUNT(1) FROM TF_audit_logs WHERE `+clause, args...)
	items, err := s.store.Maps(`SELECT id, request_id, user_id, action, resource_type, resource_id,
		ip_address, context_json, created_at FROM TF_audit_logs WHERE `+clause+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, perPage, offset)...)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if v, ok := item["context_json"]; ok {
			item["context_json"] = parseJSONTextDefault(v)
		}
	}
	return pageResult(items, total, page, perPage, offset), nil
}

// ---------- 敏感数据脱敏 ----------

var sensitiveKeyPattern = regexp.MustCompile(`(?i)(cookie|token|secret|password|passwd|sessdata|bduss|csrf|authorization|api_?key)`)
var secretValuePattern = regexp.MustCompile(`(?i)(BDUSS|STOKEN|SESSDATA|bili_jct|MUSIC_U|__csrf|P00001|P00003|access_token|refresh_token)=([^;&\s"']+)`)

func RedactString(v string) string {
	if v == "" {
		return v
	}
	return secretValuePattern.ReplaceAllString(v, "$1=***")
}

func RedactMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if sensitiveKeyPattern.MatchString(k) {
			out[k] = "***"
			continue
		}
		switch t := v.(type) {
		case string:
			out[k] = RedactString(t)
		case map[string]any:
			out[k] = RedactMap(t)
		case []any:
			items := make([]any, 0, len(t))
			for _, item := range t {
				if m, ok := item.(map[string]any); ok {
					items = append(items, RedactMap(m))
				} else if s, ok := item.(string); ok {
					items = append(items, RedactString(s))
				} else {
					items = append(items, item)
				}
			}
			out[k] = items
		default:
			out[k] = v
		}
	}
	return out
}

func typeName(v any) string {
	if v == nil {
		return ""
	}
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return fmt.Sprintf("%s.%s", t.PkgPath(), t.Name())
}
