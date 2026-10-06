package duckgo

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

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
