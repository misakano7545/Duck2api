package duckgo

import (
	"encoding/json"
	"fmt"
	"sort"
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
func ToolInstruction(toolsDoc string, hasCustom bool) string {
	// 措辞要点(踩过的坑): 只说 "你可以调用下列工具" 会被 claude 系模型当成越权注入而拒绝
	// (回 "工具列表与我的系统提示不符"); 必须点明这是本次 API 调用的线格式约定、
	// 函数由客户端注册、执行方是客户端。改措辞就是这条链路的调参旋钮。
	doc := "【API 线格式约定】本次请求由客户端程序发出, 下列函数由客户端注册, 由客户端执行。\n" +
		"你内置的搜索等能力与本约定无关; 当用户的话需要这些函数的数据才能回答, 或用户点名要求调用时, " +
		"你必须只输出下面这一行(可多行表示并行调用), 不要解释、不要直接凭自己回答、不要写成代码块:\n" +
		toolCallOpen + `{"name":"函数名","arguments":{参数}}` + toolCallClose + "\n" +
		"客户端会执行它并把结果回传给你。若问题完全不需要这些函数, 正常回答即可。\n"
	if hasCustom {
		// Codex 的 exec 这类工具没有 JSON Schema: 它的入参是原始文本(JS 源码)。
		doc += "标着 [原始文本输入] 的工具没有参数表, 把要发给它的原文整个放进 arguments 的 " +
			"input 字段(字符串), 例如 " + toolCallOpen + `{"name":"exec","arguments":{"input":"const r = await tools.exec_command({cmd:\"date\"}); text(r)"}}` + toolCallClose + "\n"
	}
	return doc + "\n客户端注册的函数:\n" + toolsDoc
}

// 工具块是注入提示词里最大的一块, 而它整个是我们自己造的。实测一个 34 工具的客户端
// (Hermes 默认) 光 schema 就 48,279 字符 —— 比它的 system 提示(36,224)还大, 直接把
// 请求顶过上游的单请求上限。模型要发出正确的调用只认三样: 函数名、参数名、以及够用来
// 选对工具的描述。所以按 `名字(参数:类型) — 描述` 压成一行一个, 其余的 JSON Schema
// 细节(嵌套、枚举、additionalProperties…)对提示词模拟这条链路没有价值。
const (
	toolLineDescLimit = 80  // 每个工具描述保留多少字符
	customDescLimit   = 400 // 自由格式工具没有参数表, 描述就是它唯一的 API 文档, 多留些
	toolDocCharLimit  = 8000
)

// CompactToolList 把客户端的三种 tools 形状压成紧凑清单。
// chat/OpenAI 是 {type,function:{name,parameters}}, responses 是平铺的
// {type,name,parameters}, anthropic 是 {name,input_schema}, 另有 {type:"custom"}
// 这种没有参数表的自由格式工具(Codex 的 exec)。认不出的形状原样退回 JSON ——
// 宁可没省下体积, 也不要丢工具。
func CompactToolList(tools interface{}) string {
	if tools == nil {
		return ""
	}
	list, ok := tools.([]interface{})
	if !ok {
		if b, err := json.Marshal(tools); err == nil {
			return string(b)
		}
		return ""
	}

	var sb strings.Builder
	for i, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		fn, _ := m["function"].(map[string]interface{}) // chat 形状
		if fn == nil {
			fn = m // responses / anthropic
		}
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		custom, _ := fn["type"].(string)
		isCustom := custom == "custom"

		var line string
		if isCustom {
			line = name + "[原始文本输入]"
		} else {
			schema, _ := fn["parameters"].(map[string]interface{})
			if schema == nil {
				schema, _ = fn["input_schema"].(map[string]interface{})
			}
			line = name + compactParams(schema)
		}

		if sb.Len() >= toolDocCharLimit {
			sb.WriteString(fmt.Sprintf("\n(其余 %d 个工具因上游输入上限省略, 名字: %s)", len(list)-i, name))
			break
		}
		limit := toolLineDescLimit
		if isCustom {
			limit = customDescLimit
		}
		if desc, _ := fn["description"].(string); desc != "" {
			line += " — " + truncateRunes(strings.Join(strings.Fields(desc), " "), limit)
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// compactParams 把 JSON Schema 的 properties/required 压成 "(a:string, b:integer*)"
// (带 * 的是必填)。没有参数就回 "()"。
func compactParams(schema map[string]interface{}) string {
	if schema == nil {
		return "()"
	}
	props, _ := schema["properties"].(map[string]interface{})
	if len(props) == 0 {
		return "()"
	}
	required := map[string]bool{}
	if reqs, ok := schema["required"].([]interface{}); ok {
		for _, r := range reqs {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	names := make([]string, 0, len(props))
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names) // map 遍历无序, 定序才能让同一请求每次生成同一份提示
	parts := make([]string, 0, len(names))
	for _, n := range names {
		typ := "any"
		if pm, ok := props[n].(map[string]interface{}); ok {
			if t, ok := pm["type"].(string); ok && t != "" {
				typ = t
			}
		}
		p := n + ":" + typ
		if required[n] {
			p += "*"
		}
		parts = append(parts, p)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
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

// ResponsesToolCalls 把解析出的调用转成 Responses API 的输出项。
// 自由格式工具(custom, 如 Codex 的 exec)要回成 custom_tool_call + input(原始文本,
// 不是 JSON 参数) —— 客户端按 item 类型分派, 回成 function_call 它不认。
// ponytail: 整块一次性给出, 不发 function_call_arguments.delta —— 与 chat 路径
// 「工具调用不分块」同一取舍(见 OfficialToolCalls 上方注释)。
func ResponsesToolCalls(calls []ToolCall, custom map[string]bool) []officialtypes.ResponseOutput {
	out := make([]officialtypes.ResponseOutput, 0, len(calls))
	for _, call := range calls {
		id := util.RandomHexadecimalString()
		item := officialtypes.ResponseOutput{
			ID:     id,
			Status: "completed",
			Name:   call.Name,
			CallID: "call_" + id,
		}
		if custom[call.Name] {
			item.Type = "custom_tool_call"
			item.ID = "ctc_" + id
			item.Input = customToolInput(call.Arguments)
		} else {
			item.Type = "function_call"
			item.ID = "fc_" + id
			item.Arguments = call.Arguments
		}
		out = append(out, item)
	}
	return out
}

// customToolInput 从约定里取的 arguments 拆出原始文本。
// 约定让模型写 {"input":"<原文>"}; 拿不到(模型直接吐了原文)就原样返回 arguments 文本,
// 总比回一个空 input 让客户端执行空脚本强。
func customToolInput(arguments string) string {
	var envelope struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal([]byte(arguments), &envelope); err == nil && envelope.Input != "" {
		return envelope.Input
	}
	return arguments
}

// StreamGate 边收边发的闸门。约定里模型需要调用工具时应只吐 <tool_call>{...}</tool_call>，
// 闸门负责把它扣住、交给 ParseToolCalls，其余文本照常流式透传。
//
// 判定是**滑窗**的，不看「这是不是第一段」：只要缓冲里出现完整标记就切到扣住。
// 早先的版本只在第一段可见输出上判定一次 —— 模型先吐半句人话再给调用是常态，那样会被
// 永久判成「不是工具调用」，标记当普通正文漏给客户端，那一轮 tool_calls 直接为空。
type StreamGate struct {
	active bool // 只有本次请求带了工具定义才启用
	hold   bool
	buf    strings.Builder
}

func NewStreamGate(tools bool) *StreamGate { return &StreamGate{active: tools} }

// Push 喂一段增量, 返回可以立刻下发的文本; hold=true 表示这段先扣住不发。
//
// 除「可能是标记前缀」的尾巴外一切照常下发, 所以普通聊天的流式不受影响; 那截尾巴最多
// len(toolCallOpen)-1 字节, 是必要的 -- 否则标记跨越分块到达时会被切成两半、永远认不出来。
func (g *StreamGate) Push(chunk string) (emit string, hold bool) {
	if !g.active {
		return chunk, false
	}
	g.buf.WriteString(chunk)
	if g.hold {
		return "", true
	}
	raw := g.buf.String()
	if i := strings.Index(raw, toolCallOpen); i >= 0 {
		g.buf.Reset()
		g.buf.WriteString(raw[i:])
		g.hold = true
		if i == 0 {
			return "", true // 没有正文可发
		}
		return raw[:i], false // 标记之前的是正文，已到手的先发出去
	}
	// 没有完整标记: 只扣住最长的、恰好是标记前缀的尾巴。
	keep := 0
	for k := len(toolCallOpen) - 1; k > 0; k-- {
		if k <= len(raw) && raw[len(raw)-k:] == toolCallOpen[:k] {
			keep = k
			break
		}
	}
	if keep > 0 && keep == len(raw) {
		return "", true // 整段都还是前缀, 一个字节都发不了
	}
	out := raw[:len(raw)-keep]
	g.buf.Reset()
	g.buf.WriteString(raw[len(raw)-keep:])
	return out, false
}

// Holding 表示当前扣住的可能是工具调用。
func (g *StreamGate) Holding() bool { return g.active && g.hold }

// Buffered 取出扣住的文本, 收尾时用于解析。
func (g *StreamGate) Buffered() string { return g.buf.String() }
