// butler_env.go 是管家的内核级环境感知层（借鉴 Codex 的 shell 检测
// + DeepSeek 的动态上下文注入）：网关启动时自动检测 OS、shell、
// 常用工具路径，注入到管家的系统提示和工具结果中——模型永远不需要
// "猜"平台，工具层负责适配。
package server

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// EnvFacts 是启动时一次性检测的环境事实，注入管家系统提示和工具结果。
type EnvFacts struct {
	OS           string   `json:"os"`             // "windows" | "darwin" | "linux"
	Arch         string   `json:"arch"`           // "amd64" | "arm64"
	Shell        string   `json:"shell"`          // 检测到的默认 shell
	ShellPath    string   `json:"shell_path"`     // shell 可执行路径
	HomeDir      string   `json:"home_dir"`       // 用户 home 目录
	HasGit       bool     `json:"has_git"`
	HasNode      bool     `json:"has_node"`
	HasPython    bool     `json:"has_python"`
	HasGo        bool     `json:"has_go"`
	PathExes     []string `json:"path_exes"`      // PATH 上检测到的常见工具
}

// DetectEnv 启动时一次性检测本机环境（Codex 模式：运行时检测，
// 非硬编码）。
func DetectEnv() EnvFacts {
	home, _ := os.UserHomeDir()
	facts := EnvFacts{
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		HomeDir: home,
	}
	// 检测默认 shell。
	switch runtime.GOOS {
	case "windows":
		facts.Shell = "powershell"
		if p, err := exec.LookPath("pwsh.exe"); err == nil {
			facts.ShellPath = p
			facts.Shell = "pwsh"
		} else if p, err := exec.LookPath("powershell.exe"); err == nil {
			facts.ShellPath = p
		} else {
			facts.Shell = "cmd"
			facts.ShellPath, _ = exec.LookPath("cmd.exe")
		}
	case "darwin":
		facts.Shell = "zsh"
		if p, err := exec.LookPath("zsh"); err == nil {
			facts.ShellPath = p
		} else {
			facts.Shell = "bash"
			facts.ShellPath, _ = exec.LookPath("bash")
		}
	default: // linux
		facts.Shell = "bash"
		if p, err := exec.LookPath("bash"); err == nil {
			facts.ShellPath = p
		} else {
			facts.Shell = "sh"
			facts.ShellPath, _ = exec.LookPath("sh")
		}
	}
	// 检测常见工具。
	for _, tool := range []struct {
		name string
		exes []string
	}{
		{"git", []string{"git", "git.exe"}},
		{"node", []string{"node", "node.exe"}},
		{"python", []string{"python3", "python", "py"}},
		{"go", []string{"go", "go.exe"}},
	} {
		for _, exe := range tool.exes {
			if _, err := exec.LookPath(exe); err == nil {
				switch tool.name {
				case "git":
					facts.HasGit = true
				case "node":
					facts.HasNode = true
				case "python":
					facts.HasPython = true
				case "go":
					facts.HasGo = true
				}
				break
			}
		}
	}
	return facts
}

// FormatEnvHint 返回注入管家系统提示的环境上下文片段
//（DeepSeek 的动态上下文注入模式：运行时事实，非硬编码）。
func (f EnvFacts) FormatEnvHint(lang string) string {
	tools := []string{}
	if f.HasGit {
		tools = append(tools, "git")
	}
	if f.HasNode {
		tools = append(tools, "node")
	}
	if f.HasPython {
		tools = append(tools, "python")
	}
	if f.HasGo {
		tools = append(tools, "go")
	}

	osName := map[string]string{"windows": "Windows", "darwin": "macOS", "linux": "Linux"}[f.OS]
	if osName == "" {
		osName = f.OS
	}

	if lang == "zh" {
		return fmt.Sprintf(
			"【运行环境（自动检测）】系统=%s %s，Shell=%s，Home=%s。可用工具：%s。run_command 在此环境执行——Windows 用 dir/type/where/findstr，macOS/Linux 用 ls/cat/which/grep。",
			osName, f.Arch, f.Shell, f.HomeDir, strings.Join(tools, ", "),
		)
	}
	return fmt.Sprintf(
		"[Runtime environment (auto-detected)] OS=%s %s, Shell=%s, Home=%s. Available tools: %s. run_command executes in this environment — use platform-appropriate commands.",
		osName, f.Arch, f.Shell, f.HomeDir, strings.Join(tools, ", "),
	)
}

// TranslateCommand 把通用命令意图翻译成平台原生命令
//（Codex 的 exec args translation 模式：模型说意图，工具层适配）。
// 返回翻译后的命令和是否做了翻译。
func (f EnvFacts) TranslateCommand(cmd string) (string, bool) {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return cmd, false
	}
	// 只翻译已知的 Unix→Windows 映射（仅在 Windows 上）。
	if f.OS != "windows" {
		return cmd, false
	}
exe := strings.ToLower(fields[0])
switch exe {
case "ls":
		args := "/c dir /b"
		if len(fields) > 1 {
			args = "/c dir " + strings.Join(fields[1:], " ")
		}
		return "cmd " + args, true
	case "cat":
		if len(fields) > 1 {
			return "cmd /c type " + strings.Join(fields[1:], " "), true
		}
		return cmd, false
	case "which":
		if len(fields) > 1 {
			return "cmd /c where " + strings.Join(fields[1:], " "), true
		}
		return cmd, false
	case "grep":
		if len(fields) > 1 {
			return "cmd /c findstr " + strings.Join(fields[1:], " "), true
		}
		return cmd, false
	}
	return cmd, false
}
