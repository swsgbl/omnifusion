package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

type ctxKey int

const requestIDKey ctxKey = 0

// GetRequestID 从 context 取请求 ID。
func GetRequestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

// RequestLogger 注入 request_id 并记录请求开始/结束日志。
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-Id")
			if id == "" {
				id = newRequestID()
			}
			ctx := context.WithValue(r.Context(), requestIDKey, id)
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

			logger.InfoContext(ctx, "request",
				"request_id", id, "method", r.Method, "path", r.URL.Path)
			next.ServeHTTP(sw, r.WithContext(ctx))
			logger.InfoContext(ctx, "request_done",
				"request_id", id, "status", sw.status,
				"duration_ms", float64(time.Since(start).Microseconds())/1000)
		})
	}
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand 失败极罕见；退化为纳秒时间戳保证可用性
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// statusWriter 捕获响应状态码用于日志。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush 透传底层 Flusher：包装 ResponseWriter 后若不转发，
// SSE 流式路径（server.handleChatStream）会因断言失败而拒绝服务。
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
