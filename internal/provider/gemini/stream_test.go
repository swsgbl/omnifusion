package gemini

import (
	"context"
	"io"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// geminiSSE 是 alt=sse 形态的事件流：每帧一个完整 GenerateContentResponse，
// 无 [DONE] 终止符，连接关闭即收尾。
const geminiSSE = "data: {\"responseId\":\"r1\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hel\"}]},\"index\":0}]}\n\n" +
	"data: {\"responseId\":\"r1\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"lo\"}]},\"finishReason\":\"STOP\",\"index\":0}]}\n\n" +
	"data: {\"responseId\":\"r1\",\"candidates\":[{\"content\":{\"role\":\"model\"},\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2,\"totalTokenCount\":7},\"modelVersion\":\"gemini-x\"}\n\n"

func TestParseStreamFramesToEOF(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stream, err := a.ParseStream(context.Background(),
		&provider.ProviderCall{Model: "gemini-x"}, httpResponse(t, 200, geminiSSE))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()

	ctx := context.Background()
	var texts []string
	var finish string
	var usage *struct{ p, c int }
	for {
		chunk, err := stream.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if chunk.ProviderName != "mock" {
			t.Errorf("ProviderName = %q", chunk.ProviderName)
		}
		if chunk.ID != "r1" || chunk.Model != "gemini-x" {
			t.Errorf("id/model = %q/%q", chunk.ID, chunk.Model)
		}
		for _, ch := range chunk.Choices {
			if t := ch.Delta.Content.TextOf(); t != "" {
				texts = append(texts, t)
			}
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
		}
		if chunk.Usage != nil {
			usage = &struct{ p, c int }{chunk.Usage.PromptTokens, chunk.Usage.CompletionTokens}
		}
	}
	if join := texts[0] + texts[1]; len(texts) != 2 || join != "Hello" {
		t.Errorf("texts = %v, want [Hel lo]", texts)
	}
	if finish != "stop" {
		t.Errorf("finish = %q, want stop", finish)
	}
	if usage == nil || usage.p != 5 || usage.c != 2 {
		t.Errorf("usage = %+v, want 5/2", usage)
	}
}

// TestParseStreamEmptyBodyIsCleanEOF 钉住与 OpenAI 形的语义差异：
// Gemini 流无 [DONE]，干净读完（哪怕零帧）就是正常收尾，不是断流。
func TestParseStreamEmptyBodyIsCleanEOF(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	stream, err := a.ParseStream(context.Background(), nil, httpResponse(t, 200, ""))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()
	if _, err := stream.Next(context.Background()); err != io.EOF {
		t.Errorf("err = %v, want io.EOF (no [DONE] sentinel exists)", err)
	}
}

func TestParseStreamNon2xx(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = a.ParseStream(context.Background(), nil, httpResponse(t, 403,
		`{"error":{"code":403,"message":"denied","status":"PERMISSION_DENIED"}}`))
	ue, ok := provider.IsUpstream(err)
	if !ok {
		t.Fatalf("err = %v, want UpstreamError", err)
	}
	if ue.Status != 403 {
		t.Errorf("Status = %d, want 403", ue.Status)
	}
}

// TestParseStreamFunctionCall 覆盖 M3.3 工具流：帧内 functionCall parts
// 归一为 delta.tool_calls（args 为完整对象字符串、index 递增），
// finishReason STOP 有调用时强制 finish=tool_calls。
func TestParseStreamFunctionCall(t *testing.T) {
	a, err := New(baseSpec())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sse := "data: {\"responseId\":\"r9\",\"modelVersion\":\"gemini-x\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Checking.\"},{\"functionCall\":{\"name\":\"get_weather\",\"args\":{\"city\":\"Paris\"}}}]},\"index\":0}]}\n\n" +
		"data: {\"responseId\":\"r9\",\"candidates\":[{\"content\":{\"role\":\"model\"},\"finishReason\":\"STOP\",\"index\":0}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":3}}\n\n"
	stream, err := a.ParseStream(context.Background(),
		&provider.ProviderCall{Model: "gemini-x"}, httpResponse(t, 200, sse))
	if err != nil {
		t.Fatalf("ParseStream: %v", err)
	}
	defer stream.Close()

	ctx := context.Background()
	var text, args, finish string
	var id, name string
	var idx = -1
	for {
		chunk, err := stream.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		for _, ch := range chunk.Choices {
			text += ch.Delta.Content.TextOf()
			for _, tc := range ch.Delta.ToolCalls {
				id, name, args = tc.ID, tc.Function.Name, tc.Function.Arguments
				if tc.Index != nil {
					idx = *tc.Index
				}
			}
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
		}
	}
	if text != "Checking." {
		t.Errorf("text = %q, want Checking.", text)
	}
	if id != "get_weather" || name != "get_weather" {
		t.Errorf("id/name = %q/%q (id 缺省应回落 name)", id, name)
	}
	if args != "{\"city\":\"Paris\"}" {
		t.Errorf("args = %q, want {\"city\":\"Paris\"}", args)
	}
	if idx != 0 {
		t.Errorf("index = %d, want 0", idx)
	}
	if finish != "tool_calls" {
		t.Errorf("finish = %q, want tool_calls (STOP + functionCall)", finish)
	}
}
