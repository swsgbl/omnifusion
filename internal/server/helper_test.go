package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// testGatewayToken 是测试用网关统一 API Key（M1.8）：凡打到数据面的
// 请求都必须带 `Authorization: Bearer testGatewayToken`。
const testGatewayToken = "ofg-" +
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// authedServer 给被测 Server 装配测试网关 key。
func authedServer(s *Server) *Server {
	s.SetGatewayToken(testGatewayToken)
	return s
}

// postAuthed 以合法网关 key 向数据面发请求。
func postAuthed(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testGatewayToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}

// a2aErrCode 发一条 JSON-RPC 请求并取回错误码（0 = 无 error 字段）。
func a2aErrCode(t *testing.T, url, body string) int {
	t.Helper()
	resp := postAuthed(t, url, body)
	defer resp.Body.Close()
	var rpc struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpc); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if rpc.Error == nil {
		return 0
	}
	return rpc.Error.Code
}

// postBare 不带任何鉴权头（用于 401 断言）。
func postBare(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	return resp
}
