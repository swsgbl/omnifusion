// update.go 是 无遥测的"拉取式"更新检查：启动后与每 24h 静默拉一次
// Release 元数据（GitHub api.github.com 与国内直连的 GitCode 双源，
// 国内源优先），只取 {tag_name, html_url} 比对本地版本——不发任何
// 用户数据、不带认证、失败静默下个周期再试。发现新版本经
// dashboard API 暴露给 UI（横幅/徽标/管家）；升级动作永远由用户在
// 浏览器确认（零遥测红线 + 持密钥进程不后台静默替换二进制）。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// updateSource 是一个 Release 元数据源。国内主入口（AtomGit）直连可达，
// 排在前；GitHub api.github.com 被墙环境下走系统代理或失败静默。
// AtomGit 与 GitCode 是同一仓库的双域入口（数据同源）——主入口用
// atomgit.com，gitcode 域留作第三路兜底。
var updateSources = []string{
	"https://atomgit.com/api/v5/repos/hongfu/omnifusion/releases/latest",
	"https://api.github.com/repos/swsgbl/omnifusion/releases/latest",
	"https://gitcode.com/api/v5/repos/hongfu/omnifusion/releases/latest",
}

// updateInterval 常规检查周期（24h）；启动首轮延迟 30s 让网关先就绪。
const (
	updateInterval = 24 * time.Hour
	updateFirstTry = 30 * time.Second
	updateTimeout  = 15 * time.Second
)

// UpdateInfo 是暴露给 UI 的检查结果。
type UpdateInfo struct {
	Current         string `json:"current"`          // 本地运行版本
	Latest          string `json:"latest,omitempty"` // 远端最新 tag（如 v0.1.9）
	ReleaseURL      string `json:"release_url,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	CheckedAt       string `json:"checked_at,omitempty"` // 最近一次成功检查（RFC3339）
	Source          string `json:"source,omitempty"`     // 命中的源（gitcode/github）
}

// updateChecker 是 Server 上的检查器状态（nil-safe：未启用时零行为）。
type updateChecker struct {
	mu     sync.RWMutex
	info   UpdateInfo
	client *http.Client
}

// StartUpdateChecker 启动后台检查循环（导出给 main 接线；防重复）。
// version==dev（开发构建）也检查——开发者同样想知道有新版本。
func (s *Server) StartUpdateChecker(ctx context.Context) {
	if s.updates != nil {
		return // 防重复启动
	}
	uc := &updateChecker{client: &http.Client{Timeout: updateTimeout}}
	s.updates = uc
	go func() {
		// 启动首轮：先等网关就绪，再立即查一次（首启即知更新，不等 24h）。
		select {
		case <-time.After(updateFirstTry):
		case <-ctx.Done():
			return
		}
		uc.check(version)
		t := time.NewTicker(updateInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				uc.check(version)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// check 依序尝试各源；任一成功即记录并返回（不阻塞、不重试同轮失败源）。
func (u *updateChecker) check(current string) {
	info := UpdateInfo{Current: current}
	for _, src := range updateSources {
		tag, url, err := fetchLatestRelease(u.client, src)
		if err != nil {
			continue
		}
		info.Latest = tag
		info.ReleaseURL = url
		info.UpdateAvailable = newerVersion(current, tag)
		info.CheckedAt = time.Now().UTC().Format(time.RFC3339)
		info.Source = sourceName(src)
		break
	}
	u.mu.Lock()
	u.info = info
	u.mu.Unlock()
}

// snapshot 返回当前检查结果（零值安全）。
func (u *updateChecker) snapshot() UpdateInfo {
	if u == nil {
		return UpdateInfo{Current: version}
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.info
}

// fetchLatestRelease 拉一个源的 latest release，只取 tag 与页面地址。
// 两个平台的 latest JSON 形状同构（Gitee 系 API），GitHub 原生同构。
func fetchLatestRelease(client *http.Client, src string) (tag, htmlURL string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch %s: %w", src, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("%s: status %d", src, resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", "", err
	}
	if body.TagName == "" {
		return "", "", fmt.Errorf("%s: no tag_name", src)
	}
	url := body.HTMLURL
	if url == "" {
		url = releasePageFallback(src)
	}
	return body.TagName, url, nil
}

// releasePageFallback html_url 缺失时的页面地址推导。
func releasePageFallback(apiURL string) string {
	if strings.Contains(apiURL, "gitcode") {
		return "https://gitcode.com/hongfu/omnifusion/releases"
	}
	return "https://github.com/swsgbl/omnifusion/releases"
}

// sourceName 源的可读名（观测用）。
func sourceName(src string) string {
	if strings.Contains(src, "gitcode") {
		return "gitcode"
	}
	return "github"
}

// newerVersion 语义化版本比较：current 形如 "v0.1.8" 或 "dev"（dev
// 恒视为可更新——开发构建提示远端最新无妨）。
func newerVersion(current, latest string) bool {
	if latest == "" {
		return false
	}
	if current == "" || current == "dev" {
		return true
	}
	c, ok1 := parseSemVer(current)
	l, ok2 := parseSemVer(latest)
	if !ok1 || !ok2 {
		return false // 解析失败宁可不提醒（不拿猜测打扰用户）
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// parseSemVer 把 "v0.1.8" 解析为 [0,1,8]；非三段数字视为失败。
func parseSemVer(s string) ([3]int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var out [3]int
	for i, part := range strings.SplitN(s, ".", 3) {
		n := 0
		for _, r := range part {
			if r < '0' || r > '9' {
				return out, false
			}
			n = n*10 + int(r-'0')
		}
		out[i] = n
	}
	return out, true
}

// handleUpdateInfo 是 dashboard API：GET update（health scope——只读）。
func (s *Server) handleUpdateInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.updates.snapshot())
}
