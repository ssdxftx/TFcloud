package plugins

import (
	"context"
	"testing"
)

func TestIqiyiScoreSignCredentialExpired(t *testing.T) {
	c := &iqiyiClient{http: newStaticHTTP(`{"code":"A00401"}`), p00001: "cookie", p00003: "42"}
	rec := c.scoreSign(context.Background(), 1)
	if rec.Status != "failed" || rec.Code != "PLUGIN_CREDENTIAL_EXPIRED" {
		t.Fatalf("A00401 应识别为凭据过期，实际 status=%s code=%s", rec.Status, rec.Code)
	}
}

func TestBilibiliLiveSignAlreadyDoneMessage(t *testing.T) {
	c := newBilibiliClient(newStaticHTTP(`{"code":1011040}`), map[string]any{})
	rec := c.liveSign(context.Background())
	if rec.Status != "already_done" {
		t.Fatalf("应识别为今日已签到，实际 %s", rec.Status)
	}
	if rec.Message != "直播签到今日已完成" {
		t.Fatalf("已签到文案错误: %q", rec.Message)
	}
}

func TestCollectRewardsSupportsListFallback(t *testing.T) {
	rewards := collectRewards(map[string]any{
		"list": []any{map[string]any{"gift_name": "coin", "gift_num": 3}},
	})
	if len(rewards) != 1 {
		t.Fatalf("应从 list 解析奖励，实际 %#v", rewards)
	}
	item, _ := rewards[0].(map[string]any)
	if item["name"] != "coin" || item["count"] != 3 {
		t.Fatalf("奖励解析错误: %#v", item)
	}
}
