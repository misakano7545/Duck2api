package duckgo

import (
	"bytes"
	"errors"
	"net/http"
)

// 上游错误类型。实测 2026-10（同一次会话里对照得出，别把两者混为一谈）：
//
//	ERR_INPUT_LIMIT         单请求输入超上限。确定性失败 —— 12 分钟完全空闲后单发
//	                        36000 字符仍是它，3.7s 快速返回，重试毫无意义。
//	ERR_CONVERSATION_LIMIT  会话累计超限，同属确定性。
//	ERR_MODEL_RESTRICTED    匿名调用者没有该模型权限（上游档位策略，不是网关的错）。
//	ERR_MODEL_UNAVAILABLE   模型不存在。
//	ERR_RATE_LIMIT          按客户端指纹的窗口限流 —— 换一个被接受的 UA 指纹当场就有新桶。
//	ERR_CHALLENGE           挑战被拒（UA 平台不符 / UA 与挑战里的 navigator.userAgent 不一致）。
//	ERR_BN_LIMIT            出口 IP 被拒，换指纹没用，得换出口。
//
// 这几种原先全被压成客户端看到的**同一个光秃秃 429**：Hermes 分不清"我的输入太大"
// 和"我被限速了"，只会连撞三次然后报"provider unavailable"。
type UpstreamErrorType string

const (
	ErrTypeUnknown           UpstreamErrorType = ""
	ErrTypeInputLimit        UpstreamErrorType = "ERR_INPUT_LIMIT"
	ErrTypeConversationLimit UpstreamErrorType = "ERR_CONVERSATION_LIMIT"
	ErrTypeModelRestricted   UpstreamErrorType = "ERR_MODEL_RESTRICTED"
	ErrTypeModelUnavailable  UpstreamErrorType = "ERR_MODEL_UNAVAILABLE"
	ErrTypeRateLimit         UpstreamErrorType = "ERR_RATE_LIMIT"
	ErrTypeChallenge         UpstreamErrorType = "ERR_CHALLENGE"
	ErrTypeBNLimit           UpstreamErrorType = "ERR_BN_LIMIT"
)

// 顺序即匹配顺序：先长后短，避免 ERR_MODEL 前缀互相抢。
var upstreamErrorTypes = []UpstreamErrorType{
	ErrTypeConversationLimit,
	ErrTypeModelRestricted,
	ErrTypeModelUnavailable,
	ErrTypeInputLimit,
	ErrTypeRateLimit,
	ErrTypeChallenge,
	ErrTypeBNLimit,
}

// UpstreamErrorTypeOf 从上游响应体里认出错误类型，认不出返回 ErrTypeUnknown。
func UpstreamErrorTypeOf(body []byte) UpstreamErrorType {
	for _, t := range upstreamErrorTypes {
		if bytes.Contains(body, []byte(t)) {
			return t
		}
	}
	return ErrTypeUnknown
}

// ErrInputTooLarge 是网关自己判定的"这条请求塞不进上游单请求上限"——在花掉上游调用
// 之前就拒绝。首条消息（system / 工具约定）自己超上限时走这里，不截断它。
var ErrInputTooLarge = errors.New("request exceeds the upstream per-request input limit")

// WorthRetrying 报告这个上游错误值不值得重试。
//
// 输入/会话超限与模型权限是确定性的：重试只会把挑战退避（1+2+4+8+16s × 最多 4 轮）
// 再走一遍，客户端白等约 67s 才拿到一个分不清原因的 429。出口被拒同理 —— 换 UA
// 指纹在同一条出口上不解决任何问题，得换出口 IP（PROXY_URL）。
// 其余（限流、挑战）换个指纹就能拿新桶，值得重试。
func WorthRetrying(t UpstreamErrorType) bool {
	switch t {
	case ErrTypeInputLimit, ErrTypeConversationLimit,
		ErrTypeModelRestricted, ErrTypeModelUnavailable, ErrTypeBNLimit:
		return false
	}
	return true
}

// ClientError 把上游错误类型映射成给客户端的 (状态码, error.type, error.code)。
// 客户端要能一眼分清"这次请求本身太大"和"现在被限速了，等会儿再来"。
func ClientError(t UpstreamErrorType) (int, string, string) {
	switch t {
	case ErrTypeInputLimit, ErrTypeConversationLimit:
		return http.StatusBadRequest, "invalid_request_error", "context_length_exceeded"
	case ErrTypeModelRestricted, ErrTypeModelUnavailable:
		return http.StatusBadRequest, "invalid_request_error", "model_not_available"
	case ErrTypeRateLimit:
		return http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded"
	case ErrTypeChallenge:
		return http.StatusTooManyRequests, "rate_limit_error", "challenge_unavailable"
	case ErrTypeBNLimit:
		return http.StatusServiceUnavailable, "upstream_error", "egress_blocked"
	}
	return http.StatusInternalServerError, "upstream_error", "upstream_failure"
}

// ClientErrorMessage 给客户端看的稳定文案：不复述上游原文（内含 r 事件号与站点内部
// 字段），细节留在日志里。
func ClientErrorMessage(t UpstreamErrorType) string {
	switch t {
	case ErrTypeInputLimit:
		return "request input exceeds the upstream per-request limit"
	case ErrTypeConversationLimit:
		return "conversation exceeds the upstream limit"
	case ErrTypeModelRestricted:
		return "model not available for anonymous callers"
	case ErrTypeModelUnavailable:
		return "model unavailable upstream"
	case ErrTypeRateLimit:
		return "upstream rate limited this client fingerprint"
	case ErrTypeChallenge:
		return "upstream refused to issue a challenge"
	case ErrTypeBNLimit:
		return "upstream rejected this egress IP"
	}
	return "upstream request failed"
}
