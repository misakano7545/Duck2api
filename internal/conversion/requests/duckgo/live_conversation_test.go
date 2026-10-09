package duckgo

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	dkgo "aurora/internal/duckgo"
	"aurora/internal/httpclient/resty"
	duckgotypes "aurora/internal/typings/duckgo"
	officialtypes "aurora/internal/typings/official"
)

// 实测 duck.ai 是否按 conversationId 在服务端保留上下文。
// 保留的话, 后续轮次就只发增量 —— 这是绕开上游小输入上限的唯一路径,
// 也是 agent 客户端(每轮都重发整个 system prompt + 工具 schema)能不能用的前提。
//
// 需要真实上游, 默认跳过:
//
//	DUCK_LIVE=1 go test ./internal/conversion/requests/duckgo/ -run TestLiveConversationMemory -v
func TestLiveConversationMemory(t *testing.T) {
	if os.Getenv("DUCK_LIVE") == "" {
		t.Skip("set DUCK_LIVE=1 to hit the real upstream")
	}

	client := resty.NewStdClient()
	// 直连身份: 每次跑生成一个新指纹, 与网关启动时的行为一致。
	ua := dkgo.RandomUA()
	token, err := dkgo.InitXVQD(client, "", ua)
	if err != nil {
		t.Skipf("拿不到挑战(多半是限流窗口), 稍后重试: %v", err)
	}

	var firstDS *duckgotypes.DurableStream

	ask := func(question string, reuse *duckgotypes.DurableStream) string {
		req := officialtypes.APIRequest{
			Model:    "gpt-5.6-luna",
			Messages: []officialtypes.ApiMessage{{Role: "user", Content: question}},
		}
		tr := ConvertAPIRequestWithOptions(req, "", false)
		if reuse != nil {
			tr.DurableStream = reuse // 同一会话 + 同一把公钥
		} else {
			firstDS = tr.DurableStream
		}
		resp, err := dkgo.POSTconversation(client, tr, token, "", ua)
		if err != nil {
			t.Fatalf("POSTconversation: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("status %d: %s", resp.StatusCode, string(body)[:200])
		}
		var sb strings.Builder
		rd := bufio.NewReader(resp.Body)
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				break
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var d struct {
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d) == nil {
				sb.WriteString(d.Message)
			}
		}
		return sb.String()
	}

	first := ask("请记住暗号是茄子。只回复两个字：记住", nil)
	t.Logf("turn1: %q", strings.TrimSpace(first))
	if firstDS == nil {
		t.Fatal("没拿到 durableStream")
	}
	t.Logf("conversationId=%s", firstDS.ConversationID)

	// 只发新的一句, 历史(含暗号)不进请求
	second := ask("暗号是什么？只回复暗号本身。", firstDS)
	t.Logf("turn2(只发增量): %q", strings.TrimSpace(second))

	if strings.Contains(second, "茄子") {
		t.Logf(">>> 服务端保留了上下文: 增量轮次可行")
	} else {
		t.Errorf(">>> 未保留上下文: turn2=%q", second)
	}
}
