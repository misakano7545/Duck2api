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
	// 每个出口配一个不同的 UA(轮转分配): 上游按客户端指纹限速, 而指纹里既有出口 IP
	// 也有 UA —— 身份之间复用 UA 等于没换身份。UA 池与分配规则见 duckgo.UAFor。
	add := func(proxy string) {
		idents = append(idents, proxys.Identity{Proxy: proxy, UA: duckgo.UAFor(len(idents))})
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
		// 直连: UAFor(-1) 随机取一个 UA, 于是每次启动都是一个新指纹 —— 上游按指纹
		// 计的限速窗口随之换桶(重启即换额度), 不需要代理也能做到。
		// 注意不能走上面的 add(): 它按 len(idents) 分配, 直连时恒为 0, 每次启动都
		// 抽到同一个 UA, 指纹根本不会变。
		idents = append(idents, proxys.Identity{Proxy: "", UA: duckgo.UAFor(-1)})
	}

	// 启动时把身份打出来: 换没换指纹要看得见, 不然排障只能靠猜。
	for i, id := range idents {
		slog.Info("identity", "index", i, "proxy", id.Proxy != "", "ua", id.UA)
	}

	proxyIP := proxys.NewIProxyIP(idents)
	return &proxyIP
}
