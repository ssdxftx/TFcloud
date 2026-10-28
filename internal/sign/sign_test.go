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
