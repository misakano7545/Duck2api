package duckgo

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// 别名归一：C2PA 里能看到的名字也要能当 model 传（gpt-image-1.5 → 原生、gpt-image-2 → 工具路径）。
func TestResolveImageModel(t *testing.T) {
	cases := []struct {
		in       string
		native   bool
		wantReal string
	}{
		{"", true, NativeImageModel},
		{"image-generation", true, NativeImageModel},
		{"gpt-image-1.5", true, NativeImageModel},
		{"gpt-image-1", true, NativeImageModel},
		{"GPT-Image-2", false, ToolImageChatModel},
		{"gpt-image-2", false, ToolImageChatModel},
		{"  gpt-image-2  ", false, ToolImageChatModel},
		{"gpt-5.6-luna", false, "gpt-5.6-luna"},
		{"mistral-small-2603", false, "mistral-small-2603"},
		// 没有可请求的 id：直传上游只会 404，别名表不编造映射，原样透出。
		{"dall-e-3", false, "dall-e-3"},
	}
	for _, c := range cases {
		native, real := ResolveImageModel(c.in)
		if native != c.native || real != c.wantReal {
			t.Errorf("ResolveImageModel(%q) = (%v,%q)，期望 (%v,%q)", c.in, native, real, c.native, c.wantReal)
		}
	}
}

// 严格指令必须把用户原文整段带上，且只此一段（不能截断、不能加风格词）。
func TestStrictImagePrompt(t *testing.T) {
	p := "一只柯基在沙滩上奔跑"
	got := StrictImagePrompt(p)
	if !strings.Contains(got, p) {
		t.Fatalf("严格指令丢了原文: %q", got)
	}
	if !strings.HasSuffix(got, p) {
		t.Errorf("原文应紧跟在描述标记之后，实际结尾: %q", got[len(got)-20:])
	}
	if strings.Count(got, p) != 1 {
		t.Errorf("原文出现 %d 次，应恰好 1 次", strings.Count(got, p))
	}
	if StrictImagePrompt("") == "" {
		t.Error("空提示词也要给出指令骨架，而不是空串")
	}
}

// 废稿（partial）不能当输出图返回：只有成品算图，废稿仅在拿不到成品时兜底。
func TestReadImageResponseSkipsDrafts(t *testing.T) {
	cases := []struct {
		name     string
		sse      []string
		wantImgs int
		wantB64  string
	}{
		{
			name: "原生模型：废稿+成品",
			sse: []string{
				`data: {"action":"success","role":"partial-image","result":"/9j/PARTIAL"}`,
				`data: {"action":"success","role":"generated-image","result":"/9j/FINAL"}`,
			},
			wantImgs: 1, wantB64: "/9j/FINAL",
		},
		{
			name: "工具路径：status=partial + status=success",
			sse: []string{
				`data: {"action":"success","role":"ui-component","toolName":"GenerateImage","data":{"status":"partial","b64Image":"/9j/TOOLDRAFT"}}`,
				`data: {"action":"success","role":"ui-component","toolName":"GenerateImage","data":{"status":"success","b64Image":"/9j/TOOLFINAL"}}`,
			},
			wantImgs: 1, wantB64: "/9j/TOOLFINAL",
		},
		{
			name: "废稿先到、成品后到",
			sse: []string{
				`data: {"action":"success","role":"partial-image","result":"/9j/DRAFT2"}`,
				`data: {"action":"success","role":"generated-image","result":"/9j/FINAL2"}`,
			},
			wantImgs: 1, wantB64: "/9j/FINAL2",
		},
		{
			name: "两行成品 → 两张图",
			sse: []string{
				`data: {"action":"success","role":"generated-image","result":"/9j/A"}`,
				`data: {"action":"success","role":"generated-image","result":"/9j/B"}`,
			},
			wantImgs: 2, wantB64: "/9j/A",
		},
		{
			name: "全是废稿 → 兜底返回废稿",
			sse: []string{
				`data: {"action":"success","role":"partial-image","result":"/9j/ONLYDRAFT"}`,
			},
			wantImgs: 1, wantB64: "/9j/ONLYDRAFT",
		},
	}
	for _, c := range cases {
		body := strings.Join(c.sse, "\n") + "\ndata: [DONE]\n"
		resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
		got := ReadImageResponse(resp)
		if len(got.Images) != c.wantImgs {
			t.Errorf("%s: 图片数 %d, 期望 %d", c.name, len(got.Images), c.wantImgs)
			continue
		}
		if c.wantImgs > 0 && got.Images[0].Result != c.wantB64 {
			t.Errorf("%s: 首图 %q, 期望 %q", c.name, got.Images[0].Result, c.wantB64)
		}
	}
}
