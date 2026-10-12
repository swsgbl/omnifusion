package openai_compat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// skipSpace 去掉头部空白（体形嗅探只看第一个非空白字节）。
func skipSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\r' || b[0] == '\n') {
		b = b[1:]
	}
	return b
}

// sniffFirstByte 渐进式窥探第一个非空白字节：已窥探内容全为空白时
// 才多要 1 字节（此时等待下一网络分片是判定本身的需要）；空体/读错
// 返回 ok=false，走 SSE 路径（立即 EOF，语义与旧版一致）。窥探过的
// 字节仍在 bufio 缓冲里，后续 Scanner 原样读到。
func sniffFirstByte(br *bufio.Reader) (byte, bool) {
	for want := 1; want <= 64; want++ {
		head, err := br.Peek(want)
		if err != nil {
			if len(head) == 0 {
				return 0, false
			}
			// EOF 带部分内容：就着已有字节判定。
			trimmed := skipSpace(head)
			if len(trimmed) == 0 {
				return 0, false
			}
			return trimmed[0], true
		}
		trimmed := skipSpace(head)
		if len(trimmed) > 0 {
			return trimmed[0], true
		}
		// want 字节全是空白：多要 1 字节再判。
	}
	return 0, false
}

// maxSSELine bounds one SSE line (a chunk with a large delta stays far
// below this; guards against runaway upstream lines).
const maxSSELine = 1 << 20 // 1 MiB

// StreamClient implements provider.StreamParser.
func (a *Adapter) StreamClient() *http.Client { return a.streamClient }

// ParseStream implements provider.StreamParser: non-2xx becomes the
// same typed UpstreamError as Parse; a 2xx body is wrapped into a lazy
// SSE event stream that owns resp.Body.
//
// 体形嗅探：流请求的响应由正文形状决定读法，Content-Type 只是提示——
// 部分网关负载下会以 application/json 回应 SSE 帧流，也有整份 JSON
// 文档回应流请求的（头 4KB 先读再回放，不吞首帧）。JSON 文档折叠为
// 单帧 chunk 序列（完整回答当一次事件下发），不再误判断流。
func (a *Adapter) ParseStream(ctx context.Context, call *provider.ProviderCall, resp *http.Response) (provider.ChunkStream, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("openai_compat: %q nil upstream response", a.spec.ProviderName)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		if err != nil {
			return nil, fmt.Errorf("openai_compat: %q read error body: %w", a.spec.ProviderName, err)
		}
		return nil, &provider.UpstreamError{
			Provider: a.spec.ProviderName,
			Status:   resp.StatusCode,
			Body:     bytes.TrimSpace(body),
		}
	}
	model := ""
	if call != nil {
		model = call.Model
	}
	br := bufio.NewReaderSize(resp.Body, 4096)
	// 体形判定只需要"前导空白之后的第一个字节"：渐进式窥探，每次只
	// 多要 1 字节——固定窗口的 Peek 会阻塞等满（实测上游首帧 46 字节
	// 挂住时，Peek(64) 把首 chunk 缓冲卡到取消才解除，帧尽失）。
	if first, ok := sniffFirstByte(br); ok {
		switch first {
		case '{', '[':
			return a.jsonFoldStream(model, resp, br)
		}
	}
	return &sseStream{
		providerName: a.spec.ProviderName,
		callModel:    model,
		body:         resp.Body,
		sc:           newScanner(br),
	}, nil
}

// jsonFoldStream 把"流请求收到的整份 JSON 文档"折叠为单帧 chunk 流：
// 每个 choice 一帧（Delta 携带完整 message，含 reasoning），usage 附在
// 末帧；耗尽即 EOF，下游 [DONE] 由服务层统一补写。
func (a *Adapter) jsonFoldStream(model string, resp *http.Response, br *bufio.Reader) (provider.ChunkStream, error) {
	parsed, err := schema.NewResponseFromReader(io.LimitReader(br, maxErrorBody))
	if err != nil {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("openai_compat: %q decode non-stream body: %w", a.spec.ProviderName, err)
	}
	if len(parsed.Choices) == 0 {
		_ = resp.Body.Close()
		return nil, &provider.UpstreamError{
			Provider: a.spec.ProviderName,
			Status:   resp.StatusCode,
			Body:     []byte("upstream returned 2xx with no choices"),
		}
	}
	if parsed.Model == "" {
		parsed.Model = model
	}
	if parsed.ID == "" {
		parsed.ID = "fold-" + a.spec.ProviderName
	}
	if parsed.Created == 0 {
		parsed.Created = time.Now().Unix()
	}
	chunks := make([]*schema.Chunk, 0, len(parsed.Choices))
	for i, ch := range parsed.Choices {
		c := schema.NewChunk(parsed.ID, parsed.Model, parsed.Created)
		c.Choices = []schema.ChunkChoice{{
			Index:        ch.Index,
			Delta:        ch.Message,
			FinishReason: ch.FinishReason,
		}}
		if i == len(parsed.Choices)-1 {
			c.Usage = parsed.Usage
		}
		chunks = append(chunks, c)
	}
	return &jsonOnceStream{chunks: chunks, closeFn: func() error { return resp.Body.Close() }}, nil
}

// jsonOnceStream 是一次性 chunk 队列流：Next 逐帧出队，空后 EOF。
type jsonOnceStream struct {
	chunks  []*schema.Chunk
	closeFn func() error
	closed  bool
}

// Next implements provider.ChunkStream.
func (j *jsonOnceStream) Next(ctx context.Context) (*schema.Chunk, error) {
	if len(j.chunks) == 0 {
		return nil, io.EOF
	}
	c := j.chunks[0]
	j.chunks = j.chunks[1:]
	return c, nil
}

// Close implements provider.ChunkStream.
func (j *jsonOnceStream) Close() error {
	if j.closed {
		return nil
	}
	j.closed = true
	return j.closeFn()
}

func newScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxSSELine)
	return sc
}

// sseStream 按 SSE 语义解析 OpenAI 形态事件流：只消费 data 字段
// （event/id/retry 与 ":" 注释行忽略），连续 data 行拼为一条事件，
// 空行定界；"[DONE]" 正常收尾，内嵌 {"error":...} 事件归一为错误
// （OpenRouter 免费档限流会以此形态下发）。
type sseStream struct {
	providerName string
	callModel    string
	body         io.ReadCloser
	sc           *bufio.Scanner
	data         []string // pending data lines of the current event
	done         bool
	closed       bool
}

// Next implements provider.ChunkStream.
func (s *sseStream) Next(ctx context.Context) (*schema.Chunk, error) {
	if s.done {
		return nil, io.EOF
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !s.sc.Scan() {
			if err := s.sc.Err(); err != nil {
				return nil, &provider.StreamError{
					Provider: s.providerName,
					Reason:   provider.StreamRead,
					Err:      err,
				}
			}
			// Upstream closed without [DONE]: a complete response always
			// ends with it, so treat this as broken (drives pre-first-
			// chunk failover on empty/truncated bodies).
			return nil, &provider.StreamError{
				Provider: s.providerName,
				Reason:   provider.StreamEndedWithoutDone,
				Err:      io.ErrUnexpectedEOF,
			}
		}
		line := s.sc.Text()
		switch {
		case line == "":
			if len(s.data) == 0 {
				continue // bare keep-alive boundary
			}
			payload := strings.TrimSpace(strings.Join(s.data, "\n"))
			s.data = s.data[:0]
			switch payload {
			case "":
				continue // empty data event carries nothing
			case "[DONE]":
				s.done = true
				return nil, io.EOF
			}
			return s.decode(payload)
		case strings.HasPrefix(line, ":"):
			// comment / keep-alive line
		case strings.HasPrefix(line, "data:"):
			s.data = append(s.data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		default:
			// event:/id:/retry: carry no payload in OpenAI shape; ignore
		}
	}
}

// decode 解析一条非空事件载荷：错误事件返回 typed error，其余解码
// 为归一化 Chunk。
func (s *sseStream) decode(payload string) (*schema.Chunk, error) {
	if isStreamErrorEvent(payload) {
		return nil, &provider.UpstreamError{
			Provider: s.providerName,
			Status:   0, // mid-stream error event; no HTTP status applies
			Body:     []byte(truncatePayload(payload)),
		}
	}
	var chunk schema.Chunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return nil, &provider.StreamError{
			Provider: s.providerName,
			Reason:   provider.StreamDecode,
			Err:      err,
		}
	}
	chunk.ProviderName = s.providerName
	if chunk.Model == "" {
		chunk.Model = s.callModel
	}
	return &chunk, nil
}

// Close implements provider.ChunkStream.
func (s *sseStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.body.Close()
}

// isStreamErrorEvent 识别 {"error": ...} 形态的内嵌错误事件。
func isStreamErrorEvent(payload string) bool {
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &probe); err != nil {
		return false
	}
	return len(probe.Error) > 0 && string(probe.Error) != "null"
}

func truncatePayload(p string) string {
	const n = 512
	if len(p) <= n {
		return p
	}
	return p[:n] + "..."
}
