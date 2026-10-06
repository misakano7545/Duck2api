package official

import (
	"encoding/json"
	"testing"
	"time"
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

// created 必须是真实时间戳（原先硬编码 0 = 1970），流式分片与非流式都要有。
func TestCreatedIsRealTimestamp(t *testing.T) {
	now := time.Now().Unix()
	check := func(name string, got int64) {
		if got < now-60 || got > now+60 {
			t.Errorf("%s created=%d，不是当前时间（now=%d）", name, got, now)
		}
	}
	check("chat 非流式", NewChatCompletionFull("x", "m", 1, 1, 0, 0, 0, "").Created)
	check("chat 流式分片", NewChatCompletionChunkWithModel("x", "m").Created)
	check("chat 流式收尾分片", StopChunkWithModel("stop", "m").Created)
	check("chat 工具调用", NewChatCompletionToolCalls("m", nil, 1, 1, 0, 0).Created)
	check("responses 非流式", NewResponseAPIFull("x", "m", 1, 1, 0, 0, 0, "").CreatedAt)
	check("responses 简版", NewResponseAPIWithModel("x", "m").CreatedAt)
}
