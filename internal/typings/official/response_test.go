package official

import (
	"encoding/json"
	"testing"
)

// 非流式 chat completion 必须带 finish_reason（OpenAI 客户端按它判断收尾）。
func TestChatCompletionHasFinishReason(t *testing.T) {
	resp := NewChatCompletionFull("收到", "gpt-5.6-luna", 7, 1, 7, 900, 2300, "")
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason=%v，期望 stop", resp.Choices[0].FinishReason)
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	json.Unmarshal(b, &got)
	ch := got["choices"].([]any)[0].(map[string]any)
	if ch["finish_reason"] != "stop" {
		t.Errorf("序列化后 finish_reason=%v，期望 stop", ch["finish_reason"])
	}
	// 工具调用那条路是另一个构造函数，仍应是 tool_calls
	tc := NewChatCompletionToolCalls("gpt-5.6-luna", nil, 1, 1, 0, 0)
	if tc.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("tool_calls 路径 finish_reason=%v", tc.Choices[0].FinishReason)
	}
}
