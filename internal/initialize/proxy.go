package initialize

import (
	"aurora/internal/duckgo"
	"aurora/internal/proxys"
	"bufio"
	"log/slog"
	"net/url"
	"os"
)

func checkProxy() *proxys.IProxy {
	var idents []proxys.Identity
	// 每个身份配一个**新生成**的 UA：上游按「出口 IP + 指纹」限速，身份之间复用 UA
	// 等于没换身份。UA 是生成的不是枚举的，见 duckgo.RandomUA。
	add := func(proxy string) {
		idents = append(idents, proxys.Identity{Proxy: proxy, UA: duckgo.RandomUA()})
	}

	proxyUrl := os.Getenv("PROXY_URL")
	if proxyUrl != "" {
		add(proxyUrl)
	}

	if _, err := os.Stat("proxies.txt"); err == nil {
		file, _ := os.Open("proxies.txt")
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			proxy := scanner.Text()
			parsedURL, err := url.Parse(proxy)
			if err != nil {
				slog.Warn("proxy url is invalid", "url", proxy, "err", err)
				continue
			}

			// 如果缺少端口信息，不是完整的代理链接
			if parsedURL.Port() != "" {
				add(proxy)
			} else {
				continue
			}
		}
	}

	if len(idents) == 0 {
		proxy := os.Getenv("http_proxy")
		if proxy != "" {
			add(proxy)
		}
	}

	if len(idents) == 0 {
		// 直连: 也走同一个 add()，生成的 UA 每次启动都不同 —— 上游按指纹计的窗口
		// 随之换桶（重启即换额度），不需要代理。
		add("")
	}

	// 启动时把身份打出来: 换没换指纹要看得见, 不然排障只能靠猜。
	for i, id := range idents {
		slog.Info("identity", "index", i, "proxy", id.Proxy != "", "ua", id.UA)
	}

	proxyIP := proxys.NewIProxyIP(idents)
	return &proxyIP
}
