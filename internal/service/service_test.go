package service

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeIdentity(t *testing.T) {
	cases := map[string]string{
		"  Admin ":     "admin",
		"ADMIN":        "admin",
		"Üser":         "user", // 去重音，与 MySQL ai_ci 一致
		"café@a.com":   "cafe@a.com",
		"cafe\u0301@a": "cafe@a",
	}
	for input, want := range cases {
		if got := NormalizeIdentity(input); got != want {
			t.Fatalf("NormalizeIdentity(%q) = %q, want %q", input, got, want)
		}
	}
	if NormalizeIdentity("Admin") != NormalizeIdentity("admin") {
		t.Fatal("大小写必须归一到同一唯一键")
	}
}

func TestNextScheduleAutoWindow(t *testing.T) {
	next := nextSchedule(map[string]any{"schedule_mode": "auto"}, "tieba:1:1")
	if next == "" {
		t.Fatal("auto 模式必须给出下次执行时间")
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", next, time.Local)
	if err != nil {
		t.Fatalf("时间格式错误: %v", err)
	}
	minutes := parsed.Hour()*60 + parsed.Minute()
	if minutes < 300 || minutes >= 480 {
		t.Fatalf("auto 模式应落在 05:00-08:00，实际 %s", next)
	}
	if !parsed.After(time.Now()) {
		t.Fatalf("下次执行时间必须晚于当前时间: %s", next)
	}
	// 同一 seed 必须稳定，避免每次调度都漂移
	if again := nextSchedule(map[string]any{"schedule_mode": "auto"}, "tieba:1:1"); again != next {
		t.Fatalf("同 seed 结果应稳定: %s vs %s", next, again)
	}
}

func TestNextScheduleFixed(t *testing.T) {
	if got := nextSchedule(map[string]any{"schedule_mode": "fixed", "schedule_time": "25:00"}, "x"); got != "" {
		t.Fatalf("非法时间应返回空: %s", got)
	}
	got := nextSchedule(map[string]any{"schedule_mode": "fixed", "schedule_time": "07:30"}, "x")
	if !strings.HasSuffix(got, "07:30:00") {
		t.Fatalf("固定时间应保持 07:30，实际 %s", got)
	}
	if parsed, _ := time.ParseInLocation("2006-01-02 15:04:05", got, time.Local); !parsed.After(time.Now()) {
		t.Fatal("固定时间必须滚动到未来")
	}
}

func TestNormalizeLegacySettings(t *testing.T) {
	// 只有 schedule_enabled 且没有历史执行时刻时，无法确定具体时间，降级为 auto
	out := normalizeLegacySettings(map[string]any{"schedule_enabled": true}, nil)
	if out["schedule_mode"] != "auto" {
		t.Fatalf("无历史时刻时应降级 auto，实际 %v", out["schedule_mode"])
	}
	// 有历史 next_run_at 时沿用原计划
	legacy := "2026-01-01 09:15:00"
	out = normalizeLegacySettings(map[string]any{"schedule_enabled": true}, &legacy)
	if out["schedule_mode"] != "fixed" || out["schedule_time"] != "09:15" {
		t.Fatalf("应沿用历史计划 09:15，实际 %v %v", out["schedule_mode"], out["schedule_time"])
	}
	legacyNext := "2026-01-01 06:45:00"
	out = normalizeLegacySettings(map[string]any{"schedule_mode": "fixed"}, &legacyNext)
	if out["schedule_time"] != "06:45" {
		t.Fatalf("应从既有 next_run_at 继承时间，实际 %v", out["schedule_time"])
	}
}

func TestPaginateSupportsOffset(t *testing.T) {
	page, perPage, offset := paginate(map[string]any{"limit": "20", "offset": "40"}, 20)
	if page != 3 || perPage != 20 || offset != 40 {
		t.Fatalf("offset 分页解析错误: page=%d perPage=%d offset=%d", page, perPage, offset)
	}
}

func TestRandomCodeFormat(t *testing.T) {
	for i := 0; i < 200; i++ {
		code := randomCode()
		if len(code) != 6 {
			t.Fatalf("验证码长度应为 6，实际 %q", code)
		}
		for _, r := range code {
			if r < '0' || r > '9' {
				t.Fatalf("验证码必须为数字，实际 %q", code)
			}
		}
	}
}

func TestRetryPolicy(t *testing.T) {
	if retryDelaySeconds(1) != 60 {
		t.Fatal("首次重试应为 60 秒")
	}
	if retryDelaySeconds(100) != 900 {
		t.Fatal("重试间隔应封顶 900 秒")
	}
	if extendedAttemptLimit(24, 24) != 25 {
		t.Fatal("持续补签应抬高上限")
	}
	if extendedAttemptLimit(2, 24) != 24 {
		t.Fatal("不应降低原有上限")
	}
}
