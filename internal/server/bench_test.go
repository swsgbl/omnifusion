// bench_test.go 是蓝图 Phase 10 的吞吐/内存基准（套件 H）：
// go test -bench 运行（CI 默认不跑 -bench，零成本）；本机跑法——
//
//	go test ./internal/server/ -bench BenchmarkGateway -benchmem -benchtime 3s
//
// 报告纪律（蓝图 §11）：跑基准前后各记一次 go version 与 commit，
// 数字只与同机同数据版本对比（chaos-mocks-v1 的上游行为）。
package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/openai_compat"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/store"
)

// benchGateway 装配基准网关：本地 mock 上游（无延迟 200）+ 全链
// （鉴权→解析→护栏→路由→审计落库），指标含 SQLite 写入。
func benchGateway(b *testing.B) *httptest.Server {
	b.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, healthyUpstreamBody())
	}))
	b.Cleanup(up.Close)
	a, err := openai_compat.New(openai_compat.Spec{ProviderName: "mock", BaseURL: up.URL + "/v1", APIKey: "k"})
	if err != nil {
		b.Fatalf("adapter: %v", err)
	}
	st, err := store.Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatalf("store.Open: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })
	s := authedServer(New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), st))
	s.SetRouter(&routing.Router{Providers: []provider.Provider{a}})
	gw := httptest.NewServer(s.Handler())
	b.Cleanup(gw.Close)
	return gw
}

// BenchmarkGatewayChatThroughput 非流式 chat 全链吞吐与分配。
func BenchmarkGatewayChatThroughput(b *testing.B) {
	gw := benchGateway(b)
	body := `{"model":"model-a","messages":[{"role":"user","content":"bench"}]}`
	client := &http.Client{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest(http.MethodPost, gw.URL+"/v1/chat/completions", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+testGatewayToken)
		resp, err := client.Do(req)
		if err != nil {
			b.Fatalf("do: %v", err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b.Fatalf("status = %d", resp.StatusCode)
		}
	}
}
