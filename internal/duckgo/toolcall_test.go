package duckgo

import (
	"encoding/json"
	"strings"
	"testing"
)

// 工具调用的解析 + 流式闸门: 契约要求"要调用时只输出 <tool_call>...</tool_call>",
// 闸门必须在开头就能判定, 且普通文本的流式不能被扣住。
func TestParseToolCalls(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []ToolCall
	}{
		{"none", "今天北京晴", nil},
		{"single", `<tool_call>{"name":"get_weather","arguments":{"city":"北京"}}</tool_call>`, []ToolCall{{Name: "get_weather", Arguments: `{"city":"北京"}`}}},
		{"with prose around", "好的\n<tool_call>{\"name\":\"a\",\"arguments\":{}}</tool_call>\n", []ToolCall{{Name: "a", Arguments: "{}"}}},
		{"no arguments field", `<tool_call>{"name":"ping"}</tool_call>`, []ToolCall{{Name: "ping", Arguments: "{}"}}},
		{"parallel", `<tool_call>{"name":"a","arguments":{"x":1}}</tool_call><tool_call>{"name":"b","arguments":{"y":2}}</tool_call>`,
			[]ToolCall{{Name: "a", Arguments: `{"x":1}`}, {Name: "b", Arguments: `{"y":2}`}}},
		{"broken json skipped", `<tool_call>{"name":</tool_call>`, nil},
		{"unterminated", `<tool_call>{"name":"a"}`, nil},
	}
	for _, tc := range cases {
		got := ParseToolCalls(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: [%d] got %+v want %+v", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

func TestStreamGate(t *testing.T) {
	// 没带工具定义: 一律原样透传, 不扣
	g := NewStreamGate(false)
	if emit, hold := g.Push("随便什么"); hold || emit != "随便什么" {
		t.Fatalf("gate off: emit=%q hold=%v", emit, hold)
	}

	// 普通文本: 前几块先扣住判断, 确认非工具后补发, 之后逐块透传
	g = NewStreamGate(true)
	var out string
	for _, chunk := range []string{"今", "天", "北京", "晴"} {
		emit, _ := g.Push(chunk)
		out += emit
	}
	if out != "今天北京晴" {
		t.Fatalf("text path: %q", out)
	}
	if g.Holding() {
		t.Fatal("text path must not hold")
	}

	// 工具调用: 全部扣住, 收尾解析出调用
	g = NewStreamGate(true)
	var released string
	for _, chunk := range []string{"<tool", `_call>{"name":"get_weather",`, `"arguments":{"city":"北京"}}</tool_call>`} {
		emit, _ := g.Push(chunk)
		released += emit
	}
	if released != "" {
		t.Fatalf("tool path must not release text, got %q", released)
	}
	if !g.Holding() {
		t.Fatal("tool path must hold")
	}
	calls := ParseToolCalls(g.Buffered())
	if len(calls) != 1 || calls[0].Name != "get_weather" || calls[0].Arguments != `{"city":"北京"}` {
		t.Fatalf("parsed %+v", calls)
	}
}

// 工具块是我们注入的、也是提示词里最大的一块：必须压得动，且三种形状都要认。
func TestCompactToolList(t *testing.T) {
	weather := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"city": map[string]interface{}{"type": "string", "description": "城市名" + strings.Repeat("x", 200)},
			"unit": map[string]interface{}{"type": "string"},
		},
		"required":             []interface{}{"city"},
		"additionalProperties": false,
	}

	cases := []struct {
		name  string
		tools interface{}
	}{
		{"chat 嵌套", []interface{}{map[string]interface{}{
			"type":     "function",
			"function": map[string]interface{}{"name": "get_weather", "description": "查天气", "parameters": weather}}}},
		{"responses 平铺", []interface{}{map[string]interface{}{
			"type": "function", "name": "get_weather", "description": "查天气", "parameters": weather}}},
		{"anthropic", []interface{}{map[string]interface{}{
			"name": "get_weather", "description": "查天气", "input_schema": weather}}},
	}
	for _, tc := range cases {
		got := CompactToolList(tc.tools)
		if !strings.Contains(got, "get_weather(city:string*, unit:string)") {
			t.Fatalf("%s: %q", tc.name, got)
		}
		if !strings.Contains(got, "查天气") {
			t.Fatalf("%s: 描述丢了: %q", tc.name, got)
		}
		// 描述要截断：原描述 200+ 字, 不能整段带上去
		if len([]rune(got)) > 200 {
			t.Fatalf("%s: 没压住 (%d runes): %q", tc.name, len([]rune(got)), got)
		}
	}

	// 同一请求两次生成的提示必须一致（map 遍历无序, 不排序就会每次不同）
	once, twice := CompactToolList(cases[0].tools), CompactToolList(cases[0].tools)
	if once != twice {
		t.Fatalf("不稳定:\n%s\n%s", once, twice)
	}
	// 认不出的形状退回原 JSON, 不能把工具丢了
	raw := `[{"weird":true,"name":"x"}]`
	var parsed interface{}
	_ = json.Unmarshal([]byte(raw), &parsed)
	if got := CompactToolList(parsed); !strings.Contains(got, "x") {
		t.Fatalf("退回失败: %q", got)
	}
}

// 解析结果回写成 Responses API 的 function_call 项: call_id 必须有(客户端拿它回填
// function_call_output), arguments 原样保留, 并行调用各占一项。
func TestResponsesToolCalls(t *testing.T) {
	items := ResponsesToolCalls([]ToolCall{
		{Name: "shell", Arguments: `{"cmd":"ls"}`},
		{Name: "read", Arguments: "{}"},
	})
	if len(items) != 2 {
		t.Fatalf("got %d items", len(items))
	}
	for i, item := range items {
		if item.Type != "function_call" {
			t.Fatalf("[%d] type = %q", i, item.Type)
		}
		if item.CallID == "" || item.ID == "" {
			t.Fatalf("[%d] missing ids: %+v", i, item)
		}
		if item.Content != nil || item.Role != "" {
			t.Fatalf("[%d] function_call 不该有 content/role: %+v", i, item)
		}
	}
	if items[0].Name != "shell" || items[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("arguments 未原样保留: %+v", items[0])
	}
	if items[0].CallID == items[1].CallID {
		t.Fatal("call_id 必须各不相同")
	}
	if got := ResponsesToolCalls(nil); len(got) != 0 {
		t.Fatalf("空输入应得空输出, got %+v", got)
	}
}
