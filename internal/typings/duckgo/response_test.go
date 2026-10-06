package duckgo

import "testing"

func TestGetImageGenPrompt(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"出图调用", `{"imageGenPrompt":"一只橘猫，柔和自然光，写实风格"}`, "一只橘猫，柔和自然光，写实风格"},
		{"无入参", ``, ""},
		{"非 JSON", `not-json`, ""},
		{"别的工具入参", `{"query":"天气"}`, ""},
	}
	for _, c := range cases {
		r := &ApiResponse{ToolArguments: c.raw}
		if got := r.GetImageGenPrompt(); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
