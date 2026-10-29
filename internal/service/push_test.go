package service

import "testing"

func TestBarkEndpoint(t *testing.T) {
	got, err := barkEndpoint("abc123", "标题", "内容")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://api.day.app/abc123/%E6%A0%87%E9%A2%98/%E5%86%85%E5%AE%B9" {
		t.Fatalf("Bark key 地址错误: %s", got)
	}
	got, err = barkEndpoint("https://api.day.app/abc123", "t", "b")
	if err != nil || got != "https://api.day.app/abc123/t/b" {
		t.Fatalf("Bark 完整地址错误: %s, %v", got, err)
	}
	if _, err := barkEndpoint("http://127.0.0.1/x", "t", "b"); err == nil {
		t.Fatal("Bark 不应接受非 api.day.app 地址")
	}
}

func TestSendPushRejectsMissingConfig(t *testing.T) {
	if err := sendPush("none", "", "t", "b"); err == nil {
		t.Fatal("未配置渠道时应返回错误")
	}
	if err := sendPush("unknown", "token", "t", "b"); err == nil {
		t.Fatal("未知渠道应返回错误")
	}
}
