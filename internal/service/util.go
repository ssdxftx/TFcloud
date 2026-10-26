package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

var regexpSixDigits = regexp.MustCompile(`^\d{6}$`)

func marshalNoEscape(v any) (string, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return string(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

func unmarshalMap(v *string) map[string]any {
	out := map[string]any{}
	if v == nil || *v == "" {
		return out
	}
	_ = json.Unmarshal([]byte(*v), &out)
	return out
}

func unmarshalAny(v *string) any {
	if v == nil || *v == "" {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(*v), &out); err != nil {
		return nil
	}
	return out
}

func jsonOrNil(v any) any {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok && s == "" {
		return nil
	}
	b, err := marshalNoEscape(v)
	if err != nil {
		return nil
	}
	return b
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func base64Std(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

func firstText(values ...any) string {
	for _, v := range values {
		if s := strings.TrimSpace(str(v)); s != "" {
			return s
		}
	}
	return ""
}

func firstNonNil(values ...any) any {
	for _, v := range values {
		if v != nil {
			return v
		}
	}
	return nil
}

// HostID 用于区分同一实例内不同 Worker 的锁标识。
func HostID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "tfcloud"
	}
	return host
}
