package duckgo

import (
	"aurora/internal/httpclient"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fakeStatusClient 返回固定响应的 AuroraHttpClient，用来在不联网的情况下
// 驱动 InitXVQD 的取挑战路径。
type fakeStatusClient struct {
	calls int
	hash  string
}

func (f *fakeStatusClient) Request(_ httpclient.HttpMethod, _ string, _ httpclient.AuroraHeaders, _ []*http.Cookie, _ io.Reader) (*http.Response, error) {
	f.calls++
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"X-Vqd-Hash-1": []string{f.hash}},
		Body:       io.NopCloser(strings.NewReader("{}")),
	}, nil
}

func (f *fakeStatusClient) SetProxy(string) error { return nil }

// 上游不发挑战时: 按 chalRetryDelays 重试(len+1 次), 最后报 ErrChallengeUnavailable
// (调用方据此返回 429, 而不是把它当服务故障)。
func TestInitXVQDRetriesThenReportsRateLimit(t *testing.T) {
	oldDelays := chalRetryDelays
	chalRetryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	oldToken := Token
	Token = &XqdgToken{}
	defer func() {
		chalRetryDelays = oldDelays
		Token = oldToken
	}()

	client := &fakeStatusClient{} // hash 为空 = 上游限速
	_, err := InitXVQD(client, "")
	if !errors.Is(err, ErrChallengeUnavailable) {
		t.Fatalf("err = %v, want ErrChallengeUnavailable", err)
	}
	if want := len(chalRetryDelays) + 1; client.calls != want {
		t.Fatalf("attempts = %d, want %d", client.calls, want)
	}
	if Token.Token != "" {
		t.Fatalf("token must stay empty on failure, got %q", Token.Token)
	}
}

// 拿到挑战头(且解算直接失败)时不重试: 重试只针对「上游没发挑战」。
func TestInitXVQDNoRetryWhenChallengePresent(t *testing.T) {
	oldDelays := chalRetryDelays
	chalRetryDelays = []time.Duration{time.Millisecond}
	oldToken := Token
	Token = &XqdgToken{}
	defer func() {
		chalRetryDelays = oldDelays
		Token = oldToken
	}()

	// 非法 base64 → GenerateVQDHash 直接报错(合法脚本即使跑不起来也只走 fallback, 不报错)
	client := &fakeStatusClient{hash: "!!!not-base64!!!"}
	if _, err := InitXVQD(client, ""); err == nil {
		t.Fatal("expected error for undecodable challenge")
	}
	if client.calls != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry when a challenge is returned)", client.calls)
	}
}
