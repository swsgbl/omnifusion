// modelgate_test.go 锁死路由面 gate 语义（providers 页模型启停的
// 路由效果）：禁用集中的模型被路由跳过（零上游调用）、未禁用不受
// 影响、全禁用回退未过滤（保守边界——宁可上游拒绝不能全拒）。
package routing

import (
	"context"
	"testing"

	"github.com/swsgbl/omnifusion/internal/core/schema"
	"github.com/swsgbl/omnifusion/internal/provider"
)

// fakeMembershipOf 是最小 ModelMembership：每对 (name, models) 声明
// 可服务集合（等价 Catalog 快照语义）。
func fakeMembershipOf(pairs ...any) ModelMembership {
	m := map[string]map[string]bool{}
	for i := 0; i+1 < len(pairs); i += 2 {
		name := pairs[i].(string)
		ids := pairs[i+1].([]string)
		set := map[string]bool{}
		for _, id := range ids {
			set[id] = true
		}
		m[name] = set
	}
	return catalogMembership{m: m}
}

type catalogMembership struct {
	m map[string]map[string]bool
}

func (c catalogMembership) ServesModel(providerName, model string) bool {
	return c.m[providerName][model]
}

type fakeGate map[string]map[string]bool

func (f fakeGate) DisabledModels(p string) map[string]bool { return f[p] }

func gateReq(model string) *schema.UnifiedRequest {
	return &schema.UnifiedRequest{
		Model:    model,
		Messages: []schema.Message{{Role: "user", Content: schema.NewTextContent("hi")}},
	}
}

func TestDispatchGateSkipsDisabledModel(t *testing.T) {
	upA := newMemberUpstream(t, "m-a")
	upB := newMemberUpstream(t, "m-b")
	r := &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "b", upB.srv.URL),
			newMockAdapter(t, "a", upA.srv.URL),
		},
		Models: fakeMembershipOf("b", []string{"m-b"}, "a", []string{"m-a"}),
		Gate:   fakeGate{"b": {"m-a": true}}, // b 家的 m-a 被用户停用
	}
	// 注册序 b 在前；无 gate 时 b 会吃一次 404 才回退 a——gate 生效
	// 则 b 零调用直达 a。
	resp, attempts, err := r.Dispatch(context.Background(), gateReq("m-a"))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.ProviderName != "a" || len(attempts) != 1 {
		t.Fatalf("resp=%s attempts=%d, want a 单条（b/m-a 被停用零调用）", resp.ProviderName, len(attempts))
	}
	if upB.hits != 0 {
		t.Fatalf("b 被调用 %d 次，want 0（gate 未生效）", upB.hits)
	}
	// 未停用的模型不受影响：m-b 在 b 家正常路由。
	resp2, _, err := r.Dispatch(context.Background(), gateReq("m-b"))
	if err != nil || resp2.ProviderName != "b" {
		t.Fatalf("m-b resp=%s err=%v, want b（未停用不受影响）", resp2.ProviderName, err)
	}
}

func TestDispatchGateAllDisabledFallsBack(t *testing.T) {
	upA := newMemberUpstream(t, "m-a")
	upB := newMemberUpstream(t, "m-b")
	r := &Router{
		Providers: []provider.Provider{
			newMockAdapter(t, "a", upA.srv.URL),
			newMockAdapter(t, "b", upB.srv.URL),
		},
		Models: fakeMembershipOf("a", []string{"m-a"}, "b", []string{"m-b"}),
		Gate:   fakeGate{"a": {"m-a": true}, "b": {"m-a": true}}, // 全禁
	}
	// 全禁回退未过滤列表（保守边界）：回退后照常逐家尝试——a 在注册
	// 序首位且实际服务 m-a，一次尝试即成功（回退=用户开关暂时失效，
	// 但请求不至于全拒）。
	resp, attempts, err := r.Dispatch(context.Background(), gateReq("m-a"))
	if err != nil {
		t.Fatalf("fallback dispatch: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Provider != "a" {
		t.Fatalf("attempts = %+v, want 仅 a 一条（回退后按注册序首中）", attempts)
	}
	if resp.ProviderName != "a" {
		t.Fatalf("fallback winner = %s, want a（回退后首个能服务的家）", resp.ProviderName)
	}
}

func TestDispatchGateNilPasses(t *testing.T) {
	upA := newMemberUpstream(t, "m-a")
	r := &Router{
		Providers: []provider.Provider{newMockAdapter(t, "a", upA.srv.URL)},
		Models:    fakeMembershipOf("a", []string{"m-a"}),
	}
	resp, _, err := r.Dispatch(context.Background(), gateReq("m-a"))
	if err != nil || resp.ProviderName != "a" {
		t.Fatalf("nil gate must not filter: resp=%s err=%v", resp.ProviderName, err)
	}
}
