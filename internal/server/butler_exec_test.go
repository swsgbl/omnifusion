package server

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
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
	out := s.runCommand([]string{goPath, "version"}, time.Minute, 0, 0)
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

// TestTruncateOutputHeadTail 超长输出保头保尾、行数标注、默认值正确。
func TestTruncateOutputHeadTail(t *testing.T) {
	small := strings.Repeat("line\n", 10)
	if got := truncateOutput(small, 0, 0); got != small {
		t.Errorf("small output must pass through, got %d chars", len(got))
	}
	lines := make([]string, 500)
	for i := range lines {
		lines[i] = fmt.Sprintf("%04d-", i) + strings.Repeat("x", 60) // 每行足够长以触发总量超限
	}
	big := strings.Join(lines, "\n")
	got := truncateOutput(big, 2, 3)
	if !strings.Contains(got, "0001-") || !strings.Contains(got, "0499-") {
		t.Error("head/tail not preserved")
	}
	if !strings.Contains(got, "省略") {
		t.Error("ellipsis marker missing")
	}
	if strings.Contains(got, "0250-") {
		t.Error("middle lines must be dropped")
	}
	// 默认 50/300。
	got = truncateOutput(big, 0, 0)
	if !strings.Contains(got, "0049-") || !strings.Contains(got, "0499-") {
		t.Error("default head/tail wrong")
	}
}

// TestClampWait 等待秒数夹取：0/负数回默认 60，超 300 夹 300。
func TestClampWait(t *testing.T) {
	if got := clampWait(0); got != 60*time.Second {
		t.Errorf("0 -> %v, want 60s", got)
	}
	if got := clampWait(-5); got != 60*time.Second {
		t.Errorf("-5 -> %v, want 60s", got)
	}
	if got := clampWait(5); got != 5*time.Second {
		t.Errorf("5 -> %v, want 5s", got)
	}
	if got := clampWait(9999); got != 300*time.Second {
		t.Errorf("9999 -> %v, want 300s", got)
	}
}

// TestBackgroundLifecycle 后台任务全生命周期：启动→输出进缓冲→完成态
// 可观察；完成前单槽拒绝第二个后台。
func TestBackgroundLifecycle(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not in PATH")
	}
	s := &Server{}
	// 清场：单槽全局，测试前确保空闲。
	bgMu.Lock()
	if bgCur != nil && !bgCur.done {
		bgCur.cancel()
	}
	bgCur = nil
	bgMu.Unlock()

	id, ok := s.startBackground([]string{goPath, "version"}, "go version")
	if !ok {
		t.Fatal("startBackground refused")
	}
	if id == "" {
		t.Fatal("empty id")
	}
	// go version 毫秒级退出；轮询至完成态。
	alive := true
	var output string
	for i := 0; i < 50 && alive; i++ {
		bgMu.Lock()
		task := bgCur
		bgMu.Unlock()
		if task == nil {
			t.Fatal("task vanished")
		}
		task.mu.Lock()
		alive = !task.done
		output += decodePlatform(task.buf.Bytes())
		task.buf.Reset()
		task.mu.Unlock()
		if alive {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if alive {
		t.Error("background task never completed")
	}
	if !strings.Contains(output, "go version") {
		t.Errorf("output = %q", output)
	}
	// 完成后的任务不再是活跃槽：新后台可再启动。
	time.Sleep(50 * time.Millisecond)
	if _, ok2 := s.startBackground([]string{goPath, "version"}, "again"); !ok2 {
		t.Error("slot still busy after task done")
	}
	bgMu.Lock()
	if bgCur != nil && !bgCur.done {
		bgCur.cancel()
	}
	bgMu.Unlock()
}
