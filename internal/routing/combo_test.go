package routing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// comboFixture 起 a、b 两家 mock 上游（记录收到的 model 与请求次序）。
func comboFixture(t *testing.T) (*Router, *Recorder) {
	t.Helper()
	rec := &Recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.record(string(b))
		io.WriteString(w, okCompletion("m"))
	}))
	t.Cleanup(up.Close)
	r := &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "a", up.URL),
			newMockAdapter(t, "b", up.URL),
		},
	}
	return r, rec
}

type Recorder struct {
	mu     sync.Mutex
	bodies []string
}

func (rec *Recorder) record(body string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.bodies = append(rec.bodies, body)
}

func (rec *Recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.bodies)
}

func (rec *Recorder) last() string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.bodies) == 0 {
		return ""
	}
	return rec.bodies[len(rec.bodies)-1]
}

func comboAB() map[string]Combo {
	return map[string]Combo{
		"free": {
			Name: "free",
			Members: []ComboMember{
				{Provider: "b", Model: "model-b-free"},
				{Provider: "a", Model: "model-a-free"},
			},
		},
	}
}

func TestComboMemberOrderAndModelRewrite(t *testing.T) {
	r, rec := comboFixture(t)
	r.Combos = comboAB()

	req := testRequest()
	req.Model = "@combo:free"
	resp, attempts, err := r.Dispatch(context.Background(), req, WithCombo("free"))
	if err != nil || resp == nil {
		t.Fatalf("dispatch: %v", err)
	}
	// 成员声明序：b 先于 a（注册序是 a 前，声明序翻转为用户优先级）
	if len(attempts) == 0 || attempts[0].Provider != "b" || attempts[0].Model != "model-b-free" {
		t.Fatalf("first attempt = %+v, want b/model-b-free", attempts)
	}
	// 上游收到成员模型，而非 @combo 指令原串
	if got := rec.last(); !strings.Contains(got, `"model":"model-b-free"`) {
		t.Errorf("upstream body = %s, want member model model-b-free", got)
	}
	// 调用方 req 不被逐尝试改写污染
	if req.Model != "@combo:free" {
		t.Errorf("caller req.Model mutated to %q", req.Model)
	}
}

func TestComboMissingProviderSkipped(t *testing.T) {
	r, rec := comboFixture(t)
	r.Combos = map[string]Combo{
		"free": {
			Name: "free",
			Members: []ComboMember{
				{Provider: "ghost", Model: "nope"}, // 未装配：跳过
				{Provider: "a", Model: "model-a-free"},
			},
		},
	}
	resp, attempts, err := r.Dispatch(context.Background(), testRequest(), WithCombo("free"))
	if err != nil || resp == nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Provider != "a" {
		t.Fatalf("attempts = %+v, want only a", attempts)
	}
	if rec.count() != 1 {
		t.Errorf("upstream hits = %d, want 1", rec.count())
	}
}

func TestComboUnknownNameNoAttempts(t *testing.T) {
	r, _ := comboFixture(t)
	r.Combos = comboAB()

	_, _, err := r.Dispatch(context.Background(), testRequest(), WithCombo("nope"))
	if err == nil {
		t.Fatal("未知组合应报错（无候选）")
	}
}

func TestComboWindowFilterByMemberModel(t *testing.T) {
	r, _ := comboFixture(t)
	r.Combos = comboAB()
	// b 的成员模型窗口 4k 装不下 5k 请求；a 的成员模型未收录不过滤
	r.Windows = stubWindows{"b/model-b-free": 4000}

	resp, attempts, err := r.Dispatch(context.Background(), testRequest(),
		WithCombo("free"), WithPromptTokens(5000))
	if err != nil || resp == nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(attempts) == 0 || attempts[0].Provider != "a" {
		t.Fatalf("first attempt = %+v, want a (b filtered by member window)", attempts)
	}
}

func TestComboStreamModelRewrite(t *testing.T) {
	rec := &Recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.record(string(b))
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+chunkPayload("ok")+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	r := &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "a", up.URL),
			newMockAdapter(t, "b", up.URL),
		},
		Combos: comboAB(),
	}

	req := testRequest()
	req.Stream = true
	stream, attempts, err := r.DispatchStream(context.Background(), req, WithCombo("free"))
	if err != nil || stream == nil {
		t.Fatalf("dispatch stream: %v", err)
	}
	defer stream.Close()
	if attempts[len(attempts)-1].Model != "model-b-free" {
		t.Errorf("stream attempt model = %q, want member model", attempts[len(attempts)-1].Model)
	}
	if got := rec.last(); !strings.Contains(got, `"model":"model-b-free"`) {
		t.Errorf("upstream stream body = %s, want member model", got)
	}
}
