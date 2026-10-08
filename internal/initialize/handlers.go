package initialize

import (
	duckgoConvert "aurora/internal/conversion/requests/duckgo"
	"aurora/internal/duckgo"
	"aurora/internal/httpclient/resty"
	"aurora/internal/proxys"
	duckgotypes "aurora/internal/typings/duckgo"
	officialtypes "aurora/internal/typings/official"
	"aurora/internal/util"
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	proxy *proxys.IProxy
}

func NewHandle(proxy *proxys.IProxy) *Handler {
	// Wire up file store for file_id resolution in chat
	duckgoConvert.FileStore = func(fileID string) (string, string, []byte, bool) {
		f, ok := fileStorage[fileID]
		if !ok {
			return "", "", nil, false
		}
		return f.Filename, f.MimeType, f.Bytes, true
	}
	return &Handler{proxy: proxy}
}

// upstreamStatus 把「取挑战失败」当作上游限速返回 429 + Retry-After, 其它错误仍是 500。
// 所有会走到 duckgo.InitXVQD 的失败点都该用它代替写死的 500, 免得把限速当故障报给调用方。
// ponytail: 只改状态码与 Retry-After, 各站点原有响应体形状不动。
func upstreamStatus(c *gin.Context, err error) int {
	if errors.Is(err, duckgo.ErrChallengeUnavailable) {
		c.Header("Retry-After", "60")
		return http.StatusTooManyRequests
	}
	return http.StatusInternalServerError
}

// writeClientError 把上游/网关判定的失败写成客户端能分辨的 typed 错误。
// 客户端必须一眼分清「这次请求本身太大」（400 context_length_exceeded）与
// 「现在按指纹被限速」（429 rate_limit_exceeded）：原先两者都是同一个光秃秃 429，
// Hermes 实测因此连撞三次后报 provider unavailable，看不出真正原因。
func writeClientError(c *gin.Context, t duckgo.UpstreamErrorType) {
	status, typ, code := duckgo.ClientError(t)
	if status == http.StatusTooManyRequests && c.Writer.Header().Get("Retry-After") == "" {
		c.Header("Retry-After", "60")
	}
	c.JSON(status, gin.H{"error": gin.H{
		"message": duckgo.ClientErrorMessage(t),
		"type":    typ,
		"param":   nil,
		"code":    code,
	}})
}

// writeUpstreamFailure 处理上游非 200：按错误类型回 typed 状态码与 code。
// 上游原文（含 r 事件号与站点内部字段）只进日志，不回给客户端。
func writeUpstreamFailure(c *gin.Context, response *http.Response) {
	body, _ := io.ReadAll(response.Body)
	t := duckgo.UpstreamErrorTypeOf(body)
	if t == duckgo.ErrTypeUnknown {
		log.Printf("[UPSTREAM] %d 未归类: %s", response.StatusCode, truncateStr(string(body), 300))
	}
	writeClientError(c, t)
}

// writeGatewayError 处理「网关自己判定 / 取挑战失败」的 err。输入超限在打到上游之前
// 就拒绝，其余沿用 upstreamStatus 的既有语义。
func writeGatewayError(c *gin.Context, err error) {
	if errors.Is(err, duckgo.ErrInputTooLarge) {
		writeClientError(c, duckgo.ErrTypeInputLimit)
		return
	}
	status := upstreamStatus(c, err)
	c.JSON(status, gin.H{"error": gin.H{
		"message": err.Error(),
		"type":    "upstream_error",
		"code":    "upstream_failure",
	}})
}

func optionsHandler(c *gin.Context) {
	// Set headers for CORS
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "POST")
	c.Header("Access-Control-Allow-Headers", "*")
	c.JSON(200, gin.H{
		"message": "pong",
	})
}

func (h *Handler) duckduckgo(c *gin.Context) {
	var original_request officialtypes.APIRequest
	err := c.BindJSON(&original_request)
	if err != nil {
		c.JSON(400, gin.H{"error": gin.H{
			"message": "Request must be proper JSON",
			"type":    "invalid_request_error",
			"param":   nil,
			"code":    err.Error(),
		}})
		return
	}

	// Resolve thinking effort: request field takes precedence, then CLAUDE_CODE_EFFORT_LEVEL env.
	effort := original_request.ReasoningEffort
	if effort == "" {
		effort = os.Getenv("CLAUDE_CODE_EFFORT_LEVEL")
		original_request.ReasoningEffort = effort
	}

	// Input token counting + prompt-cache simulation (cache creation / hit).
	inputTokens := util.CountMessagesTokens(original_request.Messages)
	promptHash := util.HashPrompt(messagesText(original_request.Messages))
	cacheCreation, cacheRead := util.RecordCache(promptHash, inputTokens)
	cachedTokens := cacheRead // a cache hit is what gets reported in usage

	translated_request, response, err := h.startDuckDuckGoRequest(original_request)
	if err != nil {
		writeGatewayError(c, err)
		return
	}
	defer response.Body.Close()

	// Debug: log upstream response status
	if response.StatusCode != 200 {
		writeUpstreamFailure(c, response)
		return
	}

	start := time.Now()
	stats := duckgo.HandlerStats{
		Start:        start,
		Tools:        original_request.Tools != nil,
		PromptTokens: inputTokens,
		CachedTokens: cachedTokens,
		Effort:       effort,
	}

	// Cache breakdown is known before streaming starts, so set these headers
	// before the first chunk is flushed (works for both stream and non-stream).
	setCacheHeaders(c, promptHash, cacheCreation, cacheRead)

	result := duckgo.Handler(c, response, translated_request, original_request.Stream, stats)

	// Timing is only known after the stream completes. For non-stream this header
	// is delivered; for stream the same values are in the final usage chunk.
	c.Header("X-TTFT-Ms", fmt.Sprintf("%d", result.TTFTMs))
	c.Header("X-Total-Time-Ms", fmt.Sprintf("%d", result.TotalMs))

	if c.Writer.Status() != 200 {
		return
	}
	if !original_request.Stream {
		// 模型要调工具: 按 OpenAI 原生结构回写, finish_reason=tool_calls
		if len(result.ToolCalls) > 0 {
			c.JSON(200, officialtypes.NewChatCompletionToolCalls(original_request.Model, duckgo.OfficialToolCalls(result.ToolCalls),
				int64(inputTokens), int64(result.OutputTokens), result.TTFTMs, result.TotalMs))
			return
		}
		c.JSON(200, officialtypes.NewChatCompletionFull(
			result.Text,
			translated_request.Model,
			int64(inputTokens),
			int64(result.OutputTokens),
			int64(cachedTokens),
			result.TTFTMs,
			result.TotalMs,
			effort,
		))
	} else {
		c.String(200, "data: [DONE]\n\n")
	}
}

// messagesText concatenates message contents into a single string for cache keying.
func messagesText(messages []officialtypes.ApiMessage) string {
	var sb strings.Builder
	for _, msg := range messages {
		sb.WriteString(util.MessageText(msg.Content))
		sb.WriteString("\n")
	}
	return sb.String()
}

// setCacheHeaders sets both the existing custom cache headers and the standard
// HTTP cache headers (X-Cache / Age) that external cache-statistics software
// (Varnish, Squid, CDNs, monitoring) can read.
func setCacheHeaders(c *gin.Context, promptHash string, cacheCreation, cacheRead int) {
	c.Header("X-Cache-Creation-Tokens", fmt.Sprintf("%d", cacheCreation))
	c.Header("X-Cache-Read-Tokens", fmt.Sprintf("%d", cacheRead))
	xCache, age := util.CacheHeaders(promptHash)
	c.Header("X-Cache", xCache)
	if xCache == "HIT" {
		c.Header("Age", fmt.Sprintf("%d", age))
	}
}

func (h *Handler) responses(c *gin.Context) {
	var responseRequest officialtypes.ResponseAPIRequest
	err := c.BindJSON(&responseRequest)
	if err != nil {
		c.JSON(400, gin.H{"error": gin.H{
			"message": "Request must be proper JSON",
			"type":    "invalid_request_error",
			"param":   nil,
			"code":    err.Error(),
		}})
		return
	}

	// Resolve thinking effort: request field takes precedence, then CLAUDE_CODE_EFFORT_LEVEL env.
	effort := responseRequest.ReasoningEffort
	if effort == "" {
		effort = os.Getenv("CLAUDE_CODE_EFFORT_LEVEL")
		responseRequest.ReasoningEffort = effort
	}

	chatRequest := responseRequest.ToChatCompletionRequest()
	chatRequest.ReasoningEffort = effort

	// Input token counting + prompt-cache simulation.
	inputTokens := util.CountMessagesTokens(chatRequest.Messages)
	promptHash := util.HashPrompt(messagesText(chatRequest.Messages))
	cacheCreation, cacheRead := util.RecordCache(promptHash, inputTokens)
	cachedTokens := cacheRead

	// Cache breakdown is known before streaming starts, so set these headers
	// before the first chunk is flushed (works for both stream and non-stream).
	setCacheHeaders(c, promptHash, cacheCreation, cacheRead)

	translatedRequest, response, err := h.startDuckDuckGoRequest(chatRequest)
	if err != nil {
		writeGatewayError(c, err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		writeUpstreamFailure(c, response)
		return
	}

	// Cache breakdown is known before streaming; set before first flush.
	setCacheHeaders(c, promptHash, cacheCreation, cacheRead)

	start := time.Now()
	stats := duckgo.HandlerStats{
		Start:        start,
		Tools:        chatRequest.Tools != nil,
		PromptTokens: inputTokens,
		CachedTokens: cachedTokens,
		Effort:       effort,
	}

	if responseRequest.Stream {
		result := handleResponsesStream(c, response.Body, translatedRequest.Model, stats)
		c.Header("X-TTFT-Ms", fmt.Sprintf("%d", result.ttftMs))
		c.Header("X-Total-Time-Ms", fmt.Sprintf("%d", result.totalMs))
		return
	}

	result := duckgo.Handler(c, response, translatedRequest, false, stats)
	c.Header("X-TTFT-Ms", fmt.Sprintf("%d", result.TTFTMs))
	c.Header("X-Total-Time-Ms", fmt.Sprintf("%d", result.TotalMs))

	completed := officialtypes.NewResponseAPIFull(
		result.Text,
		translatedRequest.Model,
		int64(inputTokens),
		int64(result.OutputTokens),
		int64(cachedTokens),
		result.TTFTMs,
		result.TotalMs,
		effort,
	)
	// 模型要调工具: output 换成 function_call 项, output_text 置空(与上游 Responses API 一致)。
	if len(result.ToolCalls) > 0 {
		completed.Output = duckgo.ResponsesToolCalls(result.ToolCalls)
		completed.OutputText = ""
	}
	c.JSON(http.StatusOK, completed)
}

func (h *Handler) startDuckDuckGoRequest(originalRequest officialtypes.APIRequest) (duckgotypes.ApiRequest, *http.Response, error) {
	proxyUrl := h.proxy.GetProxyIP()
	client := resty.NewStdClient()
	token, err := duckgo.InitXVQD(client, proxyUrl)
	if err != nil {
		return duckgotypes.ApiRequest{}, nil, err
	}

	reasoningEffort := originalRequest.ReasoningEffort
	webSearch := originalRequest.WebSearch != nil && *originalRequest.WebSearch

	translatedRequest := duckgoConvert.ConvertAPIRequestWithOptions(originalRequest, reasoningEffort, webSearch)

	// 上游单请求上限是硬的（≈4k token，见 fit.go）：在这里一次把三条入站路（chat /
	// responses / messages）都管住，别让请求白白打到上游再吃一个 67s 的 429。
	if dropped, ok := duckgoConvert.FitToUpstreamLimit(&translatedRequest); !ok {
		return duckgotypes.ApiRequest{}, nil, duckgo.ErrInputTooLarge
	} else if dropped > 0 {
		log.Printf("[FIT] 上游输入上限 %d token，丢弃 %d 条旧历史（保留首条与最新几条）",
			duckgoConvert.MaxInputTokens(), dropped)
	}

	// Debug: log request
	reqJSON, _ := json.Marshal(translatedRequest)
	log.Printf("[DEBUG] DuckDuckGo request: %s", truncateStr(string(reqJSON), 2000))

	response, err := duckgo.POSTconversation(client, translatedRequest, token, proxyUrl)
	if err != nil {
		return duckgotypes.ApiRequest{}, nil, err
	}
	return translatedRequest, response, nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// responsesStreamResult holds output-side telemetry for a streamed response.
type responsesStreamResult struct {
	text         string
	outputTokens int
	ttftMs       int64
	totalMs      int64
}

// handleResponsesStream reads DuckDuckGo's text SSE and emits real Response API
// SSE events (response.created → output_item.added → content_part.added →
// output_text.delta per token → output_text.done → content_part.done →
// output_item.done → response.completed).
// 带工具的请求先过闸门(与 chat / anthropic 两条路同一套 duckgo.StreamGate):
// 判定是工具调用就发 function_call 输出项, 否则才发 message / output_text。
func handleResponsesStream(c *gin.Context, body io.ReadCloser, model string, stats duckgo.HandlerStats) responsesStreamResult {
	defer body.Close()

	reader := bufio.NewReader(body)
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	// Pre-build the response shells.
	inProgress := officialtypes.NewResponseAPIWithModel("", model)
	inProgress.Status = "in_progress"
	inProgress.Output = []officialtypes.ResponseOutput{}
	inProgress.Usage = officialtypes.ResponseUsage{InputTokens: stats.PromptTokens}

	// response.created
	writeRespEvent(c, officialtypes.ResponseStreamEvent{Type: "response.created", Sequence: 1, Response: &inProgress})

	// 输出项要等闸门判定完才知道是 message 还是 function_call, 所以这两条推迟到第一个
	// 可下发的分片; 无工具的请求闸门不启用, 第一块就到, 事件顺序与改动前一致。
	gate := duckgo.NewStreamGate(stats.Tools)
	output := officialtypes.NewResponseOutput("")
	output.Status = "in_progress"
	part := officialtypes.ResponseOutputContent{
		Type:        "output_text",
		Text:        "",
		Annotations: []interface{}{},
	}
	started := false
	startMessageItem := func() {
		if started {
			return
		}
		started = true
		// response.output_item.added
		writeRespEvent(c, officialtypes.ResponseStreamEvent{Type: "response.output_item.added", Sequence: 2, OutputIndex: 0, Item: &output})
		// response.content_part.added
		writeRespEvent(c, officialtypes.ResponseStreamEvent{Type: "response.content_part.added", Sequence: 3, ItemID: output.ID, OutputIndex: 0, ContentIndex: 0, Part: part})
	}

	var sb strings.Builder
	var firstTokenSet bool
	var ttftMs int64

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return responsesStreamResult{}
		}
		if len(line) < 6 {
			continue
		}
		line = line[6:] // strip "data: "
		if strings.HasPrefix(line, "[DONE]") {
			continue
		}

		var delta struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &delta); err != nil {
			continue
		}
		if delta.Message == "" {
			continue
		}

		sb.WriteString(delta.Message)
		if !firstTokenSet {
			firstTokenSet = true
			ttftMs = time.Since(stats.Start).Milliseconds()
		}

		emit, hold := gate.Push(delta.Message)
		if hold || emit == "" {
			continue
		}
		startMessageItem()
		// response.output_text.delta
		writeRespEvent(c, officialtypes.ResponseStreamEvent{
			Type:         "response.output_text.delta",
			Sequence:     0,
			ItemID:       output.ID,
			OutputIndex:  0,
			ContentIndex: 0,
			Delta:        emit,
		})
	}

	fullText := sb.String()
	outputTokens := util.CountToken(fullText)
	totalMs := time.Since(stats.Start).Milliseconds()

	// 模型要调工具: output 换成 function_call 项, 不分片整块发(与 chat 路径同一取舍)。
	var toolCalls []duckgo.ToolCall
	if gate.Holding() {
		toolCalls = duckgo.ParseToolCalls(gate.Buffered())
	}
	if len(toolCalls) > 0 {
		completed := officialtypes.NewResponseAPIFull("", model, int64(stats.PromptTokens), int64(outputTokens), int64(stats.CachedTokens), ttftMs, totalMs, stats.Effort)
		completed.Output = duckgo.ResponsesToolCalls(toolCalls)
		for i := range completed.Output {
			writeRespEvent(c, officialtypes.ResponseStreamEvent{Type: "response.output_item.added", Sequence: 0, OutputIndex: i, Item: &completed.Output[i]})
			writeRespEvent(c, officialtypes.ResponseStreamEvent{Type: "response.output_item.done", Sequence: 0, OutputIndex: i, Item: &completed.Output[i]})
		}
		// response.completed
		writeRespEvent(c, officialtypes.ResponseStreamEvent{Type: "response.completed", Sequence: 0, Response: &completed})
		c.Writer.Flush()
		return responsesStreamResult{
			outputTokens: outputTokens,
			ttftMs:       ttftMs,
			totalMs:      totalMs,
		}
	}

	startMessageItem()
	// 兜底: 看着像工具标记但解析不出来, 把扣住的当普通文本补发, 别让客户端收空。
	if gate.Holding() && gate.Buffered() != "" {
		writeRespEvent(c, officialtypes.ResponseStreamEvent{
			Type:         "response.output_text.delta",
			Sequence:     0,
			ItemID:       output.ID,
			OutputIndex:  0,
			ContentIndex: 0,
			Delta:        gate.Buffered(),
		})
	}

	donePart := officialtypes.ResponseOutputContent{
		Type:        "output_text",
		Text:        fullText,
		Annotations: []interface{}{},
	}
	completed := officialtypes.NewResponseAPIFull(fullText, model, int64(stats.PromptTokens), int64(outputTokens), int64(stats.CachedTokens), ttftMs, totalMs, stats.Effort)

	// response.output_text.done
	writeRespEvent(c, officialtypes.ResponseStreamEvent{Type: "response.output_text.done", Sequence: 0, ItemID: output.ID, OutputIndex: 0, ContentIndex: 0, Text: fullText})
	// response.content_part.done
	writeRespEvent(c, officialtypes.ResponseStreamEvent{Type: "response.content_part.done", Sequence: 0, ItemID: output.ID, OutputIndex: 0, ContentIndex: 0, Part: donePart})
	// response.output_item.done
	writeRespEvent(c, officialtypes.ResponseStreamEvent{Type: "response.output_item.done", Sequence: 0, OutputIndex: 0, Item: &completed.Output[0]})
	// response.completed
	writeRespEvent(c, officialtypes.ResponseStreamEvent{Type: "response.completed", Sequence: 0, Response: &completed})
	c.Writer.Flush()

	return responsesStreamResult{
		text:         fullText,
		outputTokens: outputTokens,
		ttftMs:       ttftMs,
		totalMs:      totalMs,
	}
}

// writeRespEvent serializes a Response API SSE event and writes it to the client.
func writeRespEvent(c *gin.Context, event officialtypes.ResponseStreamEvent) {
	c.Writer.WriteString("event: " + event.Type + "\n")
	c.Writer.WriteString("data: " + event.String() + "\n\n")
	c.Writer.Flush()
}

func (h *Handler) imageGenerations(c *gin.Context) {
	var req officialtypes.ImageGenerationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": gin.H{
			"message": "Request must be proper JSON",
			"type":    "invalid_request_error",
			"param":   nil,
			"code":    err.Error(),
		}})
		return
	}

	if req.Prompt == "" {
		c.JSON(400, gin.H{"error": gin.H{
			"message": "prompt is required",
			"type":    "invalid_request_error",
			"param":   "prompt",
			"code":    "missing_prompt",
		}})
		return
	}

	if req.N == 0 {
		req.N = 1
	}

	// Build a chat request with image generation enabled
	// 默认走 duck.ai 原生图片模型：POST /duckchat/v1/chat + model=image-generation，
	// 提示词原样进图（不被聊天模型改写）。显式点名一个聊天模型时才走旧路径
	// 「聊天模型 + GenerateImage 工具」（那条路出图由 gpt-image-2 出，原生的是 gpt-image-1.5）。
	// 别名（gpt-image-1.5 / gpt-image-2 等）见 duckgo.ResolveImageModel。
	native, imageModel := duckgo.ResolveImageModel(req.Model)
	var translatedRequest duckgotypes.ApiRequest
	if native {
		translatedRequest = duckgotypes.NewApiRequest(imageModel)
		translatedRequest.ReasoningEffort = "" // 实测能用的 body 里没有该键，空值靠 omitempty 省掉
		translatedRequest.AddMessage("user", req.Prompt)
	} else {
		chatReq := officialtypes.APIRequest{
			Model: imageModel,
			Messages: []officialtypes.ApiMessage{
				{Role: "user", Content: duckgo.StrictImagePrompt(req.Prompt)},
			},
			Stream: false,
		}
		translatedRequest = duckgoConvert.ConvertAPIRequestWithOptions(chatReq, req.ReasoningEffort, false)
		translatedRequest.Metadata.ToolChoice.GenerateImage = true
	}

	proxyUrl := h.proxy.GetProxyIP()
	client := resty.NewStdClient()
	token, err := duckgo.InitXVQD(client, proxyUrl)
	if err != nil {
		c.JSON(upstreamStatus(c, err), gin.H{"error": gin.H{
			"message": "Failed to initialize VQD token",
			"type":    "internal_server_error",
			"code":    err.Error(),
		}})
		return
	}

	response, err := duckgo.POSTconversation(client, translatedRequest, token, proxyUrl)
	if err != nil {
		c.JSON(500, gin.H{"error": gin.H{
			"message": "Failed to generate image",
			"type":    "internal_server_error",
			"code":    err.Error(),
		}})
		return
	}
	defer response.Body.Close()

	if response.StatusCode != 200 {
		c.JSON(response.StatusCode, gin.H{"error": gin.H{
			"message": duckgo.ReadResponseError(response).Error(),
			"type":    "api_error",
			"code":    "upstream_error",
		}})
		return
	}

	result := duckgo.ReadImageResponse(response)

	if len(result.Images) == 0 {
		c.JSON(500, gin.H{"error": gin.H{
			"message": "No images were generated",
			"type":    "internal_server_error",
			"code":    "no_images",
		}})
		return
	}

	// Build OpenAI-compatible response
	imageData := make([]officialtypes.ImageData, 0, len(result.Images))
	for _, img := range result.Images {
		b64 := img.Result
		if b64 == "" && img.Data != nil {
			b64 = img.Data.B64Image
		}
		if b64 == "" {
			continue
		}
		imageData = append(imageData, officialtypes.ImageData{
			B64JSON:       b64,
			RevisedPrompt: imageRevisedPrompt(result),
		})
	}

	c.JSON(200, officialtypes.ImageGenerationResponse{
		Created: time.Now().Unix(),
		Data:    imageData,
	})
}

// imageRevisedPrompt 优先回上游喂给图像服务（gpt-image-2）的真实提示词——用户给的 prompt
// 会被上游改写后再送进去，抓这句才拿得到"实际画的那句话"；没有才退回助手文本。
// ponytail: 出图/改图两处共用一次取舍，别写两遍。
func imageRevisedPrompt(result duckgo.ImageResult) string {
	if result.Prompt != "" {
		return result.Prompt
	}
	return result.Text
}

func (h *Handler) imageEdits(c *gin.Context) {
	// Parse multipart form
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
		// Try JSON body for base64 input
		var req officialtypes.ImageEditRequest
		if jsonErr := c.ShouldBindJSON(&req); jsonErr != nil {
			c.JSON(400, gin.H{"error": gin.H{
				"message": "Request must be multipart form or proper JSON",
				"type":    "invalid_request_error",
				"param":   nil,
				"code":    err.Error(),
			}})
			return
		}
		h.handleImageEditJSON(c, req)
		return
	}

	// Multipart form handling
	prompt := c.Request.FormValue("prompt")
	if prompt == "" {
		c.JSON(400, gin.H{"error": gin.H{
			"message": "prompt is required",
			"type":    "invalid_request_error",
			"param":   "prompt",
			"code":    "missing_prompt",
		}})
		return
	}

	// Read image file
	file, _, err := c.Request.FormFile("image")
	if err != nil {
		c.JSON(400, gin.H{"error": gin.H{
			"message": "image file is required",
			"type":    "invalid_request_error",
			"param":   "image",
			"code":    "missing_image",
		}})
		return
	}
	defer file.Close()

	imageBytes, err := io.ReadAll(file)
	if err != nil {
		c.JSON(500, gin.H{"error": gin.H{
			"message": "Failed to read image file",
			"type":    "internal_server_error",
			"code":    err.Error(),
		}})
		return
	}

	imageB64 := base64.StdEncoding.EncodeToString(imageBytes)
	h.doImageEdit(c, prompt, imageB64)
}

func (h *Handler) handleImageEditJSON(c *gin.Context, req officialtypes.ImageEditRequest) {
	if req.Prompt == "" {
		c.JSON(400, gin.H{"error": gin.H{
			"message": "prompt is required",
			"type":    "invalid_request_error",
			"param":   "prompt",
			"code":    "missing_prompt",
		}})
		return
	}

	if req.Image == "" {
		c.JSON(400, gin.H{"error": gin.H{
			"message": "image is required",
			"type":    "invalid_request_error",
			"param":   "image",
			"code":    "missing_image",
		}})
		return
	}

	h.doImageEdit(c, req.Prompt, req.Image)
}

// decodeImageInput 归一改图输入：既可能是 data URL，也可能是裸 base64（multipart 那条路就是）。
// mime 先取 data URL 里声明的，没声明就用 stdlib 嗅探字节，不猜。
func decodeImageInput(image string) ([]byte, string, error) {
	mimeType := ""
	if strings.HasPrefix(image, "data:") {
		if i := strings.Index(image, ","); i > 0 {
			head := image[len("data:"):i] // 形如 image/png;base64
			if j := strings.Index(head, ";"); j > 0 {
				mimeType = head[:j]
			}
			image = image[i+1:]
		}
	}
	blob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(image))
	if err != nil {
		return nil, "", err
	}
	if len(blob) == 0 {
		return nil, "", errors.New("empty image")
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(blob)
	}
	return blob, mimeType, nil
}

func (h *Handler) doImageEdit(c *gin.Context, prompt string, imageB64 string) {
	// 改图固定走原生图片模型：实测「聊天模型 + GenerateImage + 图」上游回 400 ERR_BAD_REQUEST，
	// 而原生模型单请求就能吃图——先回 role=image-validated（服务端验图发 moderationToken），
	// 再回 partial-image / generated-image。所以请求里的 model 在改图上不参与选路。
	blob, mimeType, err := decodeImageInput(imageB64)
	if err != nil {
		c.JSON(400, gin.H{"error": gin.H{
			"message": "image is not valid base64: " + err.Error(),
			"type":    "invalid_request_error",
			"param":   "image",
			"code":    "invalid_image",
		}})
		return
	}

	// 严格指令：改图指令也是用户原话，不该被聊天模型改写成风格描述。
	nativeReq := duckgotypes.NewApiRequest(duckgo.NativeImageModel)
	nativeReq.ReasoningEffort = ""
	nativeReq.AddMessageWithParts("user", []duckgotypes.ContentPart{
		{Type: "image", MimeType: mimeType, Image: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(blob)},
		{Type: "text", Text: duckgo.StrictImagePrompt(prompt)},
	})

	proxyUrl := h.proxy.GetProxyIP()
	client := resty.NewStdClient()
	token, err := duckgo.InitXVQD(client, proxyUrl)
	if err != nil {
		c.JSON(upstreamStatus(c, err), gin.H{"error": gin.H{
			"message": "Failed to initialize VQD token",
			"type":    "internal_server_error",
			"code":    err.Error(),
		}})
		return
	}

	response, err := duckgo.POSTconversation(client, nativeReq, token, proxyUrl)
	if err != nil {
		c.JSON(500, gin.H{"error": gin.H{
			"message": "Failed to edit image",
			"type":    "internal_server_error",
			"code":    err.Error(),
		}})
		return
	}
	defer response.Body.Close()

	if response.StatusCode != 200 {
		c.JSON(response.StatusCode, gin.H{"error": gin.H{
			"message": duckgo.ReadResponseError(response).Error(),
			"type":    "api_error",
			"code":    "upstream_error",
		}})
		return
	}

	result := duckgo.ReadImageResponse(response)

	if len(result.Images) == 0 {
		c.JSON(500, gin.H{"error": gin.H{
			"message": "No images were generated",
			"type":    "internal_server_error",
			"code":    "no_images",
		}})
		return
	}

	imageData := make([]officialtypes.ImageData, 0, len(result.Images))
	for _, img := range result.Images {
		b64 := img.Result
		if b64 == "" && img.Data != nil {
			b64 = img.Data.B64Image
		}
		if b64 == "" {
			continue
		}
		imageData = append(imageData, officialtypes.ImageData{
			B64JSON:       b64,
			RevisedPrompt: imageRevisedPrompt(result),
		})
	}

	c.JSON(200, officialtypes.ImageGenerationResponse{
		Created: time.Now().Unix(),
		Data:    imageData,
	})
}

func (h *Handler) engines(c *gin.Context) {
	models, statusCode, err := fetchDuckDuckGoModels(h.proxy.GetProxyIP())
	if err != nil {
		c.JSON(statusCode, gin.H{"error": gin.H{
			"message": err.Error(),
			"type":    "upstream_error",
			"code":    "models_fetch_failed",
		}})
		return
	}

	c.JSON(http.StatusOK, models)
}
