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

// 自由格式工具(Codex 的 exec, type=="custom"): 没有参数表, 标 [原始文本输入],
// 描述要多留(那是它唯一的 API 文档), 并在约定里告诉模型原始文本放哪。
func TestCompactToolListCustom(t *testing.T) {
	tools := []interface{}{
		map[string]interface{}{"type": "custom", "name": "exec",
			"description": "Run JavaScript code to orchestrate tool calls. " + strings.Repeat("x", 600),
			"format":      map[string]interface{}{"type": "grammar", "syntax": "lark", "definition": "SOURCE: /[\\s\\S]+/"}},
		map[string]interface{}{"type": "function", "name": "wait",
			"parameters": map[string]interface{}{"type": "object",
				"properties": map[string]interface{}{"cell_id": map[string]interface{}{"type": "string"}},
				"required":   []interface{}{"cell_id"}}},
	}
	doc := CompactToolList(tools)
	if !strings.Contains(doc, "exec[原始文本输入]") {
		t.Fatalf("custom 标记缺失: %q", doc)
	}
	if strings.Contains(doc, "exec(") {
		t.Fatalf("custom 不该有参数表: %q", doc)
	}
	if !strings.Contains(doc, "wait(cell_id:string*)") {
		t.Fatalf("function 压缩错: %q", doc)
	}
	// 描述留得比普通工具多，但也得有上限（不能把 600 字全带上）
	customLine := strings.SplitN(doc, "\n", 2)[0]
	if n := len([]rune(customLine)); n < 200 || n > 520 {
		t.Fatalf("custom 描述长度 %d 不在预期区间", n)
	}

	// 有 custom 时必须交代原始文本入参
	withCustom := ToolInstruction(doc, true)
	if !strings.Contains(withCustom, "[原始文本输入]") || !strings.Contains(withCustom, `"input"`) {
		t.Fatalf("缺原始文本入参约定: %q", withCustom[:400])
	}
	if strings.Contains(ToolInstruction(doc, false), "没有参数表") {
		t.Fatal("没有 custom 时不该出现那段约定")
	}
}

// 解析结果回写成 Responses API 的 function_call 项: call_id 必须有(客户端拿它回填
// function_call_output), arguments 原样保留, 并行调用各占一项。
func TestResponsesToolCalls(t *testing.T) {
	items := ResponsesToolCalls([]ToolCall{
		{Name: "shell", Arguments: `{"cmd":"ls"}`},
		{Name: "read", Arguments: "{}"},
	}, nil)
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
	if got := ResponsesToolCalls(nil, nil); len(got) != 0 {
		t.Fatalf("空输入应得空输出, got %+v", got)
	}
}

// 自由格式工具(Codex 的 exec)必须回成 custom_tool_call + input 原始文本:
// 客户端按 item 类型分派, 回成 function_call 它不认。
func TestResponsesToolCallsCustomTool(t *testing.T) {
	items := ResponsesToolCalls([]ToolCall{
		{Name: "exec", Arguments: `{"input":"const r = await tools.exec_command({cmd:\"date\"}); text(r)"}`},
		{Name: "shell", Arguments: `{"cmd":"ls"}`},
	}, map[string]bool{"exec": true})

	if len(items) != 2 {
		t.Fatalf("got %d", len(items))
	}
	if items[0].Type != "custom_tool_call" {
		t.Fatalf("exec 应为 custom_tool_call, got %q", items[0].Type)
	}
	if items[0].Input == "" || items[0].Arguments != "" {
		t.Fatalf("custom 应当只有 input: %+v", items[0])
	}
	if !strings.Contains(items[0].Input, "exec_command") {
		t.Fatalf("input 未原样保留: %q", items[0].Input)
	}
	if !strings.HasPrefix(items[0].ID, "ctc_") {
		t.Fatalf("custom id 前缀: %q", items[0].ID)
	}
	if items[1].Type != "function_call" || items[1].Input != "" {
		t.Fatalf("普通工具不该受影响: %+v", items[1])
	}

	// 模型没按约定包 input 字段时，也要把原文当成 input，而不是回空脚本
	raw := ResponsesToolCalls([]ToolCall{{Name: "exec", Arguments: "console.log(1)"}}, map[string]bool{"exec": true})
	if raw[0].Input != "console.log(1)" {
		t.Fatalf("裸文本兜底失败: %+v", raw[0])
	}
}
