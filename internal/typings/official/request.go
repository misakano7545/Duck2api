package official

import (
	"encoding/json"
	"fmt"
	"strings"
)

type APIRequest struct {
	Messages  []ApiMessage `json:"messages"`
	Stream    bool         `json:"stream"`
	Model     string       `json:"model"`
	PluginIDs []string     `json:"plugin_ids"`
	// Extra fields for Duck.ai features (not standard OpenAI)
	ReasoningEffort string `json:"reasoning_effort,omitempty"` // "none", "low", "medium", "high"
	WebSearch       *bool  `json:"web_search,omitempty"`       // enable web search
	// Tools / ToolChoice: 函数调用定义。上游 duck.ai 没有函数调用通道, 由
	// internal/conversion/requests/duckgo 把它们注入提示词模拟, 见 internal/duckgo/toolcall.go。
	Tools      interface{} `json:"tools,omitempty"`
	ToolChoice interface{} `json:"tool_choice,omitempty"`
}

type ApiMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
	// ToolCalls 是助手历史里的工具调用; DuckDuckGo 只有文本通道, 转换时会折叠成文本
	// 以保持多轮历史连贯(详见 conversion/requests/duckgo)。
	ToolCalls []ToolCallChunk `json:"tool_calls,omitempty"`
}

type ResponseAPIRequest struct {
	Model              string      `json:"model"`
	Input              interface{} `json:"input"`
	Instructions       string      `json:"instructions"`
	Stream             bool        `json:"stream"`
	PreviousResponseID string      `json:"previous_response_id"`
	MaxOutputTokens    int         `json:"max_output_tokens"`
	Tools              interface{} `json:"tools"`
	ToolChoice         interface{} `json:"tool_choice"`
	ReasoningEffort    string      `json:"reasoning_effort,omitempty"`
}

func (r ResponseAPIRequest) ToChatCompletionRequest() APIRequest {
	request := APIRequest{
		Model:  r.Model,
		Stream: r.Stream,
		// 上游没有函数调用通道, Tools 只是提示词模拟的输入(见 duckgo/toolcall.go);
		// 不透传的话走 responses 路的客户端(Codex CLI / Hermes codex_responses)
		// 会连工具定义一起静默丢掉, 表现为"模型说它不能执行命令"。
		Tools:      r.Tools,
		ToolChoice: r.ToolChoice,
	}

	if strings.TrimSpace(request.Model) == "" {
		request.Model = "gpt-5.6-luna"
	}
	if strings.TrimSpace(r.Instructions) != "" {
		request.Messages = append(request.Messages, ApiMessage{
			Role:    "system",
			Content: r.Instructions,
		})
	}

	request.Messages = append(request.Messages, responseInputMessages(r.Input)...)
	return request
}

func responseInputMessages(input interface{}) []ApiMessage {
	switch value := input.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return []ApiMessage{{Role: "user", Content: value}}
	case []interface{}:
		messages := make([]ApiMessage, 0, len(value))
		for _, item := range value {
			messages = append(messages, responseInputItemToMessages(item)...)
		}
		return messages
	default:
		return nil
	}
}

func responseInputItemToMessages(item interface{}) []ApiMessage {
	itemMap, ok := item.(map[string]interface{})
	if !ok {
		return nil
	}

	itemType, _ := itemMap["type"].(string)
	switch itemType {
	case "message", "":
		role, _ := itemMap["role"].(string)
		if role == "" {
			role = "user"
		}
		content := responseContentText(itemMap["content"])
		if strings.TrimSpace(content) == "" {
			return nil
		}
		return []ApiMessage{{Role: role, Content: content}}
	case "function_call":
		// 助手那次的调用必须留在历史里。丢掉的后果不是"少点上下文": 第二轮带着工具
		// 结果上去却没有"谁要的", 模型会把同一个调用再发一遍, 多轮 loop 永远不闭合。
		// 措辞与 chat 路径同一口径(见 conversion/requests/duckgo: [调用工具] name args)。
		name, _ := itemMap["name"].(string)
		if name == "" {
			return nil
		}
		args := responseContentText(itemMap["arguments"])
		if args == "" {
			if b, err := json.Marshal(itemMap["arguments"]); err == nil {
				args = string(b)
			}
		}
		return []ApiMessage{{Role: "assistant", Content: fmt.Sprintf("[调用工具] %s %s", name, args)}}
	case "function_call_output":
		output := responseContentText(itemMap["output"])
		if output == "" {
			return nil
		}
		// role=tool 由转换层加上 [工具结果] 前缀, 与 chat 路径一致。
		return []ApiMessage{{Role: "tool", Content: output}}
	default:
		return nil
	}
}

func responseContentText(content interface{}) string {
	switch value := content.(type) {
	case string:
		return value
	case []interface{}:
		var text strings.Builder
		for _, part := range value {
			partMap, ok := part.(map[string]interface{})
			if !ok {
				continue
			}
			partType, _ := partMap["type"].(string)
			switch partType {
			case "input_text", "output_text", "text", "":
				if partText, ok := partMap["text"].(string); ok {
					text.WriteString(partText)
				}
			}
		}
		return text.String()
	default:
		return ""
	}
}

type OpenAISessionToken struct {
	SessionToken string `json:"session_token"`
}

type OpenAIRefreshToken struct {
	RefreshToken string `json:"refresh_token"`
}
