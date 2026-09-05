// butler_exec.go 是管家的"有边界执行手"（run_command）：替代裸 Bash 的
// 受约束形态。三档裁定：① 白名单精确形态（版本/进程/定位查询）直接
// 执行；② 形态安全但不在白名单的程序 → 需用户在对话页一键批准；
// ③ 硬拒（shell/解释器/网络工具/系统变更类程序，以及任何含 shell
// 元字符的参数）。
//
// 结构性安全（不依赖提示词）：exec 直接构造参数数组、绝不经过 shell
// ——管道/链式/注入按构造不可能；每参数字符集白名单过滤；20s 超时；
// 输出 16KB 截断；工作目录钉在 home。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

const (
	execTimeout   = 20 * time.Second
	execOutCap    = 16 << 10 // 返回给模型的输出上限
	execMaxFields = 16       // 程序+参数段数上限
	execMaxArgLen = 64       // 单参数长度上限
)

// execDecision 是三档裁定的结果。
type execDecision int

const (
	execAuto     execDecision = iota // 白名单命中：直接执行
	execApproval                     // 形态安全：需用户批准
	execRefuse                       // 硬拒
)

// tierAPatterns 白名单精确形态：段逐字匹配，"<param>" 槽位接受受字符
// 集约束的参数。只收只读、无网络、无副作用的查询类命令。
var tierAPatterns = [][]string{
	{"ofd", "status"},
	{"ofd", "gateway-key"},
	{"ofd", "--version"},
	{"node", "-v"}, {"node", "--version"},
	{"npm", "-v"}, {"npm", "--version"},
	{"python", "--version"}, {"python", "-v"}, {"python", "-V"},
	{"py", "--version"},
	{"git", "--version"},
	{"go", "version"},
	{"tasklist"},
	{"where", "<param>"},
	{"which", "<param>"},
}

// deniedPrograms 硬拒程序集（小写、Windows 去 .exe）：shell 与脚本宿主
// （注入面）、网络客户端（外传/下载）、系统变更与凭据面。解释器一律
// 硬拒——白名单里的精确版本查询形态除外（tierA 先于本表判定）。
var deniedPrograms = map[string]bool{
	// shell / 脚本宿主
	"cmd": true, "powershell": true, "pwsh": true, "bash": true, "sh": true,
	"zsh": true, "wsl": true, "cscript": true, "wscript": true, "mshta": true,
	"node": true, "deno": true, "bun": true,
	"python": true, "python3": true, "py": true,
	"pip": true, "pip3": true, "npm": true, "npx": true, "yarn": true, "pnpm": true,
	"cargo": true, "rustc": true, "gcc": true, "go": true, "java": true, "javaw": true,
	// 网络客户端（数据外传/下载面）
	"curl": true, "wget": true, "ftp": true, "telnet": true, "ssh": true,
	"scp": true, "certutil": true, "bitsadmin": true,
	// 系统变更 / 凭据 / 计划任务
	"reg": true, "regedit": true, "regsvr32": true, "setx": true,
	"schtasks": true, "sc": true, "net": true, "netsh": true, "wmic": true,
	"taskkill": true, "shutdown": true, "runas": true, "psexec": true,
	"msiexec": true, "rundll32": true, "installutil": true,
	// 文件破坏
	"del": true, "erase": true, "rd": true, "rmdir": true, "rm": true,
	"format": true, "diskpart": true, "cipher": true,
	// 版本管理（可执行任意钩子脚本）——仅 --version 白名单形态可用
	"git": true,
}

// argCharsetOK 参数字符集白名单：字母数字与常见无害符号。拒绝引号、
// 管道、重定向、命令替换等一切 shell 语义字符与空白。
func argCharsetOK(s string) bool {
	if s == "" || len(s) > execMaxArgLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == ':' || r == '/' || r == '\\':
		case r == '=' || r == '+' || r == ',' || r == '@' || r == '%' || r == '~':
		default:
			return false
		}
	}
	return true
}

// classifyCommand 对分段后的命令做三档裁定。fields[0] 为程序名。
func classifyCommand(fields []string) (execDecision, string) {
	if len(fields) == 0 || len(fields) > execMaxFields {
		return execRefuse, "empty or too many arguments"
	}
	// 参数字符集先过滤（结构性边界，适用于所有档位）。
	for _, a := range fields[1:] {
		if !argCharsetOK(a) {
			return execRefuse, fmt.Sprintf("argument %q contains characters outside the allowed set (no shell syntax)", a)
		}
	}
	prog := normalizeProgram(fields[0])
	// ① 白名单精确形态。
	for _, pat := range tierAPatterns {
		if matchTierA(pat, prog, fields[1:]) {
			return execAuto, ""
		}
	}
	// ③ 硬拒程序。
	if deniedPrograms[prog] {
		return execRefuse, fmt.Sprintf("program %q is not allowed (shells, interpreters, network clients and system-changing tools are refused)", fields[0])
	}
	// 程序名本身也要过字符集（防奇怪路径形态）。
	if !argCharsetOK(prog) || strings.ContainsAny(prog, "/\\") {
		return execRefuse, "program name invalid"
	}
	// ② 其余形态安全命令：需批准。
	return execApproval, ""
}

// matchTierA 白名单形态匹配：程序段小写相等，参数段逐字相等，
// "<param>" 槽位接受受约束参数。
func matchTierA(pat []string, prog string, args []string) bool {
	if len(pat) != len(args)+1 || normalizeProgram(pat[0]) != prog {
		return false
	}
	for i, want := range pat[1:] {
		if want == "<param>" {
			if !argCharsetOK(args[i]) {
				return false
			}
			continue
		}
		if args[i] != want {
			return false
		}
	}
	return true
}

// normalizeProgram 归一化程序名：小写 + Windows 去 .exe 后缀。
func normalizeProgram(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if runtime.GOOS == "windows" && strings.HasSuffix(p, ".exe") {
		p = strings.TrimSuffix(p, ".exe")
	}
	return p
}

// butlerRunRequest 是 run-command 端点请求体：approved=true 表示用户
// 已在对话页点过允许（同源 ?key= 鉴权体系内的信任传递）。
type butlerRunRequest struct {
	Command  string `json:"command"`
	Approved bool   `json:"approved"`
}

// handleButlerRunCommand 执行 run_command 裁定与运行。
func (s *Server) handleButlerRunCommand(w http.ResponseWriter, r *http.Request) {
	var req butlerRunRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error(), "invalid_request_error", "")
		return
	}
	cmd := strings.TrimSpace(req.Command)
	if cmd == "" || strings.ContainsAny(cmd, "\n\r") || len(cmd) > 256 {
		writeAPIError(w, http.StatusBadRequest, "command must be a single line under 256 chars", "invalid_request_error", "")
		return
	}
	fields := strings.Fields(cmd)
	decision, reason := classifyCommand(fields)
	switch decision {
	case execRefuse:
		writeAPIError(w, http.StatusBadRequest, reason, "invalid_request_error", "")
		return
	case execApproval:
		if !req.Approved {
			// 不执行：交对话页弹批准气泡，用户允许后带 approved 重发。
			writeJSON(w, http.StatusOK, map[string]any{"approval_required": true, "command": cmd})
			return
		}
	}

	out := s.runCommand(fields)
	if s.log != nil {
		s.log.Info("butler exec", "cmd", cmd, "approved", decision == execApproval, "exit", out["exit_code"], "timed_out", out["timed_out"])
	}
	writeJSON(w, http.StatusOK, out)
}

// runCommand 实际执行：无 shell 直 exec、超时杀、输出截断、cwd=home。
func (s *Server) runCommand(fields []string) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	home, _ := os.UserHomeDir()
	c := exec.CommandContext(ctx, fields[0], fields[1:]...)
	c.Dir = home
	raw, err := c.CombinedOutput()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
	// Windows 系统命令（tasklist/netstat 等）输出走 OEM 码页（中文系统
	// =GBK）：非 UTF-8 字节按 GBK 解码，中文输出对模型可读。
	if runtime.GOOS == "windows" && !utf8.Valid(raw) {
		if dec, derr := simplifiedchinese.GBK.NewDecoder().Bytes(raw); derr == nil {
			raw = dec
		}
	}
	if len(raw) > execOutCap {
		raw = append(raw[:execOutCap], []byte("\n…(输出截断)")...)
	}
	exitCode := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	} else if err != nil && !timedOut {
		return map[string]any{"command": strings.Join(fields, " "), "error": fmt.Sprintf("spawn failed: %v", err), "output": string(raw)}
	}
	return map[string]any{
		"command":   strings.Join(fields, " "),
		"exit_code": exitCode,
		"timed_out": timedOut,
		"output":    string(raw),
	}
}
