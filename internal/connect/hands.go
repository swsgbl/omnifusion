// hands.go 给管家两只"通用手"：在电脑里找到任何 AI 工具的配置
// （FindTool），读懂配置结构（ReadFile）后把接入三要素写进去
// （WriteFile，写前自动备份）。五家内置 CLI 走 writers.go 的确定性
// 写入器；这两只手覆盖其余一切工具——管家（LLM）只编排，动作本身是
// 确定性 Go 代码。
//
// 安全边界：① 只允许用户 home 目录内的路径（相对路径按 home 解析，
// symlink 解析后复核，防逃逸）；② 只允许文本（NUL 字节守卫）；
// ③ 大小限额（读 256KB / 写 64KB）；④ 覆盖前自动备份；⑤ 不新建目录。
package connect

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	readCap       = 256 << 10 // 单次读取上限
	writeCap      = 64 << 10  // 单次写入上限
	dirPreviewCap = 12        // 目录命中预览的顶层条目数
	sweepCap      = 30        // 无名扫描的返回上限
)

// FileRow 是目录命中预览里的一条顶层条目。
type FileRow struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"is_dir"`
}

// FsHit 是 FindTool 的一条命中：PATH 上的可执行、候选配置目录
//（附顶层预览）或单个文件。
type FsHit struct {
	Kind  string    `json:"kind"` // "exe" | "dir" | "file"
	Path  string    `json:"path"`
	Size  int64     `json:"size,omitempty"`
	Files []FileRow `json:"files,omitempty"`
}

// aiToolHints 是无名扫描时判定"这个目录像 AI 工具"的关键词
//（子串匹配目录名）。只是发现辅助，命中后仍由管家读配置确认。
var aiToolHints = []string{
	"claude", "codex", "gemini", "opencode", "pi", "cursor", "aider",
	"cline", "continue", "copilot", "zed", "windsurf", "trae", "qwen",
	"droid", "crush", "goose", "amp", "kilo", "roo", "codeium",
	"tabnine", "hmharness", "llm", "ai",
}

// FindTool 在本机找名为 name 的 AI 工具：PATH 上的可执行 + home 点
// 目录 + ~/.config + AppData（Roaming/Local）。name 为空时做一轮
// 无名扫描（按 aiToolHints 过滤，判断"像 AI 工具"的目录）。
func FindTool(name string) []FsHit {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	name = strings.ToLower(strings.TrimSpace(name))
	var hits []FsHit
	seen := map[string]bool{}
	add := func(h FsHit) {
		key := strings.ToLower(h.Path)
		if seen[key] || len(hits) >= 40 {
			return
		}
		seen[key] = true
		hits = append(hits, h)
	}

	if name == "" {
		// 无名扫描：home 点目录 + ~/.config/* + AppData/*/* 里像
		// AI 工具的目录（数量多时截断，宁缺勿滥）。
		candidates := []string{}
		candidates = append(candidates, dotDirs(home)...)
		candidates = append(candidates, globDirs(filepath.Join(home, ".config"))...)
		candidates = append(candidates, globDirs(filepath.Join(home, "AppData", "Roaming"))...)
		candidates = append(candidates, globDirs(filepath.Join(home, "AppData", "Local"))...)
		for _, dir := range candidates {
			base := strings.ToLower(filepath.Base(dir))
			if !containsAny(base, aiToolHints) {
				continue
			}
			add(dirHit(dir))
			if len(hits) >= sweepCap {
				break
			}
		}
		return hits
	}

	// ① PATH 可执行（name / name.exe / .cmd / .bat / .ps1）。
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, ext := range pathExts() {
			p := filepath.Join(dir, name+ext)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				add(FsHit{Kind: "exe", Path: p, Size: fi.Size()})
			}
		}
	}
	// ② home 下的点目录 / 无点目录（~/.hmharness、~/hmharness）。
	// ③ ~/.config、AppData Roaming/Local 下含 name 的目录（子串匹配，
	// 厂商目录名常有变体：hmharness / HMHarness / hm-harness）。
	searchRoots := []string{home, filepath.Join(home, ".config"),
		filepath.Join(home, "AppData", "Roaming"), filepath.Join(home, "AppData", "Local")}
	merged := map[string]bool{}
	for _, root := range searchRoots {
		for _, dir := range globDirs(root) {
			merged[dir] = true
		}
	}
	for dir := range merged {
		base := strings.ToLower(filepath.Base(dir))
		if base == name || strings.Contains(base, name) || matchLoose(base, name) {
			add(dirHit(dir))
		}
	}
	return hits
}

// dirHit 构造目录命中并附顶层条目预览（管家据此挑配置文件去读）。
func dirHit(dir string) FsHit {
	h := FsHit{Kind: "dir", Path: dir}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			if len(h.Files) >= dirPreviewCap {
				h.Files = append(h.Files, FileRow{Name: "…"})
				break
			}
			row := FileRow{Name: e.Name(), IsDir: e.IsDir()}
			if !e.IsDir() {
				if fi, err := e.Info(); err == nil {
					row.Size = fi.Size()
				}
			}
			h.Files = append(h.Files, row)
		}
	}
	return h
}

// dotDirs 返回 home 下的点目录（.claude 这类约定俗成的工具配置位）。
func dotDirs(home string) []string {
	ents, err := os.ReadDir(home)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, e := range ents {
		if e.IsDir() && strings.HasPrefix(e.Name(), ".") && e.Name() != "." && e.Name() != ".." {
			out = append(out, filepath.Join(home, e.Name()))
		}
	}
	return out
}

// globDirs 返回目录的一层子目录列表（不存在/不可读返回空）。
func globDirs(root string) []string {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, filepath.Join(root, e.Name()))
		}
	}
	return out
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// matchLoose 宽松匹配：忽略 -/_/空白差异（hm-harness ↔ hmharness）。
func matchLoose(base, name string) bool {
	norm := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(s, "-", ""), "_", "")
	}
	return strings.Contains(norm(base), norm(name))
}

func pathExts() []string {
	if runtime.GOOS == "windows" {
		return []string{"", ".exe", ".cmd", ".bat", ".ps1"}
	}
	return []string{""}
}

// resolveHomePath 把管家给的路径约束在 home 目录内：相对路径按 home
// 解析；父目录 symlink 解析后复核前缀，防 ".." 与链接逃逸。返回绝对
// 路径；越界报错。
func resolveHomePath(home, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(home, abs)
	}
	abs = filepath.Clean(abs)
	homeAbs, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	// 父目录（通常已存在）解析真实路径再复核边界。
	base := filepath.Base(abs)
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		parent = filepath.Dir(abs) // 父目录不存在时按字面检查（写入端会再拒绝）
	}
	if !underDir(homeAbs, parent) {
		return "", fmt.Errorf("path %s is outside home directory %s（安全边界：管家只动 home 内的配置）", abs, homeAbs)
	}
	out := filepath.Join(parent, base)
	if !underDir(homeAbs, out) {
		return "", fmt.Errorf("path %s is outside home directory %s", abs, homeAbs)
	}
	return out, nil
}

// underDir 报告 path 是否等于或在 dir 内（Windows 大小写不敏感）。
func underDir(dir, path string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(path, dir) || strings.HasPrefix(strings.ToLower(path), strings.ToLower(dir)+string(filepath.Separator))
	}
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

// ReadFile 读取 home 内的一个文本配置文件（二进制守卫 + 大小上限），
// 让管家先读懂结构再动笔。
func ReadFile(path string) (map[string]any, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	abs, err := resolveHomePath(home, path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", abs, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s 是目录；请读其中的具体配置文件", abs)
	}
	if fi.Size() > readCap {
		return nil, fmt.Errorf("%s 有 %d 字节，超过读取上限 %d（可能不是配置文件）", abs, fi.Size(), readCap)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return nil, fmt.Errorf("%s 是二进制文件，不读", abs)
	}
	return map[string]any{"path": abs, "size": fi.Size(), "content": string(b)}, nil
}

// PatchOp 是 patch-config 的一条改动：dotted path 定位 JSON 内的字段
//（如 "llm.apiBase"），Remove=true 删除该键，否则写 Value。
type PatchOp struct {
	Path   string `json:"path"`
	Value  any    `json:"value,omitempty"`
	Remove bool   `json:"remove,omitempty"`
}

// PatchConfig 对 home 内的一个 JSON 配置做确定性点补丁：只传改动点，
// 模型输出从"整文件重写"缩到几百 token——大配置不再撑爆 max_tokens
//（v0.1.6 实测 hmharness 4KB 配置整写在 6000 token 仍被截断）。合并
// 由代码完成，用户其余字段机制性保证不动；覆盖前自动备份。
// 仅支持 JSON 对象 + 点分键路径（键名含点或数组下标请用 write_config
// 整文件）；非 JSON 文件报错引导走 write_config。
func PatchConfig(path string, ops []PatchOp) (map[string]any, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	abs, err := resolveHomePath(home, path)
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("no patch ops given")
	}
	if fi, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("stat %s: %w（patch 只改已存在的配置；新文件用 write_config）", abs, err)
	} else if fi.IsDir() {
		return nil, fmt.Errorf("%s 是目录", abs)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	if len(raw) > readCap {
		return nil, fmt.Errorf("%s 有 %d 字节，超过补丁上限 %d", abs, len(raw), readCap)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s 不是 JSON 对象（%v）——点补丁只支持 JSON；请改用 write_config 整文件", abs, err)
	}
	for _, op := range ops {
		if err := applyPatchOp(m, op); err != nil {
			return nil, fmt.Errorf("op %q: %w", op.Path, err)
		}
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	bak := backupIfExists(abs)
	if err := os.WriteFile(abs, append(out, '\n'), 0o600); err != nil {
		return nil, err
	}
	return map[string]any{"path": abs, "applied": len(ops), "result": "已补丁" + bakNote(bak)}, nil
}

// applyPatchOp 按 dotted path 走 map 层级应用一条改动（中间层缺失则
// 创建；中途撞到非 map 节点报错）。
func applyPatchOp(root map[string]any, op PatchOp) error {
	parts := strings.Split(op.Path, ".")
	cur := root
	for i, p := range parts {
		if p == "" {
			return fmt.Errorf("empty path segment")
		}
		if i == len(parts)-1 {
			if op.Remove {
				delete(cur, p)
			} else {
				cur[p] = op.Value
			}
			return nil
		}
		next, ok := cur[p].(map[string]any)
		if !ok {
			if _, exists := cur[p]; exists {
				return fmt.Errorf("%q is not an object", strings.Join(parts[:i+1], "."))
			}
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	return nil
}

// WriteFile 把内容写入 home 内的一个配置文件：文件可以在已存在的
// 目录里新建，但覆盖前自动备份；不新建目录（目录不存在说明管家没找
// 到工具的配置位，应回问用户而不是乱建）。整文件形态，适合非 JSON
// 配置或结构全新的文件；JSON 已有配置优先 PatchConfig 点补丁。
func WriteFile(path, content string) (map[string]any, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	abs, err := resolveHomePath(home, path)
	if err != nil {
		return nil, err
	}
	if len(content) > writeCap {
		return nil, fmt.Errorf("content %d 字节超过写入上限 %d", len(content), writeCap)
	}
	// 空内容守卫：模型输出异常（截断/参数丢失）时宁可不落盘——写空
	// 等于清空用户配置，备份也救不回这一步的误操作。
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("refusing to write empty content to %s（空内容会清空配置；输出异常应重试）", abs)
	}
	dir := filepath.Dir(abs)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("目录 %s 不存在；先确认工具配置位置，不要新建目录", dir)
	}
	if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
		return nil, fmt.Errorf("%s 是目录，不能当文件写", abs)
	}
	bak := backupIfExists(abs)
	if err := os.WriteFile(abs, []byte(content), 0o600); err != nil {
		return nil, err
	}
	return map[string]any{"path": abs, "result": "已写入" + bakNote(bak)}, nil
}

// EditFile 对 home 内一个文本文件做唯一匹配替换：old 必须恰好出现一次
//（0 次报未找到；多次报不唯一，模型须带上更多上下文重试）——非 JSON
// 文件（YAML/TOML/Markdown/.env）的精确改动形态，改动量最小、用户
// 其余内容一字不动。守卫与读/写同套：home 内、文本、限额、备份先行。
func EditFile(path, oldStr, newStr string) (map[string]any, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if oldStr == "" {
		return nil, fmt.Errorf("old_string is empty（空串会匹配任意位置；请给出要替换的原文）")
	}
	abs, err := resolveHomePath(home, path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", abs, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s 是目录", abs)
	}
	if fi.Size() > readCap {
		return nil, fmt.Errorf("%s 有 %d 字节，超过编辑上限 %d", abs, fi.Size(), readCap)
	}
	raw, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(raw, 0) >= 0 {
		return nil, fmt.Errorf("%s 是二进制文件，不编辑", abs)
	}
	n := strings.Count(string(raw), oldStr)
	switch n {
	case 0:
		return nil, fmt.Errorf("old_string 在 %s 中未找到（先 read_file 看当前内容）", abs)
	case 1:
		// 唯一命中：执行替换。
	default:
		return nil, fmt.Errorf("old_string 在 %s 中出现 %d 次，不唯一（带上前后行扩大上下文后重试）", abs, n)
	}
	out := strings.Replace(string(raw), oldStr, newStr, 1)
	if len(out) > writeCap {
		return nil, fmt.Errorf("结果 %d 字节超过写入上限 %d", len(out), writeCap)
	}
	bak := backupIfExists(abs)
	if err := os.WriteFile(abs, []byte(out), 0o600); err != nil {
		return nil, err
	}
	return map[string]any{"path": abs, "result": "已替换 1 处" + bakNote(bak)}, nil
}
