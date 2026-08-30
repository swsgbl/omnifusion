package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// StreamErrorReason 细分流中断的原因，供错误分类选择策略。
type StreamErrorReason string

const (
	// StreamRead 表示底层连接读取失败（网络中断、连接重置等）。
	StreamRead StreamErrorReason = "read"
	// StreamEndedWithoutDone 表示上游未发 [DONE] 即关闭（空体/截断流）。
	StreamEndedWithoutDone StreamErrorReason = "ended_without_done"
	// StreamDecode 表示事件载荷不是合法 JSON。
	StreamDecode StreamErrorReason = "decode"
)

// StreamError 是流式传输中断的归一化错误类型（冻结接口之外的
// additive 扩展）。路由层用它区分"流坏了"（本类型）与"上游拒绝"
// （UpstreamError）；流内嵌 {"error":...} 事件仍按 契约归一为
// Status==0 的 UpstreamError，由分类器映射为 stream_broken。
type StreamError struct {
	Provider string
	Reason   StreamErrorReason
	Err      error
}

// Error implements error.
func (e *StreamError) Error() string {
	return fmt.Sprintf("provider %q stream %s: %v", e.Provider, e.Reason, e.Err)
}

// Unwrap 暴露底层错误，保持 errors.Is/As 链可用。
func (e *StreamError) Unwrap() error { return e.Err }

// AsStreamError 报告 err 是否为 StreamError，并给出实例。
func AsStreamError(err error) (*StreamError, bool) {
	var se *StreamError
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}

// ChunkStream 是归一化流式事件的拉取式迭代器（SSE 转发面，见
// 的 buffer-first-chunk 段落）。
//
// Next 在正常收尾（上游发出 [DONE]）时返回 io.EOF；其余 error 均视为
// 流中断：首 chunk 之前由路由层换家重试，首 chunk 之后只记日志不断流
// （FreeRide "cannot un-ship bytes" 语义）。
type ChunkStream interface {
	Next(ctx context.Context) (*schema.Chunk, error)
	Close() error
}

// StreamParser 是冻结 Provider 接口的可选流式扩展：
// 能流式的适配器实现它，不能的仍按非流式服务，路由层在 stream=true
// 时跳过它们。ParseStream 与 Parse 对称：非 2xx 归一为 UpstreamError，
// 2xx 体交给事件流惰性解析。
type StreamParser interface {
	// StreamClient 返回 SSE 调用专用客户端：不得携带全局 Timeout
	// （流的生命周期远超请求级超时），头部等待与整体寿命分别由
	// Transport 的 ResponseHeaderTimeout 与调用方 ctx 约束。
	StreamClient() *http.Client
	// ParseStream 把上游流式响应转成归一化事件流。成功时事件流
	// 持有 resp.Body 的所有权，调用方通过 Close 释放。
	ParseStream(ctx context.Context, call *ProviderCall, resp *http.Response) (ChunkStream, error)
}

// AsStreamParser 报告 provider 是否支持流式，并给出实现。
func AsStreamParser(p Provider) (StreamParser, bool) {
	sp, ok := p.(StreamParser)
	return sp, ok
}
