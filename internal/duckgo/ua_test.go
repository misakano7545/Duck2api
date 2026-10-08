package duckgo

import (
	"strings"
	"testing"
)

// 每个身份发出去的三样东西必须自洽: user-agent / sec-ch-ua 版本 / sec-ch-ua-platform。
// 不自洽正是 418 ERR_CHALLENGE 的成因 —— 这一条挂了, 换指纹就等于换掉整个上游通道。
func TestUAPoolHintsAreConsistent(t *testing.T) {
	if len(uaPool) < 2 {
		t.Fatalf("指纹池只有 %d 个 UA, 换指纹没有意义", len(uaPool))
	}

	seen := map[string]bool{}
	for _, ua := range uaPool {
		if seen[ua] {
			t.Fatalf("指纹池里有重复 UA: %s", ua)
		}
		seen[ua] = true

		// 只有 Linux/macOS 的 UA 被上游接受, Windows 一律 418 (实测 2026-10)。
		if strings.Contains(ua, "Windows") {
			t.Fatalf("Windows UA 会被上游拒绝, 不该进池子: %s", ua)
		}

		secChUA, platform := chromeHints(ua)
		if secChUA == "" || platform == "" {
			t.Fatalf("chromeHints 认不出池子里的 UA: %s", ua)
		}

		// sec-ch-ua 里的版本号必须就是 UA 里的版本号。
		ver := chromeVersionRE.FindStringSubmatch(ua)[1]
		if want := `"Google Chrome";v="` + ver + `"`; !strings.Contains(secChUA, want) {
			t.Fatalf("sec-ch-ua 与 UA 版本不一致: ua=%s hints=%s", ua, secChUA)
		}

		// platform 提示也要和 UA 对得上。
		wantPlatform := "Linux"
		if strings.Contains(ua, "Macintosh") {
			wantPlatform = "macOS"
		}
		if platform != wantPlatform {
			t.Fatalf("sec-ch-ua-platform=%q, UA 却是 %s", platform, wantPlatform)
		}
	}
}

// 认不出的 UA 不硬套 Linux/Chrome, 而是让调用方不发这两个头 —— 宁可不发, 不发错的。
func TestChromeHintsUnknownUA(t *testing.T) {
	if secChUA, platform := chromeHints("curl/8.5.0"); secChUA != "" || platform != "" {
		t.Fatalf("未知 UA 应返回空: got %q %q", secChUA, platform)
	}
}

// 直连(i<0)随机取, 带下标则轮转; X_USER_AGENT 覆盖一切。
func TestUAForAllocation(t *testing.T) {
	if got := UAFor(0); got != uaPool[0] {
		t.Fatalf("UAFor(0) = %q, want uaPool[0]", got)
	}
	if got := UAFor(len(uaPool)); got != uaPool[0] {
		t.Fatalf("UAFor(%d) 应回绕到 uaPool[0], got %q", len(uaPool), got)
	}
	if got := UAFor(1); got != uaPool[1] {
		t.Fatalf("UAFor(1) = %q, want uaPool[1]", got)
	}

	// 直连必须是池子里的某一个, 且允许出现不同值(随机)。
	direct := map[string]bool{}
	for i := 0; i < 50; i++ {
		got := UAFor(-1)
		if !seenInPool(got) {
			t.Fatalf("UAFor(-1) 返回了池子外的 UA: %q", got)
		}
		direct[got] = true
	}
	if len(direct) < 2 {
		t.Fatalf("直连 50 次只抽到 %d 个 UA, 随机没生效", len(direct))
	}

	t.Setenv("X_USER_AGENT", "Mozilla/5.0 (X11; Linux x86_64) test-override")
	if got := UAFor(3); got != "Mozilla/5.0 (X11; Linux x86_64) test-override" {
		t.Fatalf("X_USER_AGENT 没生效: %q", got)
	}
	if got := UAFor(-1); got != "Mozilla/5.0 (X11; Linux x86_64) test-override" {
		t.Fatalf("X_USER_AGENT 对直连也没生效: %q", got)
	}
}

func seenInPool(ua string) bool {
	for _, p := range uaPool {
		if p == ua {
			return true
		}
	}
	return false
}
