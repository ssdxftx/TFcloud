package plugins

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"tfcloud/internal/sign"
)

type staticTransport struct {
	body   string
	status int
}

func (t staticTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: t.status,
		Body:       io.NopCloser(strings.NewReader(t.body)),
		Header:     make(http.Header),
		Request:    r,
	}, nil
}

func newStaticHTTP(body string) *sign.HTTP {
	client := sign.NewHTTP()
	client.Client.Transport = staticTransport{body: body, status: http.StatusOK}
	return client
}

func newTestTiebaClient(body string) *tiebaClient {
	return newTiebaClient(newStaticHTTP(body), map[string]any{"bduss": "test-bduss"})
}

func TestTiebaForumPageRejectsInvalidResponse(t *testing.T) {
	for _, body := range []string{"<html>risk control</html>", "{}"} {
		if _, err := newTestTiebaClient(body).forumPage(context.Background(), 1); err == nil {
			t.Fatalf("响应 %q 应被拒绝", body)
		}
	}
	valid := `{"forum_list":{"gconforum":[],"non-gconforum":[]},"has_more":"0"}`
	if _, err := newTestTiebaClient(valid).forumPage(context.Background(), 1); err != nil {
		t.Fatalf("合法空列表不应报错: %v", err)
	}
}
