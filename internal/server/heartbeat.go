package server

import (
	"context"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// sseKeepaliveInterval 是下游心跳间隔：思考型模型在长推理期间上游
// 静默数分钟，30~60s 空闲超时的客户端会掐断连接——期间周期性下发
// 无害帧维持活性（OpenAI 线路用 SSE 注释，Anthropic 线路用官方
// ping 事件，客户端各自忽略/处理）。var 供测试缩短。
var sseKeepaliveInterval = 20 * time.Second

// 心跳帧常量：注释帧对 SSE 解析器不可见；ping 事件是 Anthropic 线路
// 的官方保活形态。
const (
	openaiKeepaliveFrame   = ": omnifusion-keepalive\n\n"
	anthropicPingFrame     = "event: ping\ndata: {\"type\":\"ping\"}\n\n"
)

// chunkResult 是一次 Next 的结果对。
type chunkResult struct {
	chunk *schema.Chunk
	err   error
}

// chunkFeeder 把同步 ChunkStream 适配为可 select 的结果通道：Next 在
// 独立 goroutine 里阻塞，读侧在等帧间隙能同时响应心跳 ticker。
// 通道缓冲 1 + 出错即退：读侧返回后 stream.Close() 解除阻塞的 Next，
// feeder 送出终态（缓冲内有位）随即退出，不悬挂 goroutine。
type chunkFeeder struct {
	ch chan chunkResult
}

func feedChunks(ctx context.Context, stream provider.ChunkStream) *chunkFeeder {
	f := &chunkFeeder{ch: make(chan chunkResult, 1)}
	go func() {
		for {
			c, err := stream.Next(ctx)
			f.ch <- chunkResult{chunk: c, err: err}
			if err != nil {
				return
			}
		}
	}()
	return f
}
