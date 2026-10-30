package sign

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestTransportErrorDropsURL(t *testing.T) {
	target := "https://cloud.189.cn/mkt/userSign.action?sessionKey=super-secret-token"
	client := NewHTTP()
	original := client.Client.Transport
	client.Client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: target, Err: context.DeadlineExceeded}
	})
	t.Cleanup(func() { client.Client.Transport = original })

	_, err := client.Get(context.Background(), target, RequestOptions{})
	if err == nil {
		t.Fatal("应返回上游请求错误")
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Fatalf("错误信息不应包含查询参数中的敏感值: %s", err.Error())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGzipDecodeFailureReturnsError(t *testing.T) {
	client := NewHTTP()
	client.Client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Content-Encoding", "gzip")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader("not gzip")),
			Request:    r,
		}, nil
	})
	if _, err := client.Get(context.Background(), "https://example.com/x", RequestOptions{}); err == nil {
		t.Fatal("gzip 解压失败应返回错误")
	}
}

func TestCheckRedirectRejectsCrossHostPost(t *testing.T) {
	via := []*http.Request{newTestRequest(t, "POST", "https://a.example/x")}
	req := newTestRequest(t, "POST", "https://b.example/y")
	if err := checkRedirect(req, via); err == nil {
		t.Fatal("跨主机 POST 重定向应被拒绝")
	}
}

func TestCheckRedirectRejectsInsecureAndPrivateTargets(t *testing.T) {
	via := []*http.Request{newTestRequest(t, "GET", "https://a.example/x")}
	if err := checkRedirect(newTestRequest(t, "GET", "http://a.example/y"), via); err == nil {
		t.Fatal("HTTP 重定向应被拒绝")
	}
	if err := checkRedirect(newTestRequest(t, "GET", "https://127.0.0.1/y"), via); err == nil {
		t.Fatal("内网重定向应被拒绝")
	}
}

func TestCheckRedirectAllowsSameHostGet(t *testing.T) {
	via := []*http.Request{newTestRequest(t, "GET", "https://a.example/x")}
	if err := checkRedirect(newTestRequest(t, "GET", "https://a.example/y"), via); err != nil {
		t.Fatalf("同主机 GET 应允许: %v", err)
	}
}

func newTestRequest(t *testing.T, method, target string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, strings.NewReader("a=1"))
	if err != nil {
		t.Fatal(err)
	}
	return req
}
