package plugins

import (
	"context"
	"strings"
	"testing"
)

func TestUmaSignMatchesReferenceAlgorithm(t *testing.T) {
	params := map[string]string{
		"activity_group_id": "1755595320481626",
		"activity_id":       "10000244",
		"lang":              "zh",
		"app_id":            "6829",
		"nonce":             "abc",
		"ts":                "1700000000000",
		"appkey":            "7200bfa761c94eae9ceb168bf4b129d0",
	}
	if got, want := umaSign(params), "7507d6f43511ae049b21239520a735be"; got != want {
		t.Fatalf("Uma 签名错误，期望 %s，实际 %s", want, got)
	}
}

func TestNormalizeUmaCookieKeepsOnlyRequiredKeys(t *testing.T) {
	raw := "site=1; other=x; joy_jct=j; DedeUserID=42; SESSDATA=s; extra=1"
	got := normalizeUmaCookie(raw)
	for _, part := range []string{"site=1", "joy_jct=j", "DedeUserID=42", "SESSDATA=s"} {
		if !strings.Contains(got, part) {
			t.Fatalf("缺少 Cookie %q：%s", part, got)
		}
	}
	if strings.Contains(got, "other=") || strings.Contains(got, "extra=") {
		t.Fatalf("不应保留无关 Cookie：%s", got)
	}
}

func TestV2EXMissionDetectsClaimedStreak(t *testing.T) {
	body := `<html><body>每日登录奖励已领取，已连续登录 12 天</body></html>`
	client := &v2exClient{cookie: "PB3_SESSION=x", http: newStaticHTTP(body)}
	once, claimed, streak, _, err := client.mission(context.Background())
	if err != nil {
		t.Fatalf("mission 不应失败: %v", err)
	}
	if once != "" || !claimed || streak != "12" {
		t.Fatalf("解析结果错误: once=%q claimed=%v streak=%q", once, claimed, streak)
	}
}

func TestV2EXBalanceParsesValues(t *testing.T) {
	body := `<div class="balance_area bigger"><img src="gold.png">12<img src="silver.png">34<img src="bronze.png">56</div>`
	if got, want := v2exBalance(body), "12 金币，34 银币，56 铜币"; got != want {
		t.Fatalf("余额解析错误，期望 %q，实际 %q", want, got)
	}
	body = `<div class="balance_area bigger"><img>34<img>56</div>`
	if got, want := v2exBalance(body), "0 金币，34 银币，56 铜币"; got != want {
		t.Fatalf("双数值余额解析错误，期望 %q，实际 %q", want, got)
	}
}

func TestYamiboSignPageParsesButtonAndHash(t *testing.T) {
	body := `<html><body><a class="btna" href="plugin.php?id=zqlj_sign&sign=abc123">点击打卡</a><div>我的打卡动态</div></body></html>`
	client := &yamiboClient{cookie: "discuz_uid=7", http: newStaticHTTP(body)}
	page, err := client.signPage(context.Background())
	if err != nil {
		t.Fatalf("signPage 不应失败: %v", err)
	}
	if page.signHash != "abc123" || page.alreadySigned {
		t.Fatalf("页面解析错误: hash=%q already=%v", page.signHash, page.alreadySigned)
	}
}

func TestYamiboStatsParsesChineseLabels(t *testing.T) {
	stats := yamiboStats(`<div>最近打卡：2026-10-29</div><div>本月打卡：18</div><div>连续打卡：6</div><div>累计打卡：120</div><div>最近奖励：+5 对象</div>`)
	if stats["本月打卡"] != "18" || stats["连续打卡"] != "6" || stats["累计打卡"] != "120" {
		t.Fatalf("签到统计解析错误: %#v", stats)
	}
}

func TestYamiboAccountParsesUsernameAndUID(t *testing.T) {
	body := `<div id="um"><p><strong class="vwmy"><a href="home.php?mod=space&uid=123" target="_blank">百合用户</a></strong></p></div>`
	info := yamiboAccount(body, "")
	if info.UID != "123" || info.Username != "百合用户" {
		t.Fatalf("账号信息解析错误: %#v", info)
	}
	info = yamiboAccount(`<a href="space-uid-456.html">另一个用户</a>`, "discuz_uid=456; sid=x")
	if info.UID != "456" || info.Username != "另一个用户" {
		t.Fatalf("伪静态账号信息解析错误: %#v", info)
	}
}
