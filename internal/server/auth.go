package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// bearerPrefix 是鉴权头取值前缀（OpenAI SDK 兼容）。
const bearerPrefix = "Bearer "

// requireGatewayKey 在数据面强制网关统一 API Key（docs/06 R5 对策 2：
// M1 即带，非可选）。未携带或取值不匹配一律 401；网关 token 未装配
// 时 fail-closed（全部拒绝），绝不裸奔。
func (s *Server) requireGatewayKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.gatewayKeyOK(r) {
			writeAPIError(w, http.StatusUnauthorized,
				"missing or invalid gateway API key; send 'Authorization: Bearer <key>' (see: ofd gateway-key)",
				"authentication_error", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// gatewayKeyOK 做常数时间比较，避免计时侧信道。
func (s *Server) gatewayKeyOK(r *http.Request) bool {
	if s.gatewayToken == "" {
		return false
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, bearerPrefix) {
		return false
	}
	return tokenEqual(strings.TrimPrefix(h, bearerPrefix), s.gatewayToken)
}

// requireAnthropicKey 是数据面鉴权的 Anthropic 形态（M3.1）：Claude
// Code 用 ANTHROPIC_API_KEY 时发 x-api-key，用 ANTHROPIC_AUTH_TOKEN 时
// 发 Authorization Bearer——两种都收，否则客户端无法零改动直连。
func (s *Server) requireAnthropicKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.anthropicKeyOK(r) {
			writeAnthropicError(w, http.StatusUnauthorized, "authentication_error",
				"missing or invalid gateway API key; send 'x-api-key: <key>' (see: ofd gateway-key)")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// anthropicKeyOK 取 x-api-key 或 Bearer 之一做常数时间比较。
func (s *Server) anthropicKeyOK(r *http.Request) bool {
	if s.gatewayToken == "" {
		return false
	}
	if tok := r.Header.Get("x-api-key"); tok != "" {
		return tokenEqual(tok, s.gatewayToken)
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, bearerPrefix) {
		return false
	}
	return tokenEqual(strings.TrimPrefix(h, bearerPrefix), s.gatewayToken)
}

// requireGeminiKey 是数据面鉴权的 Gemini 形态（M3.2）：Google SDK 发
// x-goog-api-key 头，分享链接/curl 常用 ?key= 查询参数，Bearer 也收。
func (s *Server) requireGeminiKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.geminiKeyOK(r) {
			writeGeminiError(w, http.StatusUnauthorized, "UNAUTHENTICATED",
				"missing or invalid gateway API key; send 'x-goog-api-key: <key>' (see: ofd gateway-key)")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// geminiKeyOK 取 x-goog-api-key / ?key= / Bearer 之一做常数时间比较。
func (s *Server) geminiKeyOK(r *http.Request) bool {
	if s.gatewayToken == "" {
		return false
	}
	if tok := r.Header.Get("x-goog-api-key"); tok != "" {
		return tokenEqual(tok, s.gatewayToken)
	}
	if tok := r.URL.Query().Get("key"); tok != "" {
		return tokenEqual(tok, s.gatewayToken)
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, bearerPrefix) {
		return false
	}
	return tokenEqual(strings.TrimPrefix(h, bearerPrefix), s.gatewayToken)
}

// tokenEqual 常数时间比较两个令牌。
func tokenEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
