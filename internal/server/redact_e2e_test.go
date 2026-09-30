// redact_e2e_test.go 是蓝图 Phase 8 的利用回归测试（攻击者视角）：
// ① 恶意上游在错误体里回显密钥 → 网关日志与客户端错误面必须只见
// [REDACTED]；② scoped token 打数据面必须 401（admin/data plane
// 边界——跨权限缓存命中面不存在）；③ 缓存键含 user 字段——不同
// user 的同形请求不得互相命中（蓝图红线：cache hit 不跨权限）。
package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
)

// 恶意上游回显的密钥形态（模拟把 Authorization 原样吐回错误体）。
const (
	leakedBearer = "sk-echoed-by-malicious-upstream-9876543210"
	leakedKV     = "hf_echoedKeyValueSecret1234567890"
)

func TestUpstreamSecretEchoRedacted(t *testing.T) {
	var logMu sync.Mutex
	logBuf := &strings.Builder{}
	log := slog.New(slog.NewTextHandler(&lockedWriter{mu: &logMu, dst: logBuf}, nil))

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"auth failed for Bearer `+leakedBearer+
			` and api_key=`+leakedKV+`","type":"upstream_error"}}`)
	}))
	defer up.Close()

	a, err := openai_compat.New(openai_compat.Spec{ProviderName: "evil", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		t.Fatalf("adapter: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()
	s := authedServer(New(nil, log, st))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{a}})
	gw := httptest.NewServer(s.Handler())
	defer gw.Close()

	resp := postAuthed(t, gw.URL+"/v1/chat/completions",
		`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// 客户端错误面：密钥形态不出网关。
	if strings.Contains(string(body), leakedBearer) || strings.Contains(string(body), leakedKV) {
		t.Fatalf("client error surface leaked secrets: %s", body)
	}
	// 日志面：上游错误体（UpstreamError.Error 含 512B 体）必须只见
	// [REDACTED]，密钥本体不落日志。
	logMu.Lock()
	logged := logBuf.String()
	logMu.Unlock()
	if strings.Contains(logged, leakedBearer) || strings.Contains(logged, leakedKV) {
		t.Fatalf("gateway log leaked secrets: %s", logged)
	}
	if !strings.Contains(logged, "[REDACTED]") {
		t.Fatalf("redaction marker missing from log: %s", logged)
	}
}

type lockedWriter struct {
	mu  *sync.Mutex
	dst *strings.Builder
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dst.Write(p)
}

func TestScopedTokenRejectedFromDataPlane(t *testing.T) {
	url, hits, _ := newCacheFixture(t, false)
	scoped := DeriveMCPToken(testGatewayToken, []string{ScopeHealth})
	if scoped == "" {
		t.Fatal("derive scoped token failed")
	}
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/chat/completions",
		strings.NewReader(`{"model":"model-a","messages":[{"role":"user","content":"ping"}],"temperature":0}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+scoped)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("scoped token on data plane = %d, want 401 (no cross-privilege cache surface)", resp.StatusCode)
	}
	if hits.Load() != 0 {
		t.Fatalf("scoped request must never reach upstream")
	}
}

func TestCacheUserFieldSegregation(t *testing.T) {
	url, hits, _ := newCacheFixture(t, false)
	alice := `{"model":"model-a","temperature":0,"user":"alice","messages":[{"role":"user","content":"ping"}]}`
	bob := `{"model":"model-a","temperature":0,"user":"bob","messages":[{"role":"user","content":"ping"}]}`

	first := postAuthed(t, url+"/v1/chat/completions", alice)
	io.Copy(io.Discard, first.Body)
	first.Body.Close()

	second := postAuthed(t, url+"/v1/chat/completions", bob)
	if got := second.Header.Get("X-OmniFusion-Cache"); got == "hit" {
		t.Fatal("different user must NOT hit alice's cache entry (cross-privilege leak)")
	}
	io.Copy(io.Discard, second.Body)
	second.Body.Close()
	if hits.Load() != 2 {
		t.Fatalf("upstream hits = %d, want 2 (bob missed)", hits.Load())
	}
}
