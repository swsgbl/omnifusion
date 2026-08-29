package compression

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/core/schema"
)

// sidecarLongText 构造超过 sidecar 单文本阈值的样例。
func sidecarLongText() string {
	s := semanticSamples()[0]
	return s.text + " " + s.text + " " + s.text
}

// sidecarMock 起一个对位截断的 mock sidecar（保留每段前 60 字符）。
func sidecarMock(t *testing.T) (*httptest.Server, *sidecarRequest) {
	t.Helper()
	var seen sidecarRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		out := make([]string, len(seen.Texts))
		for i, text := range seen.Texts {
			r := []rune(text)
			if len(r) > 60 {
				r = r[:60]
			}
			out[i] = string(r) + "…"
		}
		_ = json.NewEncoder(w).Encode(sidecarResponse{Texts: out})
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// restoreSemantic 还原包级语义配置（全局状态，测试间不渗漏）。
func restoreSemantic(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { ConfigureSemantic(0, "") })
}

func TestSidecarStageApplies(t *testing.T) {
	restoreSemantic(t)
	srv, seen := sidecarMock(t)
	ConfigureSemantic(0.5, srv.URL)
	in := semanticSession(sidecarLongText())
	st := NewSidecarStage()
	out, stats, err := st.Apply(in)
	if err != nil {
		t.Fatalf("Apply error: %v", err)
	}
	if !stats.Applied || stats.Saved <= 0 {
		t.Fatalf("stats = %+v", stats)
	}
	if len(singleTextOf(out[1])) >= len(sidecarLongText()) {
		t.Fatal("expected shortened text")
	}
	if !strings.HasSuffix(singleTextOf(out[1]), "…") {
		t.Fatal("expected sidecar output to be adopted verbatim")
	}
	if singleTextOf(out[0]) != "You are helpful." || singleTextOf(out[3]) != "Continue." {
		t.Fatal("system and guarded tail must stay untouched")
	}
	if seen.Rate != 0.5 || len(seen.Texts) != 1 {
		t.Fatalf("sidecar request = %+v", seen)
	}
	if tot := st.Totals(); tot.Runs != 1 || tot.MessagesRewritten != 1 || tot.Rate() <= 0 {
		t.Fatalf("totals = %+v", tot)
	}
}

func TestSidecarPipelineFallsBackOnError(t *testing.T) {
	restoreSemantic(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	ConfigureSemantic(0.5, srv.URL)
	long := sidecarLongText()
	in := []schema.Message{
		textMsg(schema.RoleSystem, "You are helpful."),
		textMsg(schema.RoleUser, long),
		textMsg(schema.RoleAssistant, "Understood."),
		textMsg(schema.RoleUser, "Continue."),
	}
	p := NewPipeline(nil, NewSidecarStage())
	sc := NewStageContext("m", "s", in)
	out, stats := p.Run(sc, in)
	if len(stats) != 1 || stats[0].Err == nil {
		t.Fatalf("stage must report error, stats = %+v", stats)
	}
	if singleTextOf(out[1]) != long {
		t.Fatal("pipeline must fall back to original text")
	}
}

func TestSidecarTimeoutIsAnError(t *testing.T) {
	restoreSemantic(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(sidecarResponse{Texts: []string{"x"}})
	}))
	t.Cleanup(srv.Close)
	ConfigureSemantic(0.5, srv.URL)
	st := NewSidecarStage()
	st.client.Timeout = 50 * time.Millisecond // 测试收紧，不跑满 3s
	in := semanticSession(sidecarLongText())
	if _, _, err := st.Apply(in); err == nil {
		t.Fatal("timeout must surface as an error (原文直传语义)")
	}
}

func TestSidecarCountMismatchIsAnError(t *testing.T) {
	restoreSemantic(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(sidecarResponse{Texts: []string{"only one"}})
	}))
	t.Cleanup(srv.Close)
	ConfigureSemantic(0.5, srv.URL)
	// 两条合格长文本（guard 外），sidecar 只回一条：对位协议破坏即错误。
	in := []schema.Message{
		textMsg(schema.RoleSystem, "You are helpful."),
		textMsg(schema.RoleUser, sidecarLongText()),
		textMsg(schema.RoleUser, sidecarLongText()),
		textMsg(schema.RoleAssistant, "Understood."),
		textMsg(schema.RoleUser, "Continue."),
	}
	if _, _, err := NewSidecarStage().Apply(in); err == nil {
		t.Fatal("count mismatch must be an error")
	}
}

func TestSidecarShouldRunRequiresConfiguration(t *testing.T) {
	restoreSemantic(t)
	long := sidecarLongText()
	sc := NewStageContext("m", "s", semanticSession(long))
	st := NewSidecarStage()
	ConfigureSemantic(0, "") // 未配置：阈值以上也不跑
	if st.ShouldRun(sc) {
		t.Fatal("unconfigured sidecar must never run")
	}
	ConfigureSemantic(0.5, "http://127.0.0.1:1")
	if !st.ShouldRun(sc) {
		t.Fatal("configured sidecar above threshold must run")
	}
	tiny := NewStageContext("m", "s", semanticSession("short"))
	if st.ShouldRun(tiny) {
		t.Fatal("below threshold must not run")
	}
	// Apply 在未配置时直接报错（管线回退原文）。
	ConfigureSemantic(0.5, "")
	if _, _, err := st.Apply(semanticSession(long)); err == nil {
		t.Fatal("unconfigured Apply must error")
	}
}

// TestSemanticRateClamping 验证保留率钳位语义（0.1–0.9，0=默认）。
func TestSemanticRateClamping(t *testing.T) {
	restoreSemantic(t)
	cases := []struct{ in, want float64 }{
		{0, 0.5}, {0.05, 0.1}, {0.5, 0.5}, {0.99, 0.9}, {-1, 0.5},
	}
	for _, c := range cases {
		ConfigureSemantic(c.in, "")
		if got := configuredSemanticRate(); got != c.want {
			t.Fatalf("rate(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
