package routing

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/swsgbl/omnifusion/internal/provider"
)

// stubWindows 是 WindowResolver 的测试桩：key = "provider/model"。
type stubWindows map[string]int64

func (s stubWindows) ContextWindow(providerName, model string) (int64, bool) {
	w, ok := s[providerName+"/"+model]
	return w, ok
}

// windowRouter 构造 a、b 两家 mock provider 的路由器（注册序 a 前）。
func windowRouter(t *testing.T, w WindowResolver) (*Router, *httptest.Server) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, okCompletion("m"))
	}))
	t.Cleanup(up.Close)
	return &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "a", up.URL),
			newMockAdapter(t, "b", up.URL),
		},
		Windows: w,
	}, up
}

func TestRouterFiltersByContextWindow(t *testing.T) {
	w := stubWindows{"a/m": 4000, "b/m": 200000}
	r, _ := windowRouter(t, w)
	req := testRequest()

	// 压缩前（粗估 5000）：a 的 4k 窗口装不下 → 只剩 b。
	resp, attempts, err := r.Dispatch(context.Background(), req, WithPromptTokens(5000))
	if err != nil || resp == nil {
		t.Fatalf("dispatch: %v", err)
	}
	if attempts[0].Provider != "b" {
		t.Fatalf("first attempt = %s, want b (a filtered by window)", attempts[0].Provider)
	}

	// 压缩后（粗估 3000）：a 装得下 → 回到注册序首选 a。
	r2, _ := windowRouter(t, w)
	resp2, attempts2, err := r2.Dispatch(context.Background(), req, WithPromptTokens(3000))
	if err != nil || resp2 == nil {
		t.Fatalf("dispatch: %v", err)
	}
	if attempts2[0].Provider != "a" {
		t.Fatalf("first attempt = %s, want a (small window fits after compression)",
			attempts2[0].Provider)
	}
}

func TestRouterWindowFallbackWhenAllExcluded(t *testing.T) {
	w := stubWindows{"a/m": 100, "b/m": 100}
	r, _ := windowRouter(t, w)
	_, attempts, err := r.Dispatch(context.Background(), testRequest(), WithPromptTokens(999999))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if attempts[0].Provider != "a" {
		t.Fatalf("all-excluded must fall back to full list, got %s", attempts[0].Provider)
	}
}

func TestRouterWindowUnknownModelNotFiltered(t *testing.T) {
	w := stubWindows{"a/m": 100} // b 未收录：不过滤
	r, _ := windowRouter(t, w)
	_, attempts, err := r.Dispatch(context.Background(), testRequest(), WithPromptTokens(999999))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if attempts[0].Provider != "b" {
		t.Fatalf("unknown-catalog candidate must stay, got %s", attempts[0].Provider)
	}
}

func TestRouterWindowDisabledWithoutTokens(t *testing.T) {
	r, _ := windowRouter(t, stubWindows{"a/m": 1, "b/m": 1})
	_, attempts, err := r.Dispatch(context.Background(), testRequest()) // 无 token 输入
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if attempts[0].Provider != "a" {
		t.Fatal("no token input must not filter")
	}
}

func TestRouterWindowAppliesToStream(t *testing.T) {
	w := stubWindows{"a/m": 4000, "b/m": 200000}
	r, up := windowRouter(t, w)
	up.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: "+chunkPayload("ok")+"\n\n"+"data: [DONE]\n\n")
	})
	req := testRequest()
	stream, attempts, err := r.DispatchStream(context.Background(), req, WithPromptTokens(5000))
	if err != nil {
		t.Fatalf("dispatch stream: %v", err)
	}
	defer stream.Close()
	if attempts[0].Provider != "b" {
		t.Fatalf("stream first attempt = %s, want b", attempts[0].Provider)
	}
}
