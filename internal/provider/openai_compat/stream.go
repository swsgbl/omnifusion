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

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// maxSSELine bounds one SSE line (a chunk with a large delta stays far
// below this; guards against runaway upstream lines).
const maxSSELine = 1 << 20 // 1 MiB

// StreamClient implements provider.StreamParser.
func (a *Adapter) StreamClient() *http.Client { return a.streamClient }

// ParseStream implements provider.StreamParser: non-2xx becomes the
// same typed UpstreamError as Parse; a 2xx body is wrapped into a lazy
// SSE event stream that owns resp.Body.
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
	return &sseStream{
		providerName: a.spec.ProviderName,
		callModel:    model,
		body:         resp.Body,
		sc:           newScanner(resp.Body),
	}, nil
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
