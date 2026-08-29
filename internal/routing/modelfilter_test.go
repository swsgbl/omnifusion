// modelfilter_test.go 验证模型成员过滤（docs/00 §4.5 遗留项落地）：
// 裸模型请求只尝试目录声明可服务该模型的 provider——候选序中被墙/
// 不可达家不再吃满上游超时才回退（bench 实证：每新会话首请求 ~25s）。
// 保守边界与 filterByWindow 同哲学：无快照不过滤、全排除回退未过滤。
package routing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// TestCatalogServesModel 单元：成员判定的三种态（命中/明确不服务/
// 无快照不确定）与「厂商/模型」后缀规则。
func TestCatalogServesModel(t *testing.T) {
	c := NewCatalog(nil, nil, nil, nil, nil)
	c.mu.Lock()
	c.models["p"] = []provider.ModelInfo{
		{ID: "qwen3:4b"},
		{ID: "openai/gpt-4o"},
	}
	c.mu.Unlock()

	cases := []struct {
		model string
		want  bool
		why   string
	}{
		{"qwen3:4b", true, "精确命中"},
		{"gpt-4o", true, "厂商前缀后缀命中（OpenRouter 风格限定 id）"},
		{"llama-3.3-70b", false, "快照明确不含"},
	}
	for _, tc := range cases {
		if got := c.ServesModel("p", tc.model); got != tc.want {
			t.Errorf("ServesModel(p, %s) = %v, want %v（%s）", tc.model, got, tc.want, tc.why)
		}
	}
	if !c.ServesModel("unknown-provider", "any-model") {
		t.Error("ServesModel 无快照 provider 应返回 true（不确定不过滤）")
	}
}

// memberUpstream 是一家只服务自己模型清单的上游：/v1/models 报清单，
// chat 仅当请求模型在清单内才 200，否则 404（证明没被过滤时它会被
// 尝试并失败——过滤生效后则零调用）。
type memberUpstream struct {
	models []string
	hits   int
	srv    *httptest.Server
}

func newMemberUpstream(t *testing.T, models ...string) *memberUpstream {
	t.Helper()
	u := &memberUpstream{models: models}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			type row struct {
				ID string `json:"id"`
			}
			rows := make([]row, 0, len(u.models))
			for _, m := range u.models {
				rows = append(rows, row{ID: m})
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": rows})
		case "/v1/chat/completions":
			u.hits++
			var body struct {
				Model string `json:"model"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			for _, m := range u.models {
				if body.Model == m {
					io.WriteString(w, okCompletion(m))
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":{"message":"model not found"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

// TestDispatchFiltersByModelMembership 端到端：候选序首家（B）不服务
// 请求模型时被过滤，零 HTTP 调用直达 A——被墙家吃满上游超时的场景。
func TestDispatchFiltersByModelMembership(t *testing.T) {
	upA := newMemberUpstream(t, "m-a")
	upB := newMemberUpstream(t, "m-b")

	cat := NewCatalog([]provider.Provider{
		newMockAdapter(t, "b", upB.srv.URL),
		newMockAdapter(t, "a", upA.srv.URL),
	}, nil, nil, nil, nil)
	cat.Sync(context.Background()) // 快照就位：a=[m-a]、b=[m-b]

	// 注册序 b 在前：无过滤时 b 先被尝试（404）再回退 a。
	r := &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "b", upB.srv.URL),
			newMockAdapter(t, "a", upA.srv.URL),
		},
		Models: cat,
	}
	req := &schema.UnifiedRequest{
		Model:    "m-a",
		Messages: []schema.Message{{Role: "user", Content: schema.NewTextContent("hi")}},
	}
	resp, attempts, err := r.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.ProviderName != "a" {
		t.Errorf("ProviderName = %q, want a", resp.ProviderName)
	}
	if len(attempts) != 1 || attempts[0].Provider != "a" {
		t.Fatalf("attempts = %+v, want 仅 a 一条（b 应被过滤零调用）", attempts)
	}
	if upB.hits != 0 {
		t.Errorf("provider b 被调用 %d 次，want 0（成员过滤未生效）", upB.hits)
	}
}

// TestDispatchModelFilterFallsBackWhenUnknown 保守回退：无人声明该模型
// （可能是目录未同步或别名场景）时回退未过滤列表——保持既有 502 语义。
func TestDispatchModelFilterFallsBackWhenUnknown(t *testing.T) {
	upA := newMemberUpstream(t, "m-a")
	upB := newMemberUpstream(t, "m-b")

	cat := NewCatalog([]provider.Provider{
		newMockAdapter(t, "a", upA.srv.URL),
		newMockAdapter(t, "b", upB.srv.URL),
	}, nil, nil, nil, nil)
	cat.Sync(context.Background())

	r := &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "a", upA.srv.URL),
			newMockAdapter(t, "b", upB.srv.URL),
		},
		Models: cat,
	}
	req := &schema.UnifiedRequest{
		Model:    "nobody-has-this",
		Messages: []schema.Message{{Role: "user", Content: schema.NewTextContent("hi")}},
	}
	_, attempts, err := r.Dispatch(context.Background(), req)
	if err == nil {
		t.Fatal("期望全部失败（上游都不服务该模型）")
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2（全排除须回退未过滤列表逐家尝试）", len(attempts))
	}
}

// TestDispatchModelFilterKeepsUncataloged 无快照 provider 不被过滤
// （目录未同步不是排除理由）。
func TestDispatchModelFilterKeepsUncataloged(t *testing.T) {
	upA := newMemberUpstream(t, "m-a")
	cat := NewCatalog([]provider.Provider{
		newMockAdapter(t, "a", upA.srv.URL),
	}, nil, nil, nil, nil)
	cat.Sync(context.Background()) // 只有 a 的快照；b 无快照

	upB := newMemberUpstream(t, "m-b")
	r := &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "a", upA.srv.URL),
			newMockAdapter(t, "b", upB.srv.URL),
		},
		Models: cat,
	}
	req := &schema.UnifiedRequest{
		Model:    "m-b",
		Messages: []schema.Message{{Role: "user", Content: schema.NewTextContent("hi")}},
	}
	resp, attempts, err := r.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.ProviderName != "b" || len(attempts) != 1 {
		t.Fatalf("resp=%s attempts=%d, want b 单条（a 明确不含被滤、b 无快照保留）",
			resp.ProviderName, len(attempts))
	}
}
