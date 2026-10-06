package duckgo

import (
	duckgotypes "aurora/internal/typings/duckgo"
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// NativeImageModel 是 duck.ai 的原生图片模型 id：POST /duckchat/v1/chat 直接带上它，
// 由模型自己的出图通道出图（提示词原样进图，不被聊天模型改写）。
// 旧的专用图片端点 /duckchat/v1/images（同一个模型）已 410 ERR_ENDPOINT_DEPRECATED。
const NativeImageModel = "image-generation"

// ToolImageChatModel 是「聊天模型 + GenerateImage 工具」那条路的默认载体模型：
// 出图由 gpt-image-2 出（原生那条是 gpt-image-1.5），代价是提示词被上游改写。
const ToolImageChatModel = "gpt-5.6-luna"

// ResolveImageModel 归一化出图请求里的 model 别名，返回 (是否走原生图片模型, 实际模型名)。
//
// 别名表按"客户端能看见的名字"设计：成品图 C2PA 里的生成器名（gpt-image-1.5 / gpt-image-2）
// 都能当 model 传进来，落到真正出那张图的上游路径上——否则 gpt-image-* 当 model 直传上游必 404
// ERR_MODEL_UNAVAILABLE（实测 gpt-image-1/gpt-image-2/gpt-image-1-mini/dall-e-3 全 404）。
func ResolveImageModel(model string) (native bool, real string) {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "", "image-generation", "gpt-image-1.5", "gpt-image-1":
		return true, NativeImageModel
	case "gpt-image-2":
		return false, ToolImageChatModel
	}
	return false, model
}

// ImageResult holds the extracted image data from the SSE stream
type ImageResult struct {
	Text   string
	Images []duckgotypes.ImagePart
	// Prompt 是上游转手喂给图像服务（gpt-image-2）的真实提示词（已改写），见 ApiResponse.GetImageGenPrompt。
	Prompt string
}

// ReadImageResponse reads the SSE response and extracts both text and image parts
func ReadImageResponse(response *http.Response) ImageResult {
	reader := bufio.NewReader(response.Body)
	var textBuilder strings.Builder
	var images []duckgotypes.ImagePart
	var drafts []duckgotypes.ImagePart // 扩散中段废稿（多眼/糊），只有拿不到成品时才兜底
	var imagePrompt string

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return ImageResult{}
		}
		if len(line) < 6 {
			continue
		}
		line = line[6:]
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[DONE]") || strings.HasPrefix(line, "[PING]") || strings.HasPrefix(line, "[CHAT_TITLE") {
			continue
		}

		var apiResp duckgotypes.ApiResponse
		err = json.Unmarshal([]byte(line), &apiResp)
		if err != nil || apiResp.Action != "success" {
			continue
		}

		if apiResp.Message != "" {
			textBuilder.WriteString(apiResp.Message)
		}

		// 出图工具调用：这里带的是喂给图像服务的真实提示词（上游改写后）。
		if apiResp.ToolName == "GenerateImage" && apiResp.ToolArguments != "" {
			if p := apiResp.GetImageGenPrompt(); p != "" {
				imagePrompt = p
			}
		}

		// 原生图片模型（POST /duckchat/v1/chat, model=image-generation）：
		// role 为 partial-image（扩散中段废稿）/ generated-image（成品），图在 result。
		if apiResp.Result != "" && (apiResp.Role == "generated-image" || apiResp.Role == "partial-image") {
			part := duckgotypes.ImagePart{Type: "generated-image", Result: apiResp.Result}
			if apiResp.Role == "generated-image" {
				images = append(images, part)
			} else {
				drafts = append(drafts, part)
			}
		}

		// Extract image from parts (legacy format)
		for _, part := range apiResp.Parts {
			if part.Type == "generated-image" || part.Type == "image" {
				images = append(images, part)
			}
		}

		// Extract image from data field (new format: ui-component with GenerateImage)
		if apiResp.ToolName == "GenerateImage" && apiResp.Data != nil {
			if imgData := apiResp.GetImageData(); imgData != nil && imgData.B64Image != "" {
				part := duckgotypes.ImagePart{
					Type:   "generated-image",
					Result: imgData.B64Image,
					Format: imgData.Format,
					Width:  imgData.Width,
					Height: imgData.Height,
				}
				// status=partial 是同一张图的扩散中间态：当输出图返回会送出一张多眼扭曲的废稿。
				if imgData.Status == "partial" {
					drafts = append(drafts, part)
				} else {
					images = append(images, part)
				}
			}
		}
	}

	// 只有废稿、没等到成品（流被截断）时才退回废稿，总比空手好。
	if len(images) == 0 {
		images = drafts
	}
	return ImageResult{
		Text:   textBuilder.String(),
		Images: images,
		Prompt: imagePrompt,
	}
}
