//go:build audit

package duckgo

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	crand "crypto/rand"

	duckgotypes "aurora/internal/typings/duckgo"
	officialtypes "aurora/internal/typings/official"
)

func auditTools() interface{} {
	return []interface{}{
		map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "exec",
				"description": "run a shell command",
			},
		},
	}
}

// P2 回归：上限守卫原来只数 type=="text" 的 part（MessageContent.TextContent），图片 part
// 的 base64 原文对计数完全隐形 —— 带图的请求在守卫眼里是「空请求」，一路放行后在上游撞
// ERR_INPUT_LIMIT，白等一个确定性 400。现在守卫数 CountableText（文本 + 图片原文）。
func TestAuditImagePartsCountTowardFitGuard(t *testing.T) {
	// 用随机字节的 base64 造图，避免「单字符重复」这种病态输入干扰计数耗时对照。
	raw := make([]byte, 30*1024)
	crand.Read(raw)
	blob := base64.StdEncoding.EncodeToString(raw) // 40KB，图片正文的真实量级

	withImage := duckgotypes.NewApiRequest("m")
	withImage.KeepPrefix = 1
	withImage.AddMessageWithParts("user", []duckgotypes.ContentPart{
		{Type: "text", Text: "看看这张图"},
		{Type: "image", Image: blob, MimeType: "image/png"},
	})

	t.Setenv("MAX_INPUT_TOKENS", "100") // 极小上限：任何真实体积都该被拦
	dropped, ok := FitToUpstreamLimit(&withImage)
	payload, _ := json.Marshal(withImage)
	t.Logf("带 %d KB 图片: 守卫数到 %d 字节, dropped=%d, ok=%v（请求体实际 %d KB）",
		len(blob)/1024, countAllText(&withImage), dropped, ok, len(payload)/1024)
	if len(payload) < 30*1024 {
		t.Fatalf("测试自身失效：payload 只有 %d 字节", len(payload))
	}
	// 受保护前缀（KeepPrefix=1 那条 = 唯一一条消息）自己就超限 ⇒ 只能拒绝，不能裁。
	if ok {
		t.Fatal("带图的请求仍能绕过守卫 —— 图片没有被计入计数")
	}

	// 对照：纯文本同样体积，行为必须一致（都是拒绝）
	asText := duckgotypes.NewApiRequest("m")
	asText.KeepPrefix = 1
	asText.AddMessage("user", blob)
	_, ok2 := FitToUpstreamLimit(&asText)
	if ok2 {
		t.Fatal("对照项行为不一致")
	}
}

// P2 回归：MAX_INPUT_TOKENS=0 原先是「关闭守卫」，与注释/README（都说负数才关）矛盾 ——
// 照注释配 0 的人会静默失去保护。现在 0 = 默认，负数 = 关闭。
func TestAuditMaxInputTokensZeroMeansDefault(t *testing.T) {
	t.Setenv("MAX_INPUT_TOKENS", "0")
	if got := MaxInputTokens(); got != DefaultMaxInputTokens {
		t.Fatalf("0 应按默认（%d），得到 %d", DefaultMaxInputTokens, got)
	}
	t.Setenv("MAX_INPUT_TOKENS", "-1")
	if got := MaxInputTokens(); got != 0 {
		t.Fatalf("负数应关闭守卫（0），得到 %d", got)
	}
	t.Setenv("MAX_INPUT_TOKENS", "abc")
	if got := MaxInputTokens(); got != DefaultMaxInputTokens {
		t.Fatalf("非法值应按默认，得到 %d", got)
	}
	t.Setenv("MAX_INPUT_TOKENS", "1234")
	if got := MaxInputTokens(); got != 1234 {
		t.Fatalf("显式值应生效，得到 %d", got)
	}
}

func countAllText(req *duckgotypes.ApiRequest) int {
	n := 0
	for i := 0; i < req.MessageCount(); i++ {
		n += len(req.MessageText(i))
	}
	return n
}

// 审计用：工具约定的插入点是「第二个产出 parts 的消息之前」。首条消息为空时注入位置后移一位，
// KeepPrefix 随之变大，一条真实内容被纳入受保护前缀。影响不大（不是先前怀疑的「污染成全部」），
// 但与注释「紧随首条消息」的说法不符。
func TestAuditToolInstructionInsertionPoint(t *testing.T) {
	req := officialtypes.APIRequest{
		Model: "gpt-5.6-luna",
		Tools: auditTools(),
		Messages: []officialtypes.ApiMessage{
			{Role: "user", Content: ""}, // 首条为空
			{Role: "user", Content: "第二条"},
			{Role: "assistant", Content: "第三条"},
		},
	}
	out := ConvertAPIRequestWithOptions(req, "", false)

	pos := -1
	for i := 0; i < out.MessageCount(); i++ {
		if strings.Contains(out.MessageText(i), "API 线格式约定") {
			pos = i
			break
		}
	}
	t.Logf("首条为空时: 插入位置 index=%d  KeepPrefix=%d  总条数=%d", pos, out.KeepPrefix, out.MessageCount())
	if pos != 1 {
		t.Fatalf("预期落在 index 1，得到 %d", pos)
	}
	if out.KeepPrefix != 2 {
		t.Fatalf("预期 KeepPrefix=2，得到 %d", out.KeepPrefix)
	}

	req2 := officialtypes.APIRequest{
		Model: "gpt-5.6-luna",
		Tools: auditTools(),
		Messages: []officialtypes.ApiMessage{
			{Role: "system", Content: "你是助手"},
			{Role: "user", Content: "你好"},
		},
	}
	out2 := ConvertAPIRequestWithOptions(req2, "", false)
	t.Logf("对照（首条非空）: KeepPrefix=%d 总条数=%d 首条=%q",
		out2.KeepPrefix, out2.MessageCount(), out2.MessageText(0))
	if out2.KeepPrefix != 2 {
		t.Fatalf("对照项预期 KeepPrefix=2，得到 %d", out2.KeepPrefix)
	}
}
