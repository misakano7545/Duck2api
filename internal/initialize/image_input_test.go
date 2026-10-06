package initialize

import (
	"encoding/base64"
	"testing"
)

// 改图输入既可能是 data URL（JSON 入口），也可能是裸 base64（multipart 入口）。
func TestDecodeImageInput(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 13}
	b64 := func(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

	cases := []struct {
		name     string
		in       string
		wantMime string
		wantLen  int
		wantErr  bool
	}{
		{"data URL 声明 png", "data:image/png;base64," + b64(png), "image/png", len(png), false},
		{"data URL 声明 jpeg", "data:image/jpeg;base64," + b64(jpeg), "image/jpeg", len(jpeg), false},
		{"裸 base64 + 嗅探", b64(jpeg), "image/jpeg", len(jpeg), false},
		{"裸 base64 + 嗅探 png", b64(png), "image/png", len(png), false},
		{"带空白", "  " + b64(jpeg) + "\n", "image/jpeg", len(jpeg), false},
		{"非法 base64", "!!!not-base64!!!", "", 0, true},
		{"空串", "", "", 0, true},
	}
	for _, c := range cases {
		blob, mime, err := decodeImageInput(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: 期望报错，实际 mime=%q len=%d", c.name, mime, len(blob))
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: 意外报错 %v", c.name, err)
			continue
		}
		if mime != c.wantMime || len(blob) != c.wantLen {
			t.Errorf("%s: got (%q,%d)，期望 (%q,%d)", c.name, mime, len(blob), c.wantMime, c.wantLen)
		}
	}
}
