package service

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"strings"
	"testing"
)

func TestCaptchaImageIsPNG(t *testing.T) {
	uri := captchaImage("ABCDE")
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(uri, prefix) {
		t.Fatalf("验证码应为 PNG data URI，实际 %q", uri[:minInt(len(uri), 30)])
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, prefix))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 520 || img.Bounds().Dy() != 160 {
		t.Fatalf("验证码尺寸错误: %v", img.Bounds())
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
