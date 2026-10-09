//go:build audit

package duckgo

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	duckgotypes "aurora/internal/typings/duckgo"

	"github.com/gin-gonic/gin"
)

const auditCall = `<tool_call>{"name":"exec","arguments":{"input":"date"}}</tool_call>`

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// P1 回归：闸门原来看的只是「第一段可见输出」。模型先吐半句人话再给调用是常态，
// 那样会被永久判成「不是工具调用」—— 标记当普通正文漏出去，那一轮 tool_calls 直接为空。
// 现在判定是滑窗的：缓冲里出现完整标记就切到扣住。
func TestAuditGateKeepsNonLeadingToolCall(t *testing.T) {
	// 1) 标记跨分块到达（最容易被切坏的情形）
	gate := NewStreamGate(true)
	if emit, hold := gate.Push("好的，我来查一下。"); hold || emit != "好的，我来查一下。" {
		t.Fatalf("前置正文应原样下发，得到 hold=%v emit=%q", hold, emit)
	}
	half := len(toolCallOpen) - 1
	if emit, hold := gate.Push(auditCall[:half]); !hold || emit != "" {
		t.Fatalf("标记前缀必须扣住（否则标记会被切断），得到 hold=%v emit=%q", hold, emit)
	}
	if emit, hold := gate.Push(auditCall[half:]); !hold || emit != "" {
		t.Fatalf("标记补齐后应保持扣住，得到 hold=%v emit=%q", hold, emit)
	}
	if !gate.Holding() {
		t.Fatal("应处于扣住状态")
	}
	calls := ParseToolCalls(gate.Buffered())
	if len(calls) != 1 || calls[0].Name != "exec" {
		t.Fatalf("应解析出 1 个调用，得到 %+v (buffer=%q)", calls, gate.Buffered())
	}

	// 2) 普通聊天不受影响：不含标记时逐块透传
	plain := NewStreamGate(true)
	var got strings.Builder
	for _, s := range []string{"你", "好", "，世界"} {
		if emit, hold := plain.Push(s); !hold {
			got.WriteString(emit)
		}
	}
	if got.String() != "你好，世界" || plain.Holding() {
		t.Fatalf("普通流式被改动: %q holding=%v", got.String(), plain.Holding())
	}

	// 3) 端到端（流式）：正文 + 标记同一条 data 里，客户端必须收到 tool_calls
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	sse := "data: " + mustJSON(map[string]any{
		"action": "success", "model": "upstream-model",
		"message": "好的，我来查一下。" + auditCall,
	}) + "\n\ndata: [DONE]\n\n"

	req := duckgotypes.NewApiRequest("client-model")
	result := Handler(c, &http.Response{Body: io.NopCloser(strings.NewReader(sse))},
		req, true, HandlerStats{Start: time.Now(), Tools: true})

	out := w.Body.String()
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "exec" {
		t.Fatalf("端到端应识别出 1 个调用，得到 %+v\n--- 响应 ---\n%s", result.ToolCalls, out)
	}
	if !strings.Contains(out, `"tool_calls"`) || !strings.Contains(out, `"finish_reason":"tool_calls"`) {
		t.Fatalf("响应体缺 tool_calls / finish_reason，得到\n%s", out)
	}
	if strings.Contains(out, "<tool_call>") {
		t.Fatalf("标记不该作为正文漏给客户端，得到\n%s", out)
	}
}

// P1 回归：Token / FEVersion 原先是「首次调用时在锁外惰性赋值」（-race 实测 request.go:51
// 写 vs :50/:56 读）。现在在声明处初始化，所以不存在 nil 窗口 —— 并发调用只能落到锁上。
func TestAuditTokenGlobalsPreinitialized(t *testing.T) {
	if Token == nil || FEVersion == nil {
		t.Fatal("包级 Token/FEVersion 必须声明处就初始化，否则并发首请求会在锁外竞态写全局指针")
	}
	// 零值 Mutex 必须可用（若被 struct 字面量覆盖会 panic）
	Token.M.Lock()
	Token.M.Unlock()
	FEVersion.M.Lock()
	FEVersion.M.Unlock()

	// 并发经过锁的路径不应触发竞态（配合 -race 运行）。读也必须持锁 —— 裸读 Token.Token
	// 会与 ResetXVQD 的写入竞态（这正是第一次跑 -race 时在本测试里报出来的）。
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ResetXVQD()
			Token.M.Lock()
			_ = Token.Token
			Token.M.Unlock()
		}()
	}
	wg.Wait()
}

// 仍然开放（本轮未修）：同一个 200 响应里，正文分片带的是上游回传的模型名，
// 收尾分片带的是客户端请求的模型名。对 Hermes/Codex 只是装饰性问题，按 model 字段
// 做匹配的客户端会困惑。留着是为了别让它被忘掉。
func TestAuditStreamChunkModelInconsistent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	sse := strings.Join([]string{
		`data: {"action":"success","message":"你好","model":"upstream-model"}`,
		`data: {"action":"success","message":"世界","model":"upstream-model"}`,
		`data: [DONE]`,
	}, "\n\n") + "\n\n"

	req := duckgotypes.NewApiRequest("client-model")
	result := Handler(c, &http.Response{Body: io.NopCloser(strings.NewReader(sse))},
		req, true, HandlerStats{Start: time.Now(), PromptTokens: 1})

	var models []string
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.HasPrefix(line, "data: [DONE]") {
			continue
		}
		var ch struct {
			Model string `json:"model"`
		}
		if json.Unmarshal([]byte(line[6:]), &ch) == nil && ch.Model != "" {
			models = append(models, ch.Model)
		}
	}
	if len(models) < 2 {
		t.Fatalf("预期至少 2 个分片，得到 %v", models)
	}
	first, last := models[0], models[len(models)-1]
	if first == last {
		t.Skipf("模型名已一致（%s），本项已修复", first)
	}
	if first != "upstream-model" || last != "client-model" {
		t.Fatalf("与预期不符: %v", models)
	}
	if result.Text != "你好世界" {
		t.Fatalf("文本收集异常: %q", result.Text)
	}
	t.Logf("仍不一致（本轮未修）: 正文=%q 收尾=%q", first, last)
}
