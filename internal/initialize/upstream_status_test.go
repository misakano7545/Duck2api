package initialize

import (
	"aurora/internal/duckgo"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// 限速(取不到挑战)必须报 429 + Retry-After, 其它错误才是 500。
func TestUpstreamStatusRateLimited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	if got := upstreamStatus(c, duckgo.ErrChallengeUnavailable); got != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", got)
	}
	if got := c.Writer.Header().Get("Retry-After"); got == "" {
		t.Fatal("Retry-After header missing on 429")
	}
	// 包装过的错误也要认出来 (handler 里是 %w 包装的)
	if got := upstreamStatus(c, wrapped()); got != http.StatusTooManyRequests {
		t.Fatalf("wrapped status = %d, want 429", got)
	}
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	if got := upstreamStatus(c2, errors.New("boom")); got != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", got)
	}
}

func wrapped() error {
	return errors.Join(errors.New("context"), duckgo.ErrChallengeUnavailable)
}
