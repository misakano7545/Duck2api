package duckgo

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// 指纹 UA 是**生成**的，不是从固定池子里挑的。
//
// 实测（2026-10，12/12 全过）：Windows / Edge / Android / Safari（连 Chrome 版本都没有）
// / Chrome 100 与 160 都能拿到挑战 —— UA 基本是自由字段。真正被拒的只有「客户端提示与 UA
// 自相矛盾」那一类：旧代码把 sec-ch-ua-platform 写死 "Linux"，于是 Windows UA 一律 418，
// 当时被误读成「Windows UA 不被接受」；现在由 chromeHints 从 UA 反推，Windows 直接过。
//
// 所以枚举成池子是把上限白白钉死在池子大小上（5 个），生成则是用不完的。
// ponytail: 只组合 OS + Chrome 版本；实测够用。要更"真"再加 WebKit 版本/语言/screen
// （挑战 mock 里那些值仍是常量，且实测不参与校验）。
var uaPlatforms = []string{
	"X11; Linux x86_64",
	"X11; Ubuntu; Linux x86_64",
	"X11; Fedora; Linux x86_64",
	"Macintosh; Intel Mac OS X 10_15_7",
	"Macintosh; Intel Mac OS X 13_6_0",
	"Macintosh; Intel Mac OS X 14_5_0",
	"Windows NT 10.0; Win64; x64",
}

// 实测通过带是 100~160，取内侧，免得撞边界。
const (
	uaVersionMin = 120
	uaVersionMax = 158
)

// RandomUA 生成一个指纹 UA。
//
// X_USER_AGENT 覆盖一切：要复现某个具体指纹，或排查「到底是不是 UA 的问题」，钉住它。
func RandomUA() string {
	if v := strings.TrimSpace(os.Getenv("X_USER_AGENT")); v != "" {
		return v
	}
	plat := uaPlatforms[int(randInt63n(int64(len(uaPlatforms))))]
	ver := uaVersionMin + int(randInt63n(int64(uaVersionMax-uaVersionMin+1)))
	return fmt.Sprintf(
		"Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36",
		plat, ver)
}

var chromeVersionRE = regexp.MustCompile(`Chrome/(\d+)\.\d+\.\d+\.\d+`)

// chromeHints 从 UA 派生 sec-ch-ua / sec-ch-ua-platform 两个客户端提示。
// 由 UA 反推而不是再维护一张常量表: 两份来源迟早不一致, 而客户端提示与 UA 打架
// 正是 418 ERR_CHALLENGE 的成因之一。认不出来就返回空串, 调用方不发这两个头。
func chromeHints(ua string) (secChUA, platform string) {
	m := chromeVersionRE.FindStringSubmatch(ua)
	if m == nil {
		return "", ""
	}
	platform = "Linux"
	switch {
	case strings.Contains(ua, "Macintosh"):
		platform = "macOS"
	case strings.Contains(ua, "Windows"):
		platform = "Windows"
	}
	return fmt.Sprintf(`"Google Chrome";v="%s", "Chromium";v="%s", "Not:A;Brand";v="24"`, m[1], m[1]), platform
}
