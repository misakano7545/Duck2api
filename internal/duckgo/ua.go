package duckgo

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// uaPool 可用的指纹 UA。
//
// 实测 (2026-10): 只有 Linux/macOS 的 Chrome UA 会被接受。Windows 的 UA 一律
// 418 ERR_CHALLENGE; TLS 指纹伪装客户端 (OkHttp4Android13 / Chrome_146 / Firefox /
// Safari profile) 也一律被拒 —— 出口指纹里唯一能按身份变的就只有这个 UA 串。
// 所以池子里只放 Linux/macOS, 靠 Chrome 版本号区分指纹。
//
// ponytail: 只变版本号, 不动挑战 mock 里的 navigator.platform —— 那个至今写死
// "Win32", 而 header 的 sec-ch-ua-platform 是 "Linux", 上游照样放行, 说明它只比对
// navigator.userAgent 那一项。要更"像"真实浏览器就得连 mock 的 platform/screen/
// languages 一起参数化, 现在没有必要。
var uaPool = []string{
	// 默认那个 (Chrome 145 / Linux) 放在首位: 它是唯一长期实测过的, 出了问题先回到它。
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
}

// UAFor 取第 i 个身份的 UA。
//
//   - i >= 0: 轮转分配。有几个 UA 就有几种指纹, 身份多于 UA 数时循环复用
//     (出口 IP 不同, (IP, UA) 组合仍然唯一)。
//   - i < 0: 直连(没有代理身份)。随机取一个 —— 每次进程启动都是一个新指纹,
//     上游按指纹计的限速窗口也随之换桶。
//
// X_USER_AGENT 优先级最高: 设了它则所有身份共用这一个 (旧行为, 单身份调试用)。
func UAFor(i int) string {
	if v := strings.TrimSpace(os.Getenv("X_USER_AGENT")); v != "" {
		return v
	}
	if len(uaPool) == 0 {
		return ""
	}
	if i < 0 {
		return uaPool[int(randInt63n(int64(len(uaPool))))]
	}
	return uaPool[i%len(uaPool)]
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
