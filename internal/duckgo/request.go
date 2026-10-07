package duckgo

import (
	"aurora/internal/httpclient"
	duckgotypes "aurora/internal/typings/duckgo"
	officialtypes "aurora/internal/typings/official"
	"aurora/internal/util"
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	Token     *XqdgToken
	FEVersion *XqdgToken
	// 说明: 服务端会校验 UA —— 同一请求下 Linux/macOS 的 UA 会被接受, Windows 的 UA 一律
	// 返回 418 ERR_CHALLENGE (实测 2026-10)。这个 UA 必须与 vqd.go 里 defaultVQDUserAgent
	// 完全一致: 挑战把 navigator.userAgent 算进 client_hashes[0], 服务端会拿请求头里的 UA 复算比对。
	UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"
)

type XqdgToken struct {
	Token    string     `json:"token"`
	M        sync.Mutex `json:"-"`
	ExpireAt time.Time  `json:"expire"`
}

// ErrChallengeUnavailable: 上游没下发 x-vqd-hash-1 挑战头。
// duck.ai 按客户端指纹限速(实测连发约 5 次后触发, 冷却 30-60s 自动恢复), 期间
// /status 返回 200 但没有挑战头——这是限速不是服务故障, 调用方应按 429 处理。
var ErrChallengeUnavailable = errors.New("upstream did not issue an x-vqd-hash-1 challenge")

// chalRetryDelays 取挑战失败的退避间隔, 累计约 31s。
// 实测冷却 30s 即可恢复, 更长的窗口由调用方按 429 + Retry-After 重试兜底。
var chalRetryDelays = []time.Duration{
	1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
}

func InitXVQD(client httpclient.AuroraHttpClient, proxyUrl string) (string, error) {
	if Token == nil {
		Token = &XqdgToken{
			Token: "",
			M:     sync.Mutex{},
		}
	}
	Token.M.Lock()
	// ponytail: 退避期间持锁, 并发调用会排队等同一个挑战(单账号代理场景足够);
	// 要并行就得改成 singleflight + 各自退避, 现在不做。
	defer Token.M.Unlock()
	if Token.Token == "" {
		lastErr := error(ErrChallengeUnavailable)
		for attempt := 0; ; attempt++ {
			status, err := postStatus(client, proxyUrl)
			if err != nil {
				lastErr = err // 网络/代理抖动, 同样退避重试
			} else {
				vqdHash := status.Header.Get("x-vqd-hash-1")
				status.Body.Close()
				if vqdHash != "" {
					// 拿到挑战就算成功一半: 解算失败属于另一类问题(GenerateVQDHash
					// 内部已有 fallback), 不再按限速重试。
					token, tokenErr := GenerateVQDHash(vqdHash)
					if tokenErr != nil {
						return "", tokenErr
					}
					Token.Token = token
					return Token.Token, nil
				}
				lastErr = ErrChallengeUnavailable
			}
			if attempt >= len(chalRetryDelays) {
				break
			}
			log.Printf("[VQD] %v, retrying in %s (attempt %d/%d)", lastErr, chalRetryDelays[attempt], attempt+1, len(chalRetryDelays))
			time.Sleep(chalRetryDelays[attempt])
		}
		return "", lastErr
	}

	return Token.Token, nil
}

func postStatus(client httpclient.AuroraHttpClient, proxyUrl string) (*http.Response, error) {
	if proxyUrl != "" {
		client.SetProxy(proxyUrl)
	}
	header := createHeader()
	header.Set("accept", "*/*")
	header.Set("x-vqd-accept", "1")
	response, err := client.Request(httpclient.GET, "https://duck.ai/duckchat/v1/status", header, nil, nil)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func POSTconversation(client httpclient.AuroraHttpClient, request duckgotypes.ApiRequest, token string, proxyUrl string) (*http.Response, error) {
	if proxyUrl != "" {
		client.SetProxy(proxyUrl)
	}

	maxRetries := 3
	var response *http.Response
	var err error

	for i := 0; i <= maxRetries; i++ {
		response, err = postConversationOnce(client, request, token)
		if err != nil {
			return nil, err
		}

		if response.StatusCode != http.StatusTeapot && response.StatusCode != http.StatusTooManyRequests {
			return response, nil
		}

		// 关掉前排空并存下 body：Close 之后调用方 io.ReadAll 只能读到空串，
		// 上游的错误类型（ERR_INPUT_LIMIT / ERR_RATE_LIMIT / ERR_MODEL_RESTRICTED…）
		// 会整个丢掉，客户端只看到一个光秃秃的 429，分不清"输入超限"还是"被限速"。
		errBody, _ := io.ReadAll(response.Body)
		response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(errBody))
		ResetXVQD()
		token, err = InitXVQD(client, proxyUrl)
		if err != nil {
			return nil, err
		}
	}

	return response, nil
}

func Handle_request_error(c *gin.Context, response *http.Response) bool {
	if response.StatusCode != 200 {
		// Try read response body as JSON
		var error_response map[string]interface{}
		err := json.NewDecoder(response.Body).Decode(&error_response)
		if err != nil {
			// Read response body
			body, _ := io.ReadAll(response.Body)
			c.JSON(response.StatusCode, gin.H{"error": gin.H{
				"message": "Unknown error",
				"type":    "internal_server_error",
				"param":   nil,
				"code":    "500",
				"details": string(body),
			}})
			return true
		}
		c.JSON(response.StatusCode, gin.H{"error": gin.H{
			"message": error_response["detail"],
			"type":    response.Status,
			"param":   nil,
			"code":    "error",
		}})
		return true
	}
	return false
}

func createHeader() httpclient.AuroraHeaders {
	header := make(httpclient.AuroraHeaders)
	header.Set("accept-language", "zh-CN,zh;q=0.9,en-US;q=0.8,en;q=0.7")
	header.Set("content-type", "application/json")
	header.Set("origin", "https://duck.ai")
	header.Set("referer", "https://duck.ai/")
	header.Set("sec-ch-ua", `"Google Chrome";v="145", "Chromium";v="145", "Not:A;Brand";v="24"`)
	header.Set("sec-ch-ua-mobile", "?0")
	header.Set("sec-ch-ua-platform", `"Linux"`)
	header.Set("sec-fetch-dest", "empty")
	header.Set("sec-fetch-mode", "cors")
	header.Set("sec-fetch-site", "same-origin")
	header.Set("user-agent", UA)
	return header
}

func postConversationOnce(client httpclient.AuroraHttpClient, request duckgotypes.ApiRequest, token string) (*http.Response, error) {
	bodyJSON, err := json.Marshal(request)
	if err != nil {
		return &http.Response{}, err
	}
	header := createHeader()
	header.Set("accept", "text/event-stream")
	header.Set("priority", "u=1, i")
	header.Set("x-ddg-journey-id", RandomHex(16))
	header.Set("x-fe-signals", CreateFESignals())
	if feVersion, err := InitFEVersion(client, ""); err == nil && feVersion != "" {
		header.Set("x-fe-version", feVersion)
	}
	header.Set("x-vqd-hash-1", token)
	return client.Request(httpclient.POST, "https://duck.ai/duckchat/v1/chat", header, nil, bytes.NewBuffer(bodyJSON))
}

func InitFEVersion(client httpclient.AuroraHttpClient, proxyUrl string) (string, error) {
	if FEVersion == nil {
		FEVersion = &XqdgToken{
			Token: "",
			M:     sync.Mutex{},
		}
	}
	FEVersion.M.Lock()
	defer FEVersion.M.Unlock()
	if FEVersion.Token != "" && FEVersion.ExpireAt.After(time.Now()) {
		return FEVersion.Token, nil
	}

	if proxyUrl != "" {
		client.SetProxy(proxyUrl)
	}
	header := createHeader()
	header.Set("accept", "text/html")
	response, err := client.Request(httpclient.GET, "https://duck.ai/", header, nil, nil)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	versionTagMatch := regexp.MustCompile(`data-version-tag="([^"]+)"`).FindSubmatch(body)
	versionShaMatch := regexp.MustCompile(`data-version-sha="([^"]+)"`).FindSubmatch(body)
	if len(versionTagMatch) < 2 || len(versionShaMatch) < 2 {
		return "", errors.New("duck.ai version metadata not found")
	}

	FEVersion.Token = fmt.Sprintf("%s-%s", versionTagMatch[1], versionShaMatch[1])
	FEVersion.ExpireAt = time.Now().Add(30 * time.Minute)
	return FEVersion.Token, nil
}

func CreateFESignals() string {
	now := time.Now().UnixMilli()
	// Reproduce the event log the duck.ai frontend records between page load
	// request): onboarding_impression -> action -> onboarding_finish -> startNewChat_free.
	// 模拟真实用户行为: 页面加载 -> 用户思考输入 -> 完成输入 -> 点击发送
	// 时间间隔调整为更接近真实用户的行为模式
	impression := 50 + randInt63n(100)              // 50-150ms (页面加载完成)
	action := impression + 5000 + randInt63n(25000) // 5-30秒 (用户阅读并思考)
	finish := action + 1000 + randInt63n(9000)      // 1-10秒 (输入问题)
	startChat := finish + 10 + randInt63n(90)       // 10-100ms (点击发送按钮)
	end := startChat + randInt63n(10)
	payload := map[string]interface{}{
		"start": now - end,
		"events": []map[string]interface{}{
			{"name": "onboarding_impression", "delta": impression},
			{"name": "action", "delta": action, "trusted": true},
			{"name": "onboarding_finish", "delta": finish},
			{"name": "startNewChat_free", "delta": startChat},
		},
		"end": end,
	}
	body, _ := json.Marshal(payload)
	return base64.StdEncoding.EncodeToString(body)
}

// randInt63n returns a uniform random non-negative int64 in [0, n).
func randInt63n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return 0
	}
	var v int64
	for _, b := range buf {
		v = v<<8 | int64(b)
	}
	if v < 0 {
		v = -v
	}
	return v % n
}

func RandomHex(byteLength int) string {
	buffer := make([]byte, byteLength)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

func ResetXVQD() {
	if Token == nil {
		return
	}
	Token.M.Lock()
	defer Token.M.Unlock()
	Token.Token = ""
}

func ReadResponseError(response *http.Response) error {
	var errorResponse map[string]interface{}
	if err := json.NewDecoder(response.Body).Decode(&errorResponse); err == nil {
		if detail, ok := errorResponse["detail"]; ok {
			return fmt.Errorf("%s: %v", response.Status, detail)
		}
		return fmt.Errorf("%s: %v", response.Status, errorResponse)
	}

	body, _ := io.ReadAll(response.Body)
	if len(body) == 0 {
		return fmt.Errorf("%s", response.Status)
	}
	return fmt.Errorf("%s: %s", response.Status, string(body))
}

func ReadResponseText(response *http.Response) string {
	reader := bufio.NewReader(response.Body)
	var previousText strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return ""
		}
		if len(line) < 6 {
			continue
		}
		line = line[6:]
		if strings.HasPrefix(line, "[DONE]") {
			continue
		}

		var originalResponse duckgotypes.ApiResponse
		err = json.Unmarshal([]byte(line), &originalResponse)
		if err != nil || originalResponse.Action != "success" {
			continue
		}
		previousText.WriteString(originalResponse.Message)
	}
	return previousText.String()
}

// HandlerStats carries pre-computed input stats and timing start into the stream handler.
type HandlerStats struct {
	Start        time.Time
	PromptTokens int
	CachedTokens int
	Effort       string
	// Tools 表示本次请求带了工具定义, 需要走闸门判定模型是否要调工具。
	Tools bool
}

// StreamResult is what Handler returns: the full text plus output-side telemetry.
type StreamResult struct {
	Text         string
	ToolCalls    []ToolCall
	OutputTokens int
	TTFTMs       int64
	TotalMs      int64
}

func Handler(c *gin.Context, response *http.Response, oldRequest duckgotypes.ApiRequest, stream bool, stats HandlerStats) StreamResult {
	reader := bufio.NewReader(response.Body)
	if stream {
		// Response content type is text/event-stream
		c.Header("Content-Type", "text/event-stream")
	} else {
		// Response content type is application/json
		c.Header("Content-Type", "application/json")
	}

	var previousText strings.Builder
	var firstTokenSet bool
	var ttftMs int64
	gate := NewStreamGate(stats.Tools)
	var toolCalls []ToolCall
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return StreamResult{}
		}
		if len(line) < 6 {
			continue
		}
		line = line[6:]
		if !strings.HasPrefix(line, "[DONE]") {
			var originalResponse duckgotypes.ApiResponse
			err = json.Unmarshal([]byte(line), &originalResponse)
			if err != nil {
				continue
			}
			if originalResponse.Action != "success" {
				c.JSON(500, gin.H{"error": "Error"})
				return StreamResult{}
			}
			responseString := ""
			if originalResponse.Message != "" {
				previousText.WriteString(originalResponse.Message)
				if !firstTokenSet {
					firstTokenSet = true
					ttftMs = time.Since(stats.Start).Milliseconds()
				}
				// 带工具的请求先过闸门: 模型要调工具时这段不发, 攒到收尾一次性发 tool_calls。
				if emit, hold := gate.Push(originalResponse.Message); !hold && emit != "" {
					translatedResponse := officialtypes.NewChatCompletionChunkWithModel(emit, originalResponse.Model)
					responseString = "data: " + translatedResponse.String() + "\n\n"
				}
			}

			if responseString == "" {
				continue
			}

			if stream {
				_, err = c.Writer.WriteString(responseString)
				if err != nil {
					return StreamResult{}
				}
				c.Writer.Flush()
			}
		} else {
			// 流结束: 扣住的若确实是工具调用, 先补发 tool_calls 再发 stop。
			reason := "stop"
			if gate.Holding() {
				toolCalls = ParseToolCalls(gate.Buffered())
				if len(toolCalls) > 0 {
					reason = "tool_calls"
				}
			}
			if stream {
				if len(toolCalls) > 0 {
					chunk := officialtypes.NewToolCallChunk(oldRequest.Model, OfficialToolCalls(toolCalls))
					c.Writer.WriteString("data: " + chunk.String() + "\n\n")
				} else if gate.Holding() && gate.Buffered() != "" {
					// 兜底: 看着像标记但解析不出来, 当普通文本补发, 别让客户端收空
					chunk := officialtypes.NewChatCompletionChunkWithModel(gate.Buffered(), oldRequest.Model)
					c.Writer.WriteString("data: " + chunk.String() + "\n\n")
				}
				final_line := officialtypes.StopChunkWithModel(reason, oldRequest.Model)
				c.Writer.WriteString("data: " + final_line.String() + "\n\n")
			}
		}
	}

	fullText := previousText.String()
	outputTokens := util.CountToken(fullText)
	totalMs := time.Since(stats.Start).Milliseconds()

	// Emit the final usage + timing chunk after [DONE] in stream mode (OpenAI include_usage compatible).
	if stream {
		usageChunk := officialtypes.UsageChunk(
			oldRequest.Model,
			stats.PromptTokens,
			outputTokens,
			stats.CachedTokens,
			ttftMs,
			totalMs,
			stats.Effort,
		)
		c.Writer.WriteString("data: " + usageChunk.String() + "\n\n")
		c.Writer.Flush()
	}

	return StreamResult{
		Text:         fullText,
		ToolCalls:    toolCalls,
		OutputTokens: outputTokens,
		TTFTMs:       ttftMs,
		TotalMs:      totalMs,
	}
}
