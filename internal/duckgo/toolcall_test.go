package duckgo

import "testing"

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
