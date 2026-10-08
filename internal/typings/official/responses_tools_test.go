package official

import (
	"encoding/json"
	"strings"
	"testing"
)

// 走 responses 路的客户端(Codex CLI / Hermes codex_responses)靠这条透传才有工具能力;
// 漏掉就是把工具定义静默丢掉, 模型只会回"我不能执行命令"。
func TestToChatCompletionForwardsTools(t *testing.T) {
	tools := []interface{}{
		map[string]interface{}{"type": "function", "name": "shell", "parameters": map[string]interface{}{"type": "object"}},
	}
	req := ResponseAPIRequest{
		Model:      "gpt-5.6-luna",
		Input:      "跑 ls",
		Tools:      tools,
		ToolChoice: "auto",
	}

	chat := req.ToChatCompletionRequest()
	if chat.Tools == nil {
		t.Fatal("tools dropped: responses 路的工具能力会静默消失")
	}
	if got := len(chat.Tools.([]interface{})); got != 1 {
		t.Fatalf("tools len = %d, want 1", got)
	}
	if chat.ToolChoice != "auto" {
		t.Fatalf("tool_choice = %v, want auto", chat.ToolChoice)
	}
	// instructions 仍要作为首条 system 消息带上去
	req.Instructions = "你是助手"
	withSys := req.ToChatCompletionRequest()
	if len(withSys.Messages) != 2 || withSys.Messages[0].Role != "system" {
		t.Fatalf("messages = %+v", withSys.Messages)
	}
}

// 多轮 loop 的闭合条件: 第二轮的 input 里助手那次的调用和工具结果都要留在历史里,
// 少一个模型就会把同一个调用再发一遍(实测就是这么挂的)。
func TestResponseInputFoldsToolCallHistory(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{"role": "user", "content": "跑 ls"},
		map[string]interface{}{"type": "function_call", "call_id": "call_1", "name": "shell", "arguments": `{"cmd":"ls"}`},
		map[string]interface{}{"type": "function_call_output", "call_id": "call_1", "output": "README.md"},
	}
	msgs := responseInputMessages(input)
	if len(msgs) != 3 {
		t.Fatalf("got %d messages: %+v", len(msgs), msgs)
	}
	// 自定义工具的两轮：custom_tool_call 带原文 + custom_tool_call_output
	custom := responseInputMessages([]interface{}{
		map[string]interface{}{"type": "custom_tool_call", "call_id": "c1", "name": "exec", "input": "text(await tools.exec_command({cmd:\"date\"}))"},
		map[string]interface{}{"type": "custom_tool_call_output", "call_id": "c1", "output": "Thu Oct 8"},
	})
	if len(custom) != 2 {
		t.Fatalf("custom 历史折叠丢东西: %+v", custom)
	}
	if custom[0].Role != "assistant" || !strings.Contains(custom[0].Content.(string), "exec_command") {
		t.Fatalf("custom_tool_call 没留住原文: %+v", custom[0])
	}
	if custom[1].Role != "tool" || custom[1].Content != "Thu Oct 8" {
		t.Fatalf("custom_tool_call_output 没留住: %+v", custom[1])
	}
	if msgs[0].Role != "user" {
		t.Fatalf("[0] role = %q", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" || !strings.Contains(msgs[1].Content.(string), "shell") ||
		!strings.Contains(msgs[1].Content.(string), `{"cmd":"ls"}`) {
		t.Fatalf("assistant 那次的调用丢了: %+v", msgs[1])
	}
	// role=tool: 转换层据此加 [工具结果] 前缀
	if msgs[2].Role != "tool" || msgs[2].Content != "README.md" {
		t.Fatalf("[2] = %+v", msgs[2])
	}
	// 参数是对象(非字符串)时也要看得出调用了什么
	obj := responseInputMessages([]interface{}{
		map[string]interface{}{"type": "function_call", "name": "shell", "arguments": map[string]interface{}{"cmd": "ls"}},
	})
	if len(obj) != 1 || !strings.Contains(obj[0].Content.(string), "cmd") {
		t.Fatalf("对象参数未回退序列化: %+v", obj)
	}
}

// Codex CLI 不发顶层 tools —— 工具藏在 input 的 additional_tools 项里，还套 namespace。
// 只读顶层字段就没工具可用；custom 类型还要一路带到回写那一步。
func TestCollectedToolsFromAdditionalTools(t *testing.T) {
	req := ResponseAPIRequest{
		Model: "gpt-5.6-luna",
		Input: []interface{}{
			map[string]interface{}{
				"type": "additional_tools", "role": "developer",
				"tools": []interface{}{map[string]interface{}{
					"type": "namespace", "name": "functions",
					"tools": []interface{}{
						map[string]interface{}{"type": "custom", "name": "exec", "description": "Run JavaScript",
							"format": map[string]interface{}{"type": "grammar", "syntax": "lark", "definition": "SOURCE: /[\\s\\S]+/"}},
						map[string]interface{}{"type": "function", "name": "wait",
							"parameters": map[string]interface{}{"type": "object",
								"properties": map[string]interface{}{"cell_id": map[string]interface{}{"type": "string"}},
								"required":   []interface{}{"cell_id"}}},
					}}},
			},
			map[string]interface{}{"type": "message", "role": "user", "content": "run ls"},
		},
	}

	tools := req.CollectedTools()
	if len(tools) != 2 {
		t.Fatalf("got %d tools: %+v", len(tools), tools)
	}
	if got := req.CustomToolNames(); len(got) != 1 || got[0] != "exec" {
		t.Fatalf("custom names = %v", got)
	}

	chat := req.ToChatCompletionRequest()
	if chat.Tools == nil || len(chat.CustomTools) != 1 {
		t.Fatalf("透传失败: tools=%v custom=%v", chat.Tools, chat.CustomTools)
	}
	// 顶层 tools 照旧也要收（Hermes codex_responses 走这条路）
	top := ResponseAPIRequest{Model: "m", Tools: []interface{}{map[string]interface{}{"type": "function", "name": "terminal"}}}
	if len(top.CollectedTools()) != 1 {
		t.Fatal("顶层 tools 丢了")
	}
}

// function_call 输出项的 JSON 形状必须能被 Responses API 客户端解析:
// type/call_id/name/arguments 齐全, 且不带 message 项的 role/content。
func TestResponseFunctionCallItemJSON(t *testing.T) {
	item := ResponseOutput{
		ID:        "fc_abc",
		Type:      "function_call",
		Status:    "completed",
		CallID:    "call_abc",
		Name:      "shell",
		Arguments: `{"cmd":"ls"}`,
	}
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"type", "call_id", "name", "arguments", "status"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("missing %q in %s", key, raw)
		}
	}
	if got["call_id"] != "call_abc" || got["name"] != "shell" || got["arguments"] != `{"cmd":"ls"}` {
		t.Fatalf("bad payload: %s", raw)
	}
	if _, ok := got["content"]; ok {
		t.Fatalf("function_call 不该带 content: %s", raw)
	}
	if _, ok := got["role"]; ok {
		t.Fatalf("function_call 不该带 role: %s", raw)
	}

	// message 项的形状不能因此退化(客户端已经在收这个)
	msg, _ := json.Marshal(NewResponseOutput("你好"))
	var msgGot map[string]interface{}
	_ = json.Unmarshal(msg, &msgGot)
	for _, key := range []string{"id", "type", "status", "role", "content"} {
		if _, ok := msgGot[key]; !ok {
			t.Fatalf("message 项丢了 %q: %s", key, msg)
		}
	}
}
