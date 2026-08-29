// stream.go 是 SSE 流式迭代器：ChatStream 返回 *Stream，Next 逐分片
// 推进，遇到 data: [DONE] 或 io.EOF 结束。连接错误以 StatusError /
// 包装 error 从 Next 返回；Close 幂等。
package omnifusion

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Stream 是一次流式补全的迭代器（bufio.ScanLines 天然兼容 \r\n）。
type Stream struct {
	resp   *http.Response
	sc     *bufio.Scanner
	chunk  *Chunk // 最近一次 Next 成功的分片
	text   []byte // 已收分片的 delta 文本累积
	done   bool   // 收到 [DONE] 或 EOF
	err    error  // 终止性错误
	closed bool
}

// ChatStream 发起流式补全（req.Stream 恒被置 true）。返回的 Stream
// 必须调用 Close；ctx 取消会中断底层连接并以错误结束迭代。
func (c *Client) ChatStream(ctx context.Context, req *ChatRequest) (*Stream, error) {
	q := req.clone()
	q.Stream = true
	body, err := json.Marshal(q)
	if err != nil {
		return nil, fmt.Errorf("omnifusion: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.key)
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("omnifusion: request: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		buf := make([]byte, 2048)
		n, _ := resp.Body.Read(buf)
		return nil, &StatusError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       string(buf[:n]),
		}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &Stream{resp: resp, sc: sc}, nil
}

// Next 推进到下一个分片；false = 流结束（此后 Err() 区分干净结束与
// 错误）。用法：for s.Next() { c := s.Chunk() } ; if err := s.Err(); err != nil {...}
func (s *Stream) Next() bool {
	if s.done || s.err != nil {
		return false
	}
	for s.sc.Scan() {
		line := s.sc.Bytes()
		const prefix = "data:"
		if len(line) <= len(prefix) || string(line[:len(prefix)]) != prefix {
			continue // 注释行/空行/事件字段，只认 data:
		}
		payload := trimSpaceBytes(line[len(prefix):])
		if string(payload) == "[DONE]" {
			s.done = true
			return false
		}
		var c Chunk
		if err := json.Unmarshal(payload, &c); err != nil {
			s.err = fmt.Errorf("omnifusion: decode chunk: %w", err)
			return false
		}
		s.chunk = &c
		if len(c.Choices) > 0 {
			s.text = append(s.text, c.Choices[0].Delta.Content...)
		}
		return true
	}
	if err := s.sc.Err(); err != nil {
		s.err = fmt.Errorf("omnifusion: read stream: %w", err)
	} else {
		s.done = true // 服务端未发 [DONE] 即关流：视为结束不报错
	}
	return false
}

// Chunk 返回最近一次 Next 成功的分片。
func (s *Stream) Chunk() *Chunk { return s.chunk }

// Err 返回终止性错误（干净结束返回 nil）。
func (s *Stream) Err() error { return s.err }

// Text 返回全部已收分片的文本累积（迭代中/完成后均可调用）。
func (s *Stream) Text() string { return string(s.text) }

// Close 关闭底层连接（幂等）。
func (s *Stream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.resp.Body.Close()
}

// trimSpaceBytes 去掉 SSE data: 后的前导空白。
func trimSpaceBytes(b []byte) []byte {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	return b[i:]
}
