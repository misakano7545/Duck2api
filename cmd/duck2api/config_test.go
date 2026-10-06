package main

import (
	"os"
	"path/filepath"
	"testing"
)

// 环境变量优先于 config.json；文件里独有的键回填；非法类型跳过；缺文件不报错。
func TestLoadConfigFile(t *testing.T) {
	unset := func(k string) {
		old, had := os.LookupEnv(k)
		os.Unsetenv(k)
		t.Cleanup(func() {
			if had {
				os.Setenv(k, old)
			}
		})
	}
	unset("PREFIX")
	unset("TLS_CERT")
	unset("SERVER_PORT")
	unset("X_VQD_ORIGIN")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{
	  "PREFIX": "/fromfile",
	  "TLS_CERT": "/tmp/cert.pem",
	  "SERVER_PORT": 9001,
	  "X_VQD_ORIGIN": {"nested": "should be skipped"}
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PREFIX", "/fromenv") // 已存在的键：文件不得覆盖
	loadConfigFile(path)

	if got := os.Getenv("PREFIX"); got != "/fromenv" {
		t.Fatalf("PREFIX = %q, want /fromenv (环境变量优先)", got)
	}
	if got := os.Getenv("TLS_CERT"); got != "/tmp/cert.pem" {
		t.Fatalf("TLS_CERT = %q, want /tmp/cert.pem (文件回填)", got)
	}
	if got := os.Getenv("SERVER_PORT"); got != "9001" {
		t.Fatalf("SERVER_PORT = %q, want 9001 (JSON 数字转字符串)", got)
	}
	if _, exists := os.LookupEnv("X_VQD_ORIGIN"); exists {
		t.Fatal("嵌套对象应被跳过，不能设成垃圾字符串")
	}

	// 缺文件是正常情况（Docker/Koyeb 只给环境变量）
	loadConfigFile(filepath.Join(dir, "nope.json"))
}
