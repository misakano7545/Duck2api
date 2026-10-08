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
	// CustomTools 是 type=="custom"（自由格式输入、没有 JSON Schema）的工具名。
	// 只用于回写：Responses 要把它们回成 custom_tool_call。json:"-" 不上游。
	CustomTools []string `json:"-"`
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

// CollectedTools 汇总这次请求里客户端声明的所有工具。
//
// 标准位置是顶层 tools；但 Codex CLI 0.160 根本不发顶层 tools —— 它把工具塞在
// input 里一个 {type:"additional_tools", role:"developer", tools:[...]} 项里，外面
// 还套一层 {type:"namespace"}。只读顶层字段的话，客户端明明把工具发过来了，我们
// 当没看见，模型于是回「没有可用的工具」。
func (r ResponseAPIRequest) CollectedTools() []interface{} {
	var out []interface{}
	if list, ok := r.Tools.([]interface{}); ok {
		out = append(out, flattenToolEntries(list)...)
	}
	if items, ok := r.Input.([]interface{}); ok {
		for _, item := range items {
			m, ok := item.(map[string]interface{})
			if !ok || m["type"] != "additional_tools" {
				continue
			}
			if list, ok := m["tools"].([]interface{}); ok {
				out = append(out, flattenToolEntries(list)...)
			}
		}
	}
	return out
}

// flattenToolEntries 展开 namespace 包装（Codex 把 exec/wait 这些放在
// {type:"namespace", name:"functions"} 里），留下真正的工具条目。
func flattenToolEntries(list []interface{}) []interface{} {
	var out []interface{}
	for _, e := range list {
		m, ok := e.(map[string]interface{})
		if !ok {
			continue
		}
		if m["type"] == "namespace" {
			if inner, ok := m["tools"].([]interface{}); ok {
				out = append(out, flattenToolEntries(inner)...)
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

// CustomToolNames 返回 type=="custom" 的工具名（自由格式输入，没有 JSON Schema）。
// 回写 Responses 时它们要变成 custom_tool_call 而不是 function_call。
func (r ResponseAPIRequest) CustomToolNames() []string {
	var names []string
	for _, e := range r.CollectedTools() {
		m, ok := e.(map[string]interface{})
		if !ok || m["type"] != "custom" {
			continue
		}
		if n, ok := m["name"].(string); ok && n != "" {
			names = append(names, n)
		}
	}
	return names
}

func (r ResponseAPIRequest) ToChatCompletionRequest() APIRequest {
	request := APIRequest{
		Model:  r.Model,
		Stream: r.Stream,
		// 上游没有函数调用通道, Tools 只是提示词模拟的输入(见 duckgo/toolcall.go);
		// 不透传的话走 responses 路的客户端(Codex CLI / Hermes codex_responses)
		// 会连工具定义一起静默丢掉, 表现为"模型说它不能执行命令"。
		Tools:      r.CollectedTools(),
		ToolChoice: r.ToolChoice,
		// 哪些是自由格式工具要一路带到回写那一步(Responses 要回 custom_tool_call)
		CustomTools: r.CustomToolNames(),
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
	case "function_call", "custom_tool_call":
		// 助手那次的调用必须留在历史里。丢掉的后果不是"少点上下文": 第二轮带着工具
		// 结果上去却没有"谁要的", 模型会把同一个调用再发一遍, 多轮 loop 永远不闭合。
		// 措辞与 chat 路径同一口径(见 conversion/requests/duckgo: [调用工具] name args)。
		// custom_tool_call(Codex 的 exec)入参是原始文本, 放在 input 字段里。
		name, _ := itemMap["name"].(string)
		if name == "" {
			return nil
		}
		args := responseContentText(itemMap["arguments"])
		if args == "" {
			if b, err := json.Marshal(itemMap["arguments"]); err == nil && string(b) != "null" {
				args = string(b)
			}
		}
		if args == "" {
			args = responseContentText(itemMap["input"]) // custom_tool_call 的原文
		}
		return []ApiMessage{{Role: "assistant", Content: fmt.Sprintf("[调用工具] %s %s", name, args)}}
	case "function_call_output", "custom_tool_call_output":
		output := responseContentText(itemMap["output"])
		if output == "" {
			output = responseContentText(itemMap["input"])
		}
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
