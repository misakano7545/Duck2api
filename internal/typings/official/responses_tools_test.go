package official

import (
	"encoding/json"
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
