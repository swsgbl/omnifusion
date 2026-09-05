// butler_api.go 是 管家模式的服务端执行面：扫描本机已装的 AI CLI 工具、
// 把聚合密钥确定性接入/移除。写入器在 internal/connect（ofd connect
// 共用同一份，行为一致）；LLM（管家）只编排，写操作是确定性 Go 代码。
// 鉴权：与 /dashboard/api/ 同一 scope 体系——扫描属 health（只读），
// 接入/移除属 route（变更类），scoped token 按权限收敛。
package server

import (
	"encoding/json"
	"net/http"

	"github.com/swsgbl/omnifusion/internal/connect"
)

// butlerOrigin 归一化对外展示的网关根地址（客户端应指向的地址）。
// 与 CLI 的 clientBaseURL 共用 connect.Origin，保证接入目标一致。
func (s *Server) butlerOrigin() string {
	return connect.Origin(s.cfg.Server.Host, s.cfg.Server.Port)
}

// handleButlerScan 返回本机已知 AI CLI 的安装/接入状态。
func (s *Server) handleButlerScan(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tools": connect.Scan(s.butlerOrigin())})
}

// butlerToolRequest 是 wire/unwire 端点的请求体。
type butlerToolRequest struct {
	Tool string `json:"tool"`
}

// handleButlerWire 把聚合密钥接入目标 CLI（确定性写入，自动备份）。
func (s *Server) handleButlerWire(w http.ResponseWriter, r *http.Request) {
	var req butlerToolRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	if !connect.Valid(req.Tool) {
		writeAPIError(w, http.StatusBadRequest, "unknown tool "+req.Tool, "invalid_request_error", "")
		return
	}
	msg, err := connect.Wire(req.Tool, s.butlerOrigin(), s.gatewayToken)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error(), "internal_error", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tool": req.Tool, "result": msg})
}

// handleButlerUnwire 移除目标 CLI 的 omnifusion 接入。
func (s *Server) handleButlerUnwire(w http.ResponseWriter, r *http.Request) {
	var req butlerToolRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	if !connect.Valid(req.Tool) {
		writeAPIError(w, http.StatusBadRequest, "unknown tool "+req.Tool, "invalid_request_error", "")
		return
	}
	msg, err := connect.Unwire(req.Tool)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, err.Error(), "internal_error", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tool": req.Tool, "result": msg})
}

// butlerFindRequest 是 find-tool 端点的请求体（name 空 = 无名扫描）。
type butlerFindRequest struct {
	Name string `json:"name"`
}

// handleButlerFind 在本机搜索 AI 工具的踪迹（PATH 可执行、home 点
// 目录、~/.config、AppData）——五家内置之外的一切工具的"找到"那只手。
func (s *Server) handleButlerFind(w http.ResponseWriter, r *http.Request) {
	var req butlerFindRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": connect.FindTool(req.Name)})
}

// butlerReadRequest 是 read-config 端点的请求体。
type butlerReadRequest struct {
	Path string `json:"path"`
}

// handleButlerRead 读取 home 内一个文本配置（二进制/越界/超限拒绝）——
// "了解工具的结构"那只手。
func (s *Server) handleButlerRead(w http.ResponseWriter, r *http.Request) {
	var req butlerReadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	out, err := connect.ReadConfig(req.Path)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// butlerWriteRequest 是 write-config 端点的请求体。
type butlerWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// handleButlerWrite 把内容写入 home 内一个配置文件（覆盖前自动备份，
// 不新建目录）——"把密钥写进去"那只手。
func (s *Server) handleButlerWrite(w http.ResponseWriter, r *http.Request) {
	var req butlerWriteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<19)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	out, err := connect.WriteConfig(req.Path, req.Content)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
