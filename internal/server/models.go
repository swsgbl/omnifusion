// models.go 承载 GET /v1/models：把模型目录以 OpenAI list
// 形返回（{"object":"list","data":[{"id",...,"owned_by":provider}]}），
// 数据来自 routing.Catalog 的快照（1h 定时同步 + 校验和判变更）。
package server

import (
	"net/http"
)

// modelCard 是 /v1/models 列表里的一条（OpenAI 口径）。
type modelCard struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// handleModels 返回网关聚合目录；目录未装配或尚无数据时给空列表
// （端点形状稳定，客户端不至于拿到 5xx）。
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	data := []modelCard{}
	if s.catalog != nil {
		entries := s.catalog.Snapshot()
		data = make([]modelCard, 0, len(entries))
		for _, e := range entries {
			data = append(data, modelCard{
				ID:      e.ID,
				Object:  "model",
				Created: 0,
				OwnedBy: e.Provider,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}
