package openai_compat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
)

func sseResponse(t *testing.T, status int, body string) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

func newTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	a, err := New(Spec{ProviderName: "mock", BaseURL: "http://mock.local/v1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestParseStreamMultiEventWithToolCalls(t *testing.T) {
	a := newTestAdapter(t)
	sse := "" +
		": keep-alive\n" +
		"\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n" +
		"\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hel\"},\"finish_reason\":null}]}\n" +
		"\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]},\"finish_reason\":null}]}\n" +
		"\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"city\\\":\"}}]},\"finish_reason\":null}]}\n" +
		"\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":7,\"total_tokens\":10}}\n" +
		"\n" +
		"data: [DONE]\n" +
		"\n"

	stream, err := a.ParseStream(context.Background(), nil, sseResponse(t, 200, sse))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()

	ctx := context.Background()
	var contents []string
	var args, name string
	var finish string
	var usageSeen bool
	for i := 0; i < 5; i++ {
		chunk, err := stream.Next(ctx)
		if err != nil {
			t.Fatalf("Next[%d]: %v", i, err)
		}
		if chunk.ProviderName != "mock" {
			t.Errorf("chunk %d ProviderName = %q", i, chunk.ProviderName)
		}
		if len(chunk.Choices) != 1 {
			t.Fatalf("chunk %d choices = %d", i, len(chunk.Choices))
		}
		delta := chunk.Choices[0].Delta
		if txt := delta.Content.TextOf(); txt != "" {
			contents = append(contents, txt)
		}
		for _, tc := range delta.ToolCalls {
			if tc.Function.Name != "" {
				name = tc.Function.Name
			}
			args += tc.Function.Arguments
		}
		if chunk.Choices[0].FinishReason != "" {
			finish = chunk.Choices[0].FinishReason
		}
		if chunk.Usage != nil {
			usageSeen = true
		}
	}
	if _, err := stream.Next(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("after [DONE] Next = %v, want io.EOF", err)
	}
	if _, err := stream.Next(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("repeat after EOF = %v, want io.EOF", err)
	}

	if got := strings.Join(contents, ""); got != "Hel" {
		t.Errorf("content = %q", got)
	}
	if name != "get_weather" || args != `{"city":` {
		t.Errorf("tool call delta lost: name=%q args=%q", name, args)
	}
	if finish != "tool_calls" {
		t.Errorf("finish_reason = %q", finish)
	}
	if !usageSeen {
		t.Error("usage chunk lost")
	}
}

func TestParseStreamMultiLineDataField(t *testing.T) {
	a := newTestAdapter(t)
	// SSE spec: consecutive data lines join with \n; JSON tolerates the newline.
	sse := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\"," +
		"\ndata: \"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n" +
		"\n" +
		"data: [DONE]\n\n"

	stream, err := a.ParseStream(context.Background(), nil, sseResponse(t, 200, sse))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()
	chunk, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if chunk.Choices[0].Delta.Content.TextOf() != "ok" {
		t.Errorf("multi-line data event lost: %+v", chunk)
	}
}

func TestParseStreamNon2xx(t *testing.T) {
	a := newTestAdapter(t)
	resp := sseResponse(t, 429, `{"error":{"message":"rate limited"}}`)
	_, err := a.ParseStream(context.Background(), nil, resp)
	ue, ok := provider.IsUpstream(err)
	if !ok {
		t.Fatalf("err = %v, want UpstreamError", err)
	}
	if ue.Status != 429 || !strings.Contains(string(ue.Body), "rate limited") {
		t.Errorf("unexpected upstream error: %+v", ue)
	}
}

func TestParseStreamEmptyBodyFails(t *testing.T) {
	a := newTestAdapter(t)
	stream, err := a.ParseStream(context.Background(), nil, sseResponse(t, 200, ""))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Next(context.Background()); err == nil {
		t.Fatal("empty body should fail (drives failover), got chunk")
	}
}

func TestParseStreamTruncatedWithoutDone(t *testing.T) {
	a := newTestAdapter(t)
	sse := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"},\"finish_reason\":null}]}\n\n"
	stream, err := a.ParseStream(context.Background(), nil, sseResponse(t, 200, sse))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Next(context.Background()); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	_, err = stream.Next(context.Background())
	se, ok := provider.AsStreamError(err)
	if !ok {
		t.Fatalf("err = %v, want StreamError", err)
	}
	if se.Reason != provider.StreamEndedWithoutDone {
		t.Errorf("reason = %q, want %q", se.Reason, provider.StreamEndedWithoutDone)
	}
}

func TestParseStreamInlineErrorEvent(t *testing.T) {
	a := newTestAdapter(t)
	sse := "data: {\"error\":{\"message\":\"upstream overloaded\",\"code\":503}}\n\n"
	stream, err := a.ParseStream(context.Background(), nil, sseResponse(t, 200, sse))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()
	_, err = stream.Next(context.Background())
	ue, ok := provider.IsUpstream(err)
	if !ok {
		t.Fatalf("err = %v, want UpstreamError", err)
	}
	if !strings.Contains(string(ue.Body), "upstream overloaded") {
		t.Errorf("error body = %s", ue.Body)
	}
}

func TestParseStreamModelFallbackFromCall(t *testing.T) {
	a := newTestAdapter(t)
	sse := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	call := &provider.ProviderCall{Model: "alias-target"}
	stream, err := a.ParseStream(context.Background(), call, sseResponse(t, 200, sse))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()
	chunk, err := stream.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if chunk.Model != "alias-target" {
		t.Errorf("model fallback = %q", chunk.Model)
	}
}

func TestParseStreamMalformedJSON(t *testing.T) {
	a := newTestAdapter(t)
	stream, err := a.ParseStream(context.Background(), nil, sseResponse(t, 200, "data: {not json}\n\n"))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Next(context.Background()); err == nil {
		t.Fatal("malformed event must error")
	}
}

func TestParseStreamCloseIsIdempotent(t *testing.T) {
	a := newTestAdapter(t)
	stream, err := a.ParseStream(context.Background(), nil, sseResponse(t, 200, "data: [DONE]\n\n"))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
