package util

import (
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	officialtypes "aurora/internal/typings/official"

	"github.com/pkoukk/tiktoken-go"
)

func RandomLanguage() string {
	// 初始化随机数生成器
	rand.Seed(time.Now().UnixNano())
	// 语言列表
	languages := []string{"af", "am", "ar-sa", "as", "az-Latn", "be", "bg", "bn-BD", "bn-IN", "bs", "ca", "ca-ES-valencia", "cs", "cy", "da", "de", "de-de", "el", "en-GB", "en-US", "es", "es-ES", "es-US", "es-MX", "et", "eu", "fa", "fi", "fil-Latn", "fr", "fr-FR", "fr-CA", "ga", "gd-Latn", "gl", "gu", "ha-Latn", "he", "hi", "hr", "hu", "hy", "id", "ig-Latn", "is", "it", "it-it", "ja", "ka", "kk", "km", "kn", "ko", "kok", "ku-Arab", "ky-Cyrl", "lb", "lt", "lv", "mi-Latn", "mk", "ml", "mn-Cyrl", "mr", "ms", "mt", "nb", "ne", "nl", "nl-BE", "nn", "nso", "or", "pa", "pa-Arab", "pl", "prs-Arab", "pt-BR", "pt-PT", "qut-Latn", "quz", "ro", "ru", "rw", "sd-Arab", "si", "sk", "sl", "sq", "sr-Cyrl-BA", "sr-Cyrl-RS", "sr-Latn-RS", "sv", "sw", "ta", "te", "tg-Cyrl", "th", "ti", "tk-Latn", "tn", "tr", "tt-Cyrl", "ug-Arab", "uk", "ur", "uz-Latn", "vi", "wo", "xh", "yo-Latn", "zh-Hans", "zh-Hant", "zu"}
	// 随机选择一个语言
	randomIndex := rand.Intn(len(languages))
	return languages[randomIndex]
}

// RandomHexadecimalString 返回 16 个十六进制字符的随机 ID —— 它是 tool_call id / message id
// 的来源（duckgo.OfficialToolCalls、anthropic 的 toolu_/msg_）。
//
// 用 crypto/rand，不用 math/rand：原实现每次调用都 rand.Seed(time.Now().UnixNano())，而 Seed
// 改的是**全局共享**随机源 —— 并发下「A 播种 → B 播种 → A 抽数」会让 A 拿到 B 的序列，
// 同一纳秒内的两次调用必然撞号。（本机单线程 5 万次 / 并发 8×2 万次实测 0 碰撞，但那是运气
// 不是保证；两个 tool_call 撞 id 会让客户端把两次调用认成同一个。）
func RandomHexadecimalString() string {
	buf := make([]byte, 8)
	if _, err := crand.Read(buf); err != nil {
		return fmt.Sprintf("%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
func CountToken(input string) int {
	encoding := "gpt-4o-mini"
	tkm, err := tiktoken.EncodingForModel(encoding)
	if err != nil {
		slog.Warn("tiktoken.EncodingForModel error", "err", err)
		return 0
	}
	token := tkm.Encode(input, nil, nil)
	return len(token)
}

// CountMessagesTokens counts input tokens for an array of API messages.
func CountMessagesTokens(messages []officialtypes.ApiMessage) int {
	var sb strings.Builder
	for _, msg := range messages {
		sb.WriteString(MessageText(msg.Content))
		sb.WriteString("\n")
	}
	return CountToken(sb.String())
}

// MessageText extracts plain text from a message content (string or multipart array).
func MessageText(content interface{}) string {
	if content == nil {
		return ""
	}
	if s, ok := content.(string); ok {
		return s
	}
	if parts, ok := content.([]interface{}); ok {
		var sb strings.Builder
		for _, p := range parts {
			pm, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			typ, _ := pm["type"].(string)
			switch typ {
			case "text", "":
				if t, ok := pm["text"].(string); ok {
					sb.WriteString(t)
				}
			}
		}
		return sb.String()
	}
	return ""
}

// ---------- Prompt cache simulation ----------

// cacheTTL how long a cached prompt prefix stays "warm".
const cacheTTL = 5 * time.Minute

type cacheEntry struct {
	tokens    int
	createdAt time.Time // when the entry was created (for Age header)
	expiresAt time.Time
}

var (
	cacheMu    sync.Mutex
	cacheStore = make(map[string]cacheEntry)
)

// RecordCache returns (cacheCreation, cacheRead) token counts for a prompt.
// First time a prompt hash is seen → cacheCreation = tokens (cache populated).
// Same hash seen again within TTL → cacheRead = tokens (cache hit).
func RecordCache(promptHash string, tokens int) (cacheCreation int, cacheRead int) {
	if promptHash == "" || tokens <= 0 {
		return 0, 0
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()

	// opportunistic cleanup
	now := time.Now()
	for k, v := range cacheStore {
		if now.After(v.expiresAt) {
			delete(cacheStore, k)
		}
	}

	if e, ok := cacheStore[promptHash]; ok && now.Before(e.expiresAt) {
		return 0, e.tokens
	}
	cacheStore[promptHash] = cacheEntry{tokens: tokens, createdAt: now, expiresAt: now.Add(cacheTTL)}
	return tokens, 0
}

// CacheStatus reports whether a prompt hash is currently cached (HIT vs MISS)
// and how many seconds ago the entry was created (for the Age header).
// Returns (hit bool, ageSeconds int). ageSeconds is 0 when not hit.
func CacheStatus(promptHash string) (hit bool, ageSeconds int) {
	if promptHash == "" {
		return false, 0
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()

	now := time.Now()
	if e, ok := cacheStore[promptHash]; ok && now.Before(e.expiresAt) {
		return true, int(now.Sub(e.createdAt).Seconds())
	}
	return false, 0
}

// CacheHeaders returns the standard HTTP cache headers so external
// cache-statistics/monitoring software (Varnish, Squid, CDNs) can read them.
//   - X-Cache: "HIT" or "MISS"
//   - Age: <seconds> (omitted on MISS)
func CacheHeaders(promptHash string) (xCache string, ageSeconds int) {
	hit, age := CacheStatus(promptHash)
	if hit {
		return "HIT", age
	}
	return "MISS", 0
}

// HashPrompt builds a stable hash for prompt cache keying.
func HashPrompt(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
