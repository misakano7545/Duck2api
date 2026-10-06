package duckgo

import (
	dkgo "aurora/internal/duckgo"
	duckgotypes "aurora/internal/typings/duckgo"
	officialtypes "aurora/internal/typings/official"
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log"
	"math"
	"math/big"
	"strings"

	"github.com/deepteams/webp"
	"github.com/google/uuid"
)

func capReasoningEffort(model string, effort string) string {
	effort = strings.ToLower(strings.TrimSpace(effort))
	// DuckDuckGo 只认 "none"(快速) 和 "low"(思考模式) 两档 —— 2026-10 实测:
	// 透传 medium 会被上游拒成 400 ERR_BAD_REQUEST (models 元数据里 terra/sol/opus/5.4-mini
	// 声明支持 medium, 但 reasoningEffortAccess.entityHasAccess=false, 属未开通),
	// high/max 更不在上游词表里(词表只有 none/low/medium)。所以非空一律压到 low,
	// 不要把用户输入的档位原样透传 —— 那会把请求打成 400。
	// model 参数留着是为了将来按模型白名单放开(放开前先用 /v1/models 的
	// supportedReasoningEffort + reasoningEffortAccess 确认该档位确实可用)。
	switch effort {
	case "", "none":
		return "none"
	default:
		return "low"
	}
}

// contentText 把消息内容压成纯文本(工具历史/系统提示这类不需要多模态的场合用)。
func contentText(content interface{}) string {
	if s, ok := content.(string); ok {
		return s
	}
	if content == nil {
		return ""
	}
	b, err := json.Marshal(content)
	if err != nil {
		return ""
	}
	return string(b)
}

func ConvertAPIRequest(apiRequest officialtypes.APIRequest) duckgotypes.ApiRequest {
	return ConvertAPIRequestWithOptions(apiRequest, "", false)
}

func ConvertAPIRequestWithOptions(apiRequest officialtypes.APIRequest, reasoningEffort string, webSearch bool) duckgotypes.ApiRequest {
	inputModel := apiRequest.Model
	duckgoRequest := duckgotypes.NewApiRequest(inputModel)
	realModel := inputModel

	modelLower := strings.ToLower(inputModel)
	switch {
	case strings.HasPrefix(modelLower, "gpt-3.5"):
		realModel = "gpt-4o-mini"
	case strings.HasPrefix(modelLower, "claude-3-haiku"):
		realModel = "claude-3-haiku-20240307"
	case strings.HasPrefix(modelLower, "llama-3.3-70b"):
		realModel = "meta-llama/Llama-3.3-70B-Instruct-Turbo"
	case strings.HasPrefix(modelLower, "mixtral-8x7b"):
		realModel = "mistralai/Mixtral-8x7B-Instruct-v0.1"
	case strings.HasPrefix(modelLower, "llama-4-scout"):
		realModel = "meta-llama/Llama-4-Scout-17B-16E-Instruct"
	case strings.HasPrefix(modelLower, "mistral-small"):
		realModel = "mistralai/Mistral-Small-24B-Instruct-2501"
	}

	duckgoRequest.Model = realModel

	// Set reasoning effort (cap to model's max)
	if reasoningEffort != "" {
		duckgoRequest.ReasoningEffort = capReasoningEffort(realModel, reasoningEffort)
	} else {
		duckgoRequest.ReasoningEffort = "none" // fast mode
	}

	// Set web search
	if webSearch {
		duckgoRequest.Metadata.ToolChoice.WebSearch = true
	}

	// 上游只有文本通道, 工具历史折叠成文本(与 anthropic/convert.go 同一口径),
	// 否则 agent 的多轮 loop 会掉上下文: tool 结果被丢、助手那次调用也看不见。
	// 工具约定文本(上游没有函数调用通道): 位置很关键 ——
	// 紧随首条消息: 第一轮时它是最新那条(模型才肯照格式吐调用), 后续轮次工具结果排在它后面(模型据此收口)。
	// 放开头(旧写法)模型会无视; 放结尾(旧写法)第二轮模型会对它回 "已了解" 而不干活。
	toolInstruction := ""
	if apiRequest.Tools != nil {
		if b, err := json.Marshal(apiRequest.Tools); err == nil {
			toolInstruction = dkgo.ToolInstruction(string(b))
		}
	}
	inserted := false
	var pendingCalls []string
	for _, message := range apiRequest.Messages {
		if toolInstruction != "" && !inserted && len(duckgoRequest.Messages) > 0 {
			if parts := extractContentParts(toolInstruction); len(parts) > 0 {
				duckgoRequest.AddMessageWithParts("user", parts)
			}
			inserted = true
		}
		role := message.Role
		content := message.Content
		switch role {
		case "system", "tool":
			role = "user"
			if message.Role == "tool" {
				content = "[工具结果] " + contentText(content)
			}
		case "assistant":
			for _, call := range message.ToolCalls {
				pendingCalls = append(pendingCalls, fmt.Sprintf("[调用工具] %s %s", call.Function.Name, call.Function.Arguments))
			}
			if text := contentText(content); text != "" {
				pendingCalls = append(pendingCalls, text)
			}
			content = strings.Join(pendingCalls, "\n")
			pendingCalls = nil
		}
		if role != "user" && role != "assistant" {
			continue
		}

		parts := extractContentParts(content)
		if len(parts) > 0 {
			duckgoRequest.AddMessageWithParts(role, parts)
		}
	}
	if toolInstruction != "" && !inserted {
		if parts := extractContentParts(toolInstruction); len(parts) > 0 {
			duckgoRequest.AddMessageWithParts("user", parts)
		}
	}
	duckgoRequest.DurableStream = newDurableStream()
	return duckgoRequest
}

// FileStore is a function to look up file content by ID
// Set this from the handler to enable file resolution
var FileStore func(fileID string) (filename string, mimeType string, data []byte, ok bool)

func extractContentParts(content interface{}) []duckgotypes.ContentPart {
	if content == nil {
		return nil
	}

	// String content
	if str, ok := content.(string); ok {
		if str == "" {
			return nil
		}
		return []duckgotypes.ContentPart{{Type: "text", Text: str}}
	}

	// Array content (multimodal)
	if arrayContent, ok := content.([]interface{}); ok {
		log.Printf("[DEBUG] extractContentParts: array with %d elements", len(arrayContent))
		var parts []duckgotypes.ContentPart
		for _, element := range arrayContent {
			elementMap, ok := element.(map[string]interface{})
			if !ok {
				continue
			}
			partType, _ := elementMap["type"].(string)
			switch partType {
			case "text":
				if text, ok := elementMap["text"].(string); ok && text != "" {
					parts = append(parts, duckgotypes.ContentPart{Type: "text", Text: text})
				}
			case "image_url":
				// OpenAI format: {"type":"image_url","image_url":{"url":"data:image/png;base64,..."}}
				if imageURL, ok := elementMap["image_url"].(map[string]interface{}); ok {
					if url, ok := imageURL["url"].(string); ok && url != "" {
						mimeType := "image/png"
						if strings.HasPrefix(url, "data:") {
							if idx := strings.Index(url, ";"); idx > 0 {
								mimeType = url[5:idx]
							}
						}
						parts = append(parts, duckgotypes.ContentPart{
							Type:     "image",
							Image:    url,
							MimeType: mimeType,
						})
					}
				}
			case "image":
				// DuckDuckGo native format: {"type":"image","mimeType":"image/webp","image":"data:..."}
				if image, ok := elementMap["image"].(string); ok && image != "" {
					mimeType, _ := elementMap["mimeType"].(string)
					if mimeType == "" {
						mimeType = "image/png"
					}
					parts = append(parts, duckgotypes.ContentPart{
						Type:     "image",
						Image:    image,
						MimeType: mimeType,
					})
				}
			case "input_file":
				// OpenAI format: {"type":"input_file","file_id":"file-xxx"}
				if fileID, ok := elementMap["file_id"].(string); ok && fileID != "" {
					log.Printf("[DEBUG] input_file: file_id=%s", fileID)
					if FileStore != nil {
						if filename, mimeType, data, ok := FileStore(fileID); ok {
							log.Printf("[DEBUG] FileStore found: %s %s %d bytes", filename, mimeType, len(data))
							// Image files → convert to JPEG and send as image part
							if strings.HasPrefix(mimeType, "image/") {
								webpData := convertToWebP(data, mimeType)
								b64 := base64.StdEncoding.EncodeToString(webpData)
								log.Printf("[DEBUG] Image: %d bytes webp, %d chars b64", len(webpData), len(b64))
								parts = append(parts, duckgotypes.ContentPart{
									Type:     "image",
									Image:    "data:image/webp;base64," + b64,
									MimeType: "image/webp",
								})
							} else {
								// Non-image files (PDF, text, etc.) → embed as text context
								fileText := extractTextContent(data, mimeType)
								if len(fileText) > 50000 {
									fileText = fileText[:50000] + "\n...(truncated)"
								}
								parts = append(parts, duckgotypes.ContentPart{
									Type: "text",
									Text: "[File: " + filename + "]\n" + fileText,
								})
							}
							continue
						}
					}
					// Fallback: send file ID as text reference
					parts = append(parts, duckgotypes.ContentPart{
						Type: "text",
						Text: "[file:" + fileID + "]",
					})
				}
			}
		}
		return parts
	}

	return nil
}

// extractContent extracts plain text content (backward compatible)
func extractContent(content interface{}) string {
	parts := extractContentParts(content)
	if parts == nil {
		return ""
	}
	var text strings.Builder
	for _, p := range parts {
		if p.Type == "text" {
			text.WriteString(p.Text)
		}
	}
	return text.String()
}

func newDurableStream() duckgotypes.DurableStream {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return duckgotypes.DurableStream{}
	}

	return duckgotypes.DurableStream{
		MessageID:      uuid.NewString(),
		ConversationID: uuid.NewString(),
		PublicKey: duckgotypes.PublicKey{
			Alg:    "RSA-OAEP-256",
			E:      base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
			Ext:    true,
			KeyOps: []string{"encrypt"},
			Kty:    "RSA",
			N:      base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			Use:    "enc",
		},
	}
}

// convertToWebP converts image data to WebP format with compression and resizing
func convertToWebP(data []byte, mimeType string) []byte {
	var img image.Image
	var err error

	// Decode based on mime type
	if strings.Contains(mimeType, "jpeg") || strings.Contains(mimeType, "jpg") {
		img, err = jpeg.Decode(bytes.NewReader(data))
	} else if strings.Contains(mimeType, "png") {
		img, err = png.Decode(bytes.NewReader(data))
	}
	if img == nil {
		img, _, err = image.Decode(bytes.NewReader(data))
	}
	if err != nil || img == nil {
		return data
	}

	// Resize if too large (max 512px on longest side)
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	maxSize := 512
	if w > maxSize || h > maxSize {
		ratio := float64(maxSize) / math.Max(float64(w), float64(h))
		newW := int(float64(w) * ratio)
		newH := int(float64(h) * ratio)
		if newW < 1 {
			newW = 1
		}
		if newH < 1 {
			newH = 1
		}
		resized := image.NewRGBA(image.Rect(0, 0, newW, newH))
		for y := 0; y < newH; y++ {
			for x := 0; x < newW; x++ {
				srcX := bounds.Min.X + x*w/newW
				srcY := bounds.Min.Y + y*h/newH
				resized.Set(x, y, img.At(srcX, srcY))
			}
		}
		img = resized
	} else {
		rgba := image.NewRGBA(bounds)
		draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)
		img = rgba
	}

	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, &webp.EncoderOptions{Quality: 75}); err != nil {
		// Fallback to JPEG if webp fails
		var jpegBuf bytes.Buffer
		jpeg.Encode(&jpegBuf, img, &jpeg.Options{Quality: 75})
		return jpegBuf.Bytes()
	}
	return buf.Bytes()
}

// extractTextContent extracts readable text from file data
func extractTextContent(data []byte, mimeType string) string {
	text := string(data)
	// Remove null bytes
	text = strings.ReplaceAll(text, "\x00", "")
	return text
}
