package duckgo

import (
	"os"
	"strconv"

	duckgotypes "aurora/internal/typings/duckgo"
	"aurora/internal/util"
)

// 上游单请求输入上限。实测 2026-10，量级 ≈4k token：
//
//	19,017 ASCII 字符（上游回传 prompt_tokens 4083）通过；20,017 字符 → ERR_INPUT_LIMIT。
//	完全空闲 12 分钟后的单发 36,000 字符**仍然**是 ERR_INPUT_LIMIT 且 3.7s 就返回 ——
//	这是确定性上限，不是限流窗口，等不出来（另一路 ERR_RATE_LIMIT 才是限流，两者
//	客户端看到的都是 429，区分见 internal/duckgo/errors.go）。
//
// 所以网关自己也要按这个数把关：让客户端 3.7s 拿到一句"输入太大"，而不是白等 67s。
const DefaultMaxInputTokens = 3800

// MaxInputTokens 生效上限；MAX_INPUT_TOKENS=0（或非法）按默认，负数表示关闭守卫。
func MaxInputTokens() int {
	v := os.Getenv("MAX_INPUT_TOKENS")
	if v == "" {
		return DefaultMaxInputTokens
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultMaxInputTokens
	}
	if n < 0 {
		return 0
	}
	return n
}

// FitToUpstreamLimit 把转好的请求就地压进上限，返回 (丢弃的条数, 是否装得下)。
//
// 只在超限时才动，且只丢旧历史：首条（system / 工具约定注入的锚点）和最新的几条必须留下 ——
// 丢最新的等于把这一轮的问题也丢了。首条自己就超上限时不截断它：截掉一半的 system 提示
// 比明确报错更坏，回 false 让调用方给一个 typed 400。
//
// ponytail: 每条消息的 token 只数一遍，O(n)；不引入滑动窗口/摘要，等真有长会话需求再说。
func FitToUpstreamLimit(req *duckgotypes.ApiRequest) (int, bool) {
	limit := MaxInputTokens()
	if limit <= 0 || req == nil || req.MessageCount() == 0 {
		return 0, true
	}

	n := req.MessageCount()
	tok := make([]int, n)
	total := 0
	for i := 0; i < n; i++ {
		tok[i] = util.CountToken(req.MessageText(i))
		total += tok[i]
	}
	if total <= limit {
		return 0, true
	}
	if n == 1 || tok[0] > limit {
		return 0, false
	}

	// 保留首条 + 最新 k 条：找最大的、装得下的 k。
	sum, k := 0, 0
	for j := n - 1; j >= 1; j-- {
		if tok[0]+sum+tok[j] > limit {
			break
		}
		sum += tok[j]
		k++
	}
	if k == 0 {
		return 0, false
	}
	dropped := n - k - 1
	for i := 0; i < dropped; i++ {
		req.DropMessage(1)
	}
	return dropped, true
}
