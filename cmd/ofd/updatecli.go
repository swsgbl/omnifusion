// updatecli.go 是 CLI 侧的轻量更新检查（ofd status 末尾一行对照）：
// 直查 Release 元数据（国内 GitCode 优先/GitHub 兜底，与 server/update.go
// 同源），失败静默。CLI 每次运行都是新进程，不维护缓存——查不到就
// 不显示，绝不阻塞 status 主视图。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// cliUpdateSources 与 server 侧同源序（国内主入口 AtomGit 优先）。
var cliUpdateSources = []string{
	"https://atomgit.com/api/v5/repos/hongfu/omnifusion/releases/latest",
	"https://api.github.com/repos/swsgbl/omnifusion/releases/latest",
	"https://gitcode.com/api/v5/repos/hongfu/omnifusion/releases/latest",
}

// renderUpdateHint 查最新版本并在有更新时打一行提示；任何失败静默。
func renderUpdateHint(w io.Writer, current string) {
	tag := fetchLatestTagCLI()
	if tag == "" || !cliNewer(current, tag) {
		return
	}
	_, _ = fmt.Fprintf(w, "\nupdate: %s available (this: %s) — https://atomgit.com/hongfu/omnifusion/releases\n", tag, current)
}

// fetchLatestTagCLI 依序拉源，取第一个成功的 tag_name。
func fetchLatestTagCLI() string {
	client := &http.Client{Timeout: 10 * time.Second}
	for _, src := range cliUpdateSources {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if err != nil {
			cancel()
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			continue
		}
		var body struct {
			TagName string `json:"tag_name"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&body)
		_ = resp.Body.Close()
		cancel()
		if err == nil && strings.HasPrefix(body.TagName, "v") {
			return body.TagName
		}
	}
	return ""
}

// cliNewer 三段语义比较（与 server 侧同规则；CLI 侧独立小实现避免
// 拉包依赖）。dev/空恒提示。
func cliNewer(current, latest string) bool {
	if latest == "" {
		return false
	}
	if current == "" || current == "dev" || !strings.HasPrefix(current, "v") {
		return true
	}
	c, ok1 := cliSemVer(current)
	l, ok2 := cliSemVer(latest)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func cliSemVer(s string) ([3]int, bool) {
	var out [3]int
	for i, part := range strings.SplitN(strings.TrimPrefix(s, "v"), ".", 3) {
		for _, r := range part {
			if r < '0' || r > '9' {
				return out, false
			}
			out[i] = out[i]*10 + int(r-'0')
		}
	}
	return out, true
}
