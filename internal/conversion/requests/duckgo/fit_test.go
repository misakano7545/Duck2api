package duckgo

import (
	"strings"
	"testing"

	duckgotypes "aurora/internal/typings/duckgo"
	"aurora/internal/util"
)

func build(tokensPerMsg ...int) duckgotypes.ApiRequest {
	req := duckgotypes.NewApiRequest("gpt-5.6-luna")
	for i, n := range tokensPerMsg {
		role := "user"
		if i == 0 {
			role = "system"
		}
		req.AddMessage(role, strings.Repeat("token ", n))
	}
	return req
}

func tokensOfReq(t *testing.T, req *duckgotypes.ApiRequest) int {
	t.Helper()
	var sb strings.Builder
	for i := 0; i < req.MessageCount(); i++ {
		sb.WriteString(req.MessageText(i))
		sb.WriteString("\n")
	}
	return util.CountToken(sb.String())
}

// 超限时丢旧历史，首条与最新几条必须留下 —— 丢最新的等于把这轮的问题也丢了。
func TestFitTrimsOldHistoryKeepsFirstAndNewest(t *testing.T) {
	t.Setenv("MAX_INPUT_TOKENS", "200")
	req := build(20, 60, 60, 60, 60, 60)
	first, last := req.MessageText(0), req.MessageText(req.MessageCount()-1)
	before := req.MessageCount()

	dropped, ok := FitToUpstreamLimit(&req)
	if !ok {
		t.Fatal("应当装得下")
	}
	if dropped == 0 || req.MessageCount() >= before {
		t.Fatalf("没丢东西: dropped=%d count=%d before=%d", dropped, req.MessageCount(), before)
	}
	if req.MessageText(0) != first {
		t.Fatal("首条被动了")
	}
	if req.MessageText(req.MessageCount()-1) != last {
		t.Fatal("最新的那条被丢了")
	}
	if n := tokensOfReq(t, &req); n > 200 {
		t.Fatalf("仍然超限: %d", n)
	}
}

// 装得下就一个字节都不动。
func TestFitNoopWhenItFits(t *testing.T) {
	t.Setenv("MAX_INPUT_TOKENS", "100000")
	req := build(5, 5, 5)
	before := req.MessageCount()
	if dropped, ok := FitToUpstreamLimit(&req); dropped != 0 || !ok || req.MessageCount() != before {
		t.Fatalf("dropped=%d ok=%v count=%d", dropped, ok, req.MessageCount())
	}
}

// 首条（system / 工具约定宿主）自己就超上限：不截断它，报 false 让调用方回 400。
func TestFitRefusesWhenFirstMessageAloneTooBig(t *testing.T) {
	t.Setenv("MAX_INPUT_TOKENS", "20")
	req := build(200, 1, 1)
	if dropped, ok := FitToUpstreamLimit(&req); ok {
		t.Fatalf("应当拒绝: dropped=%d", dropped)
	}
	if req.MessageText(0) != strings.Repeat("token ", 200) {
		t.Fatal("首条不该被截断")
	}
}

// 单条消息就超限（n==1）同样拒绝，别把自己压成空请求。
func TestFitRefusesSingleOversizedMessage(t *testing.T) {
	t.Setenv("MAX_INPUT_TOKENS", "20")
	req := build(200)
	if _, ok := FitToUpstreamLimit(&req); ok {
		t.Fatal("单条超限应当拒绝")
	}
}

// MAX_INPUT_TOKENS 负数 = 关掉守卫，原样放行。
func TestFitDisabledByNegativeLimit(t *testing.T) {
	t.Setenv("MAX_INPUT_TOKENS", "-1")
	req := build(500, 500, 500)
	before := req.MessageCount()
	if dropped, ok := FitToUpstreamLimit(&req); dropped != 0 || !ok || req.MessageCount() != before {
		t.Fatalf("守卫没关: dropped=%d ok=%v", dropped, ok)
	}
}
