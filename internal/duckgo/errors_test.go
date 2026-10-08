package duckgo

import (
	"bytes"
	"errors"
	"net/http"
	"testing"
)

// 上游错误类型必须分得开：尺寸上限是确定性的（不该重试、该回 400），
// 限流是暂时的（该回 429 + Retry-After）。混成一个光秃秃 429 就是实测踩到的坑。
func TestUpstreamErrorTypeAndMapping(t *testing.T) {
	body := func(s string) []byte { return []byte(s) }
	cases := []struct {
		name      string
		body      []byte
		want      UpstreamErrorType
		retry     bool
		wantHTTP  int
		wantCode  string
	}{
		{"input limit", body(`{"action":"error","status":429,"type":"ERR_INPUT_LIMIT"}`), ErrTypeInputLimit, false, http.StatusBadRequest, "context_length_exceeded"},
		{"conversation limit", body(`{"type":"ERR_CONVERSATION_LIMIT"}`), ErrTypeConversationLimit, false, http.StatusBadRequest, "context_length_exceeded"},
		{"model restricted", body(`{"type":"ERR_MODEL_RESTRICTED"}`), ErrTypeModelRestricted, false, http.StatusBadRequest, "model_not_available"},
		{"egress blocked", body(`{"type":"ERR_BN_LIMIT"}`), ErrTypeBNLimit, false, http.StatusServiceUnavailable, "egress_blocked"},
		{"rate limit", body(`{"type":"ERR_RATE_LIMIT","r":"c6d8b851"}`), ErrTypeRateLimit, true, http.StatusTooManyRequests, "rate_limit_exceeded"},
		{"challenge", body(`{"type":"ERR_CHALLENGE"}`), ErrTypeChallenge, true, http.StatusTooManyRequests, "challenge_unavailable"},
		{"unknown", body(`{"detail":"whatever"}`), ErrTypeUnknown, true, http.StatusInternalServerError, "upstream_failure"},
	}
	for _, tc := range cases {
		got := UpstreamErrorTypeOf(tc.body)
		if got != tc.want {
			t.Fatalf("%s: type = %q want %q", tc.name, got, tc.want)
		}
		if WorthRetrying(got) != tc.retry {
			t.Fatalf("%s: WorthRetrying = %v want %v", tc.name, WorthRetrying(got), tc.retry)
		}
		status, _, code := ClientError(got)
		if status != tc.wantHTTP || code != tc.wantCode {
			t.Fatalf("%s: got (%d,%s) want (%d,%s)", tc.name, status, code, tc.wantHTTP, tc.wantCode)
		}
	}

	// ERR_MODEL_RESTRICTED 不能被 ERR_MODEL_UNAVAILABLE 抢走
	if got := UpstreamErrorTypeOf(body(`ERR_MODEL_UNAVAILABLE`)); got != ErrTypeModelUnavailable {
		t.Fatalf("model unavailable: %q", got)
	}
	// 真实上游响应体是外面套了一层 JSON 字符串的
	real := []byte(`{"error":{"code":"429 Too Many Requests","message":"{\"action\":\"error\",\"status\":429,\"type\":\"ERR_INPUT_LIMIT\",\"cd\":{}}"}}`)
	if got := UpstreamErrorTypeOf(real); got != ErrTypeInputLimit {
		t.Fatalf("nested body: %q", got)
	}
	if !errors.Is(ErrInputTooLarge, ErrInputTooLarge) || bytes.Contains([]byte(ClientErrorMessage(ErrTypeInputLimit)), []byte("r:")) {
		t.Fatal("客户端文案不应复述上游原文")
	}
}
