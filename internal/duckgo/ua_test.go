package duckgo

import (
	"regexp"
	"strings"
	"testing"
)

// testUA 只给「需要某个具体 UA」的测试用（挑战解算、重试路径），与生成逻辑无关。
const testUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"

// 生成出来的每个 UA 都必须过得了 chromeHints，且三样东西自洽：
// user-agent 里的版本、sec-ch-ua 的版本、sec-ch-ua-platform 的平台。
// 不自洽正是 418 ERR_CHALLENGE 的成因（旧代码写死 "Linux" + Windows UA 就是栽在这）。
func TestRandomUAIsSelfConsistent(t *testing.T) {
	shape := regexp.MustCompile(`^Mozilla/5\.0 \(.+\) AppleWebKit/537\.36 \(KHTML, like Gecko\) Chrome/(\d+)\.0\.0\.0 Safari/537\.36$`)

	for i := 0; i < 500; i++ {
		ua := RandomUA()

		m := shape.FindStringSubmatch(ua)
		if m == nil {
			t.Fatalf("生成的 UA 形状不对: %s", ua)
		}

		secChUA, platform := chromeHints(ua)
		if secChUA == "" || platform == "" {
			t.Fatalf("chromeHints 认不出自己生成的 UA: %s", ua)
		}
		if want := `"Google Chrome";v="` + m[1] + `"`; !strings.Contains(secChUA, want) {
			t.Fatalf("sec-ch-ua 与 UA 版本不一致: ua=%s hints=%s", ua, secChUA)
		}

		wantPlatform := "Linux"
		switch {
		case strings.Contains(ua, "Macintosh"):
			wantPlatform = "macOS"
		case strings.Contains(ua, "Windows"):
			wantPlatform = "Windows"
		}
		if platform != wantPlatform {
			t.Fatalf("sec-ch-ua-platform=%q, UA 却是 %s", platform, wantPlatform)
		}
	}
}

// 每次生成都不一样 —— 这是「指纹用不完」的全部依据。
func TestRandomUAIsDistinct(t *testing.T) {
	const draws = 500
	seen := map[string]bool{}
	families := map[string]bool{}
	for i := 0; i < draws; i++ {
		ua := RandomUA()
		seen[ua] = true
		switch {
		case strings.Contains(ua, "Macintosh"):
			families["macOS"] = true
		case strings.Contains(ua, "Windows"):
			families["Windows"] = true
		default:
			families["Linux"] = true
		}
	}
	// 平台 7 个 × 版本 39 个 = 273 种；500 次抽样期望约 230 个不同值。
	if len(seen) < 150 {
		t.Fatalf("%d 次只生成 %d 个不同 UA，随机没生效", draws, len(seen))
	}
	if len(families) < 3 {
		t.Fatalf("500 次只覆盖 %d 个平台族，平台分布有问题: %v", len(families), families)
	}
}

// X_USER_AGENT 覆盖一切：要复现某个指纹时钉住它。
func TestRandomUAEnvOverride(t *testing.T) {
	const pinned = "Mozilla/5.0 (X11; Linux x86_64) pinned-by-env"
	t.Setenv("X_USER_AGENT", pinned)
	for i := 0; i < 50; i++ {
		if got := RandomUA(); got != pinned {
			t.Fatalf("X_USER_AGENT 没生效: %q", got)
		}
	}
}

// 认不出的 UA 不硬套 Linux/Chrome，而是让调用方不发这两个头 —— 宁可不发，不发错的。
func TestChromeHintsUnknownUA(t *testing.T) {
	if secChUA, platform := chromeHints("curl/8.5.0"); secChUA != "" || platform != "" {
		t.Fatalf("未知 UA 应返回空: got %q %q", secChUA, platform)
	}
}
