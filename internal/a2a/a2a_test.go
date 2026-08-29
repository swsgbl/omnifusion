package a2a

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

func TestToUnifiedTextAndModel(t *testing.T) {
	meta, _ := json.Marshal(map[string]string{"model": "mock-model"})
	m := &Message{
		Role:     RoleUser,
		Parts:    []Part{{Text: "hello "}, {Text: "world"}},
		Metadata: meta,
	}
	req, err := ToUnified(m, "@smart")
	if err != nil {
		t.Fatalf("ToUnified: %v", err)
	}
	if req.Model != "mock-model" {
		t.Fatalf("model = %q, want metadata override", req.Model)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v", req.Messages)
	}
	var sb strings.Builder
	for _, p := range req.Messages[0].Content.Parts {
		sb.WriteString(p.Text)
	}
	if sb.String() != "hello world" {
		t.Fatalf("text = %q", sb.String())
	}
}

func TestToUnifiedDefaultModelAndRoles(t *testing.T) {
	req, err := ToUnified(&Message{Role: RoleUser, Parts: []Part{TextPart("q")}}, "@smart")
	if err != nil || req.Model != "@smart" {
		t.Fatalf("default model fallback failed: %v %q", err, req.Model)
	}
	if _, err := ToUnified(&Message{Role: "ROLE_ADMIN", Parts: []Part{TextPart("x")}}, "m"); err == nil {
		t.Fatal("unknown role must be rejected")
	}
	if _, err := ToUnified(&Message{Role: RoleUser}, "m"); err != ErrNoContent {
		t.Fatalf("empty parts err = %v, want ErrNoContent", err)
	}
	if _, err := ToUnified(&Message{Role: RoleAgent, Parts: []Part{TextPart("  ")}}, "m"); err != ErrNoContent {
		t.Fatalf("blank text err = %v, want ErrNoContent", err)
	}
}

func TestToUnifiedDataAndFileParts(t *testing.T) {
	m := &Message{Role: RoleUser, Parts: []Part{
		{Data: json.RawMessage(`{"k":1}`)},
		{URL: "https://x/f.pdf", Filename: "f.pdf"},
	}}
	req, err := ToUnified(m, "m")
	if err != nil {
		t.Fatalf("ToUnified: %v", err)
	}
	var sb strings.Builder
	for _, p := range req.Messages[0].Content.Parts {
		sb.WriteString(p.Text)
	}
	got := sb.String()
	if !strings.Contains(got, `{"k":1}`) || !strings.Contains(got, "[file: https://x/f.pdf]") {
		t.Fatalf("data/file parts not stringified: %q", got)
	}
}

func TestFromResponseTextAndUsage(t *testing.T) {
	resp := &schema.Response{
		ID: "r1", Model: "m",
		Choices: []schema.ResponseChoice{{
			Message: schema.Message{Role: "assistant",
				Content: schema.Content{Parts: []schema.Part{{Type: schema.PartText, Text: "pong"}}}},
		}},
		Usage:        &schema.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6},
		ProviderName: "mock",
	}
	msg := FromResponse(resp)
	if msg.Role != RoleAgent || len(msg.Parts) != 1 || msg.Parts[0].Text != "pong" {
		t.Fatalf("msg = %+v", msg)
	}
	var meta struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
		Usage    struct {
			Total int `json:"totalTokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(msg.Metadata, &meta); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if meta.Model != "m" || meta.Provider != "mock" || meta.Usage.Total != 6 {
		t.Fatalf("metadata = %+v", meta)
	}
}

func TestBuildCardWireShape(t *testing.T) {
	card := BuildCard(CardOptions{BaseURL: "http://127.0.0.1:20130", Version: "v1.2.3", DefaultModel: "@smart", Streaming: true})
	b, err := json.Marshal(card)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ifaces := raw["supportedInterfaces"].([]any)
	iface := ifaces[0].(map[string]any)
	if iface["url"] != "http://127.0.0.1:20130/rpc" || iface["protocolBinding"] != "JSONRPC" || iface["protocolVersion"] != "1.0" {
		t.Fatalf("interface = %+v", iface)
	}
	schemes := raw["securitySchemes"].(map[string]any)
	gw := schemes["gatewayKey"].(map[string]any)["httpAuthSecurityScheme"].(map[string]any)
	if gw["scheme"] != "bearer" {
		t.Fatalf("security scheme = %+v", gw)
	}
	if reqs := raw["securityRequirements"].([]any); len(reqs) != 1 {
		t.Fatalf("securityRequirements = %v", reqs)
	} else if _, ok := reqs[0].(map[string]any)["schemes"].(map[string]any)["gatewayKey"]; !ok {
		t.Fatalf("securityRequirements[0] = %v", reqs[0])
	}
	if raw["name"] != "OmniFusion Gateway" || len(raw["skills"].([]any)) == 0 {
		t.Fatalf("card = %s", b)
	}
	caps := raw["capabilities"].(map[string]any)
	if caps["streaming"] != true {
		t.Fatalf("capabilities = %+v", caps)
	}
	if raw["defaultInputModes"].([]any)[0] != "text/plain" {
		t.Fatalf("input modes = %v", raw["defaultInputModes"])
	}
}

func TestPartWireShapeNoKind(t *testing.T) {
	b, _ := json.Marshal(TextPart("hi"))
	if string(b) != `{"text":"hi"}` {
		t.Fatalf("part wire form = %s (v1.0 must not carry kind)", b)
	}
	var ev StreamResponse
	ev.StatusUpdate = &TaskStatusUpdateEvent{TaskID: "t", Status: TaskStatus{State: StateWorking}}
	b, _ = json.Marshal(&ev)
	if !strings.Contains(string(b), `"statusUpdate"`) || !strings.Contains(string(b), `"TASK_STATE_WORKING"`) {
		t.Fatalf("event wire form = %s", b)
	}
}

func TestChunkText(t *testing.T) {
	c := &schema.Chunk{Choices: []schema.ChunkChoice{{
		Delta: schema.Message{Content: schema.Content{Parts: []schema.Part{
			{Type: schema.PartText, Text: "He"}, {Type: schema.PartText, Text: "llo"},
		}}},
	}}}
	if got := ChunkText(c); got != "Hello" {
		t.Fatalf("ChunkText = %q", got)
	}
}
