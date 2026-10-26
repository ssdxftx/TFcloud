package service

import (
	"encoding/json"
	"fmt"
	"hash/crc32"
	"strings"
	"time"

	"tfcloud/internal/database"
	"tfcloud/internal/httpapi/respond"
)

func normalizeLegacySettings(settings map[string]any, nextRunAt *string) map[string]any {
	out := map[string]any{}
	for k, v := range settings {
		out[k] = v
	}
	mode := strings.TrimSpace(str(out["schedule_mode"]))
	if mode == "" {
		if truthy(out["schedule_enabled"]) {
			mode = "fixed"
		} else {
			mode = "auto"
		}
		out["schedule_mode"] = mode
	}
	if mode != "fixed" {
		return out
	}
	if scheduleTimePattern.MatchString(strings.TrimSpace(str(out["schedule_time"]))) {
		return out
	}
	fallback := ""
	if nextRunAt != nil && len(*nextRunAt) >= 16 {
		fallback = (*nextRunAt)[11:16]
	}
	if scheduleTimePattern.MatchString(fallback) {
		out["schedule_time"] = fallback
	} else {
		out["schedule_mode"] = "auto"
		delete(out, "schedule_time")
	}
	return out
}

// nextSchedule 复刻 DailySchedule::next，返回空字符串表示不安排。
func nextSchedule(settings map[string]any, seed string) string {
	mode := strings.TrimSpace(str(settings["schedule_mode"]))
	if mode == "" {
		if truthy(settings["schedule_enabled"]) {
			mode = "fixed"
		} else {
			mode = "auto"
		}
	}
	var clock string
	switch mode {
	case "auto":
		minuteOfDay := 300 + int(crc32.ChecksumIEEE([]byte(seed))%180)
		clock = fmt.Sprintf("%02d:%02d", minuteOfDay/60, minuteOfDay%60)
	case "fixed":
		clock = strings.TrimSpace(str(settings["schedule_time"]))
		if !scheduleTimePattern.MatchString(clock) {
			return ""
		}
	default:
		return ""
	}
	now := time.Now()
	parsed, err := time.ParseInLocation("2006-01-02 15:04", now.Format("2006-01-02")+" "+clock, time.Local)
	if err != nil {
		return ""
	}
	if !parsed.After(now) {
		parsed = parsed.Add(24 * time.Hour)
	}
	return database.Ts(parsed)
}

// EveryDayAt 返回下一个整点时间点，供调度器复用。
func EveryDayAt(clock string) (time.Time, error) {
	if !scheduleTimePattern.MatchString(clock) {
		return time.Time{}, respond.NewError("SCHEDULE_TIME_INVALID", "schedule_time 必须使用 HH:MM 格式", 422)
	}
	now := time.Now()
	parsed, err := time.ParseInLocation("2006-01-02 15:04", now.Format("2006-01-02")+" "+clock, time.Local)
	if err != nil {
		return time.Time{}, err
	}
	if !parsed.After(now) {
		parsed = parsed.Add(24 * time.Hour)
	}
	return parsed, nil
}

func unmarshalInto(data []byte, out any) error { return json.Unmarshal(data, out) }
