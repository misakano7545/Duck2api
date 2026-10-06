// Package resty 提供 duck.ai 需要的 HTTP 客户端。
//
// 注意: 这里用标准库 net/http, 不是 TLS 指纹伪装客户端。
// 实测 (2026-10) duck.ai 的 /duckchat/v1/chat 会按 TLS/HTTP2 指纹判定:
// 同一个 token、同一套请求头, bogdanfinn/tls-client (Okhttp4Android13 / Chrome_146 /
// Firefox / Safari profile 全部试过) 拿到 418 ERR_BN_LIMIT, 换成 net/http 立刻 200。
// 所以 duck.ai 相关的调用一律走这里。(包名沿用 resty 以免改动调用方, 内部已无 resty 依赖。)
package resty

import (
	"aurora/httpclient"
	"io"
	"net/http"
	"net/url"
	"time"
)

type RestyClient struct {
	client *http.Client
}

func NewStdClient() *RestyClient {
	return &RestyClient{client: &http.Client{Timeout: 600 * time.Second}}
}

func (c *RestyClient) SetProxy(proxyURL string) error {
	if proxyURL == "" {
		c.client.Transport = nil
		return nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return err
	}
	c.client.Transport = &http.Transport{Proxy: http.ProxyURL(u)}
	return nil
}

func (c *RestyClient) Request(method httpclient.HttpMethod, rawURL string, headers httpclient.AuroraHeaders, cookies []*http.Cookie, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(string(method), rawURL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	return c.client.Do(req)
}
