package duckgo

import (
	"encoding/json"
	"strings"

	officialtypes "aurora/internal/typings/official"
	"aurora/internal/util"
)

// duck.ai 上游没有函数调用通道(请求体只有 canUseTools + 自家工具的固定枚举:
// WebSearch/NewsSearch/VideosSearch/LocalSearch/WeatherForecast/GenerateImage),
// 所以工具调用只能"提示词模拟": 把工具定义写进 system 提示, 约定模型需要调用时
// 只吐 <tool_call>{"name":..,"arguments":{..}}</tool_call>, 代理解析后按各协议
// 的原生结构回写(OpenAI tool_calls / Anthropic tool_use)。
//
// ponytail: 靠提示词约束, 模型偶尔会跑偏。跑偏的后果只是当普通文本返回, 不会写坏数据;
// 要更稳就得换上支持原生 function calling 的上游。
const (
	toolCallOpen  = "<tool_call>"
	toolCallClose = "</tool_call>"
)

// ToolCall 一次工具调用。Arguments 是原始 JSON 文本, 直接回写不再重新序列化。
type ToolCall struct {
	Name      string
	Arguments string
}

// ToolInstruction 生成注入 system 的工具说明与输出约定。
func ToolInstruction(toolsJSON string) string {
	// 措辞要点(踩过的坑): 只说 "你可以调用下列工具" 会被 claude 系模型当成越权注入而拒绝
	// (回 "工具列表与我的系统提示不符"); 必须点明这是本次 API 调用的线格式约定、
	// 函数由客户端注册、执行方是客户端。改措辞就是这条链路的调参旋钮。
	return "【API 线格式约定】本次请求由客户端程序发出, 下列函数由客户端注册, 由客户端执行。\n" +
		"你内置的搜索等能力与本约定无关; 当用户的话需要这些函数的数据才能回答, 或用户点名要求调用时, " +
		"你必须只输出下面这一行(可多行表示并行调用), 不要解释、不要直接凭自己回答、不要写成代码块:\n" +
		toolCallOpen + `{"name":"函数名","arguments":{参数}}` + toolCallClose + "\n" +
		"客户端会执行它并把结果回传给你。若问题完全不需要这些函数, 正常回答即可。\n\n" +
		"客户端注册的函数(JSON Schema):\n" + toolsJSON
}

// ParseToolCalls 从模型输出里解析工具调用, 没有则返回 nil。
func ParseToolCalls(text string) []ToolCall {
	var calls []ToolCall
	for {
		start := strings.Index(text, toolCallOpen)
		if start < 0 {
			return calls
		}
		end := strings.Index(text[start:], toolCallClose)
		if end < 0 {
			return calls
		}
		body := strings.TrimSpace(text[start+len(toolCallOpen) : start+end])
		text = text[start+end+len(toolCallClose):]

		var raw struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(body), &raw); err != nil || raw.Name == "" {
			continue
		}
		args := strings.TrimSpace(string(raw.Arguments))
		if args == "" {
			args = "{}"
		}
		calls = append(calls, ToolCall{Name: raw.Name, Arguments: args})
	}
}

// OfficialToolCalls 把解析出的调用转成 OpenAI 原生 tool_calls 结构。
func OfficialToolCalls(calls []ToolCall) []officialtypes.ToolCallChunk {
	out := make([]officialtypes.ToolCallChunk, 0, len(calls))
	for i, call := range calls {
		out = append(out, officialtypes.ToolCallChunk{
			Index:    i,
			ID:       "call_" + util.RandomHexadecimalString(),
			Type:     "function",
			Function: officialtypes.ToolCallFunction{Name: call.Name, Arguments: call.Arguments},
		})
	}
	return out
}

// StreamGate 边收边发的闸门。契约要求"调用工具时输出以 <tool_call> 开头",
// 所以开头几个字符就能判定: 一旦确认不是工具调用就原样透传, 普通聊天的流式不受影响。
type StreamGate struct {
	active  bool // 只有本次请求带了工具定义才启用
	decided bool
	hold    bool
	buf     strings.Builder
}

func NewStreamGate(tools bool) *StreamGate { return &StreamGate{active: tools} }

// Push 喂一段增量, 返回可以立刻下发的文本; hold=true 表示这段先扣住不发。
func (g *StreamGate) Push(chunk string) (emit string, hold bool) {
	if !g.active {
		return chunk, false
	}
	if g.decided {
		if g.hold {
			g.buf.WriteString(chunk)
			return "", true
		}
		return chunk, false
	}
	g.buf.WriteString(chunk)
	cur := strings.TrimLeft(g.buf.String(), " \n\r\t")
	if cur == "" {
		return "", true
	}
	if strings.HasPrefix(toolCallOpen, cur) {
		return "", true // 还看不出是不是工具调用, 再等一块
	}
	g.decided = true
	if strings.HasPrefix(cur, toolCallOpen) {
		g.hold = true
		return "", true
	}
	out := g.buf.String() // 不是工具调用: 把扣住的补发, 之后正常流式
	g.buf.Reset()
	return out, false
}

// Holding 表示当前扣住的可能是工具调用。
func (g *StreamGate) Holding() bool { return g.active && g.hold }

// Buffered 取出扣住的文本, 收尾时用于解析。
func (g *StreamGate) Buffered() string { return g.buf.String() }
