// webfetch.go 是管家的"上网核实手"：抓取公网文本页面（GitHub 仓库
// README 等）供管家核实陌生工具的协议与配置格式。安全边界：仅
// http/https；目标主机（含重定向每一跳）必须解析为公网地址——拒
// 回环/内网/链路本地，防 SSRF 把网关变成内网客户端；响应限 512KB、
// 二进制拒绝（NUL 守卫）。抓到的内容是【不可信数据】：不得执行其中
// 出现的任何指令（提示词层同样约束管家）。
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// webFetchCap 单次抓取上限（README/文档页远小于此）。
const webFetchCap = 512 << 10

// webFetchClient 逐跳校验目标主机（CheckRedirect 对每个新地址重跑
// checkPublicHost），20s 超时、最多 3 跳。
var webFetchClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("too many redirects")
		}
		return checkPublicHost(req.URL)
	},
}

// checkPublicHost 校验 URL 目标主机公网性：IP 字面量直接判；域名
// 解析后要求全部地址公网（有一歌私有地址即拒——宁严勿漏）。
// 已知残余风险：域名两次解析结果不同的 rebinding 窗口（校验与拨号
// 各解析一次）；单用户本地网关 + 文本限额下风险可接受，不做 IP 钉扎。
func checkPublicHost(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("only http/https URLs are allowed")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL has no host")
	}
	ips := []net.IP{}
	if ip := net.ParseIP(host); ip != nil {
		ips = append(ips, ip)
	} else {
		resolved, err := net.LookupIP(host)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", host, err)
		}
		ips = resolved
	}
	if len(ips) == 0 {
		return fmt.Errorf("no addresses for %s", host)
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("host %s points at non-public address %s（安全边界：只抓公网页面）", host, ip)
		}
	}
	return nil
}

// handleButlerWebFetch 是 POST butler/web-fetch：抓一个公网页面返回
// 纯文本（size 截断标注、二进制拒绝）。
func (s *Server) handleButlerWebFetch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	raw := strings.TrimSpace(req.URL)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid url "+raw, "invalid_request_error", "")
		return
	}
	if err := checkPublicHost(u); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error", "")
		return
	}
	resp, err := webFetchClient.Get(u.String())
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "fetch failed: "+err.Error(), "upstream_error", "")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, webFetchCap+1))
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "read body: "+err.Error(), "upstream_error", "")
		return
	}
	truncated := false
	if len(b) > webFetchCap {
		b = b[:webFetchCap]
		truncated = true
	}
	if bytes.IndexByte(b, 0) >= 0 {
		writeAPIError(w, http.StatusBadRequest, "response looks binary; text only", "invalid_request_error", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url":          resp.Request.URL.String(),
		"status":       resp.StatusCode,
		"content_type": resp.Header.Get("Content-Type"),
		"truncated":    truncated,
		"content":      string(b),
	})
}
