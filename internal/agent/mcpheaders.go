// mcpheaders.go 是 MCP 可路由标头与工具目录版本面（蓝图 Phase 6：
// per-request metadata、Mcp-Method/Mcp-Name 可路由标头、工具目录
// version/hash——目录变化对代理/客户端可见，不必解析 JSON-RPC 体）。
//
// 语义（2026-07-28 思路，加法式兼容层——不带标头的旧客户端零影响）：
//   - Mcp-Method：请求声明的 JSON-RPC 方法（如 tools/list、tools/call）；
//   - Mcp-Name：目标名（tools/call 的工具名等）；
//   - MCP-Protocol-Version：客户端声明的协商协议版本（进日志=显式维度）。
//
// 标头是数据不是指令：与请求体 method 冲突时告警并仍以请求体为准
// （标头只作用于观测面——鉴权与工具注册不受标头影响）。
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolCatalogHeader 是响应头：工具目录指纹（前 12 位 hex）——同 scope
// 集=同指纹；工具集任何变化（增删/描述改）必然改指纹。代理/客户端
// 据此检测目录漂移，无需解析 tools/list 载荷。
const ToolCatalogHeader = "X-OmniFusion-Tool-Catalog"

// 请求侧可路由标头常量。
const (
	HeaderMcpMethod          = "Mcp-Method"
	HeaderMcpName            = "Mcp-Name"
	HeaderMcpProtocolVersion = "MCP-Protocol-Version"
)

// mismatchBodyLimit 是标头/请求体一致性检查的体长上限（超过不缓存
// 体——大载荷（工具参数）不为观测让路）。
const mismatchBodyLimit = 32 << 10

// catalogCache 缓存 scope 集 → 工具目录指纹（工具面由代码注册决定，
// 进程内不变；指纹经真实 MCP 会话枚举，算一次复用）。
var (
	catalogMu    sync.Mutex
	catalogCache = map[string]string{}
)

// ToolCatalogVersion 返回给定 scope 集将注册的工具目录指纹：起一个
// 进程内 MCP 会话真实枚举 tools/list（与客户端同路径，零漂移），
// 对（工具名 + 描述）按名排序做 SHA-256 取前 12 位 hex。空 scope 集
// 返回 "none"。
func ToolCatalogVersion(ctx context.Context, version string, scopes []string) (string, error) {
	key := strings.Join(scopes, ",")
	catalogMu.Lock()
	if v, ok := catalogCache[key]; ok {
		catalogMu.Unlock()
		return v, nil
	}
	catalogMu.Unlock()

	v := "none"
	if len(scopes) > 0 {
		s := NewMCPServer(nil, version, scopes)
		c := mcp.NewClient(&mcp.Implementation{Name: "ofd-catalog-probe", Version: "v0"}, nil)
		t1, t2 := mcp.NewInMemoryTransports()
		if _, err := s.Connect(ctx, t1, nil); err != nil {
			return "", err
		}
		cs, err := c.Connect(ctx, t2, nil)
		if err != nil {
			return "", err
		}
		res, err := cs.ListTools(ctx, nil)
		closeErr := cs.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
		lines := make([]string, 0, len(res.Tools))
		for _, tool := range res.Tools {
			lines = append(lines, tool.Name+"\x00"+tool.Description)
		}
		sort.Strings(lines)
		sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
		v = hex.EncodeToString(sum[:])[:12]
	}

	catalogMu.Lock()
	catalogCache[key] = v
	catalogMu.Unlock()
	return v, nil
}

// MCPRoutingHandler 组装完整 /mcp HTTP 面：既有 ScopedHTTPHandler
// （scope 化工具注册）+ 可路由标头观测（Mcp-Method/Mcp-Name/协议版本
// 进结构化日志；标头与请求体 method 冲突告警）+ 响应携带工具目录指纹
// 头。scopesFor 会被调用两次（本层取指纹、内层建 server）——HMAC
// 枚举在个人网关规模下可忽略。
func MCPRoutingHandler(view *GatewayView, version string, scopesFor func(*http.Request) ([]string, bool), log *slog.Logger) http.Handler {
	inner := ScopedHTTPHandler(view, version, scopesFor)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logRoutingHeaders(r, log)
		checkMethodMismatch(r, log)
		if scopes, ok := scopesFor(r); ok {
			if v, err := ToolCatalogVersion(r.Context(), version, scopes); err == nil {
				w.Header().Set(ToolCatalogHeader, v)
			} else if log != nil {
				log.Warn("tool catalog fingerprint failed", "err", err)
			}
		}
		inner.ServeHTTP(w, r)
	})
}

// logRoutingHeaders 把可路由标头写进结构化日志（蓝图：协议版本是
// 显式维度；排障可回放）。任何标头缺失都是常态（legacy 客户端）。
func logRoutingHeaders(r *http.Request, log *slog.Logger) {
	method := r.Header.Get(HeaderMcpMethod)
	name := r.Header.Get(HeaderMcpName)
	proto := r.Header.Get(HeaderMcpProtocolVersion)
	if method == "" && name == "" && proto == "" {
		return
	}
	if log == nil {
		return
	}
	log.Info("mcp routable headers",
		HeaderMcpMethod, method, HeaderMcpName, name,
		HeaderMcpProtocolVersion, proto)
}

// checkMethodMismatch 校验 Mcp-Method 与请求体 JSON-RPC method 的一致
// 性（体长受限才检查；冲突告警但不拒——请求体是权威，标头只是路由
// 提示）。读过的体原样放回。
func checkMethodMismatch(r *http.Request, log *slog.Logger) {
	header := r.Header.Get(HeaderMcpMethod)
	if header == "" || r.Method != http.MethodPost {
		return
	}
	if r.ContentLength < 0 || r.ContentLength > mismatchBodyLimit {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, mismatchBodyLimit+1))
	if err != nil || len(body) > mismatchBodyLimit {
		// 读失败/超限：放回已读部分，跳过检查。
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var probe struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(body, &probe) != nil || probe.Method == "" || probe.Method == header {
		return
	}
	if log != nil {
		log.Warn("mcp routable header contradicts request body",
			HeaderMcpMethod, header, "body_method", probe.Method,
			"hint", "body is authoritative; header ignored for routing")
	}
}
