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

// butlerReadRequest 是 read-file 端点的请求体。
type butlerReadRequest struct {
	Path string `json:"path"`
}

// handleButlerRead 读取 home 内一个文本文件——配置/日志/脚本皆可
//（二进制/越界/超限拒绝）——"Read"那只手。
func (s *Server) handleButlerRead(w http.ResponseWriter, r *http.Request) {
	var req butlerReadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	out, err := connect.ReadFile(req.Path)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// butlerEditRequest 是 edit-file 端点的请求体。
type butlerEditRequest struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// handleButlerEdit 对 home 内一个文本文件做唯一匹配替换（"Edit"那只
// 手，非 JSON 文件的精确改动形态）。
func (s *Server) handleButlerEdit(w http.ResponseWriter, r *http.Request) {
	var req butlerEditRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	out, err := connect.EditFile(req.Path, req.OldString, req.NewString)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// butlerWriteRequest 是 write-file 端点的请求体。
type butlerWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// handleButlerWrite 把内容写入 home 内一个文件（覆盖前自动备份，
// 不新建目录）——"Write"那只手，整文件形态（全新文件/非 JSON 结构）。
func (s *Server) handleButlerWrite(w http.ResponseWriter, r *http.Request) {
	var req butlerWriteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<19)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	out, err := connect.WriteFile(req.Path, req.Content)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// butlerPatchRequest 是 patch-config 端点的请求体：JSON 配置点补丁，
// 模型只提交改动点——大配置整文件重写会撑爆输出上限被截断。
type butlerPatchRequest struct {
	Path string              `json:"path"`
	Ops  []connect.PatchOp   `json:"ops"`
}

// handleButlerPatch 对 home 内一个 JSON 配置应用确定性点补丁。
func (s *Server) handleButlerPatch(w http.ResponseWriter, r *http.Request) {
	var req butlerPatchRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	out, err := connect.PatchConfig(req.Path, req.Ops)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}
	writeJSON(w, http.StatusOK, out)
}
