package initialize

import (
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 三种入站协议各自的路径都要在路由表里（/messages 是 Anthropic 客户端常见的裸别名）。
func TestRoutesCoverThreeProtocols(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	want := []string{
		"POST /v1/chat/completions",
		"POST /v1/responses",
		"POST /v1/messages",
		"POST /messages",
	}
	got := map[string]bool{}
	for _, r := range RegisterRouter().Routes() {
		got[r.Method+" "+r.Path] = true
	}
	var missing []string
	for _, w := range want {
		if !got[w] {
			missing = append(missing, w)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("缺路由: %s", strings.Join(missing, ", "))
	}
}
