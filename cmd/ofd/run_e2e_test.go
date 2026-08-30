// run_e2e_test.go 是 CLI 包装的验收：编译真实 ofd 二进制，对
// 健康网关（假 /healthz）注入正确环境变量并拉起 PATH 里的假 CLI（三
// 目标参数化）；另覆盖 run 参数校验、waitHealthy 轮询、spawnDetached
// 后台拉起与 isLoopbackBase 判定。autospawn→真网关全链路走真机冒烟。
package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/swsgbl/omnifusion/internal/config"
)

func TestRunCommandValidation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.Host, cfg.Server.Port = "127.0.0.1", 1 // 不触网：校验先于探测
	if err := runRunCommand(cfg, "", nil); err == nil || !strings.Contains(err.Error(), "missing CLI target") {
		t.Errorf("no args err = %v, want missing CLI target", err)
	}
	if err := runRunCommand(cfg, "", []string{"cursor"}); err == nil || !strings.Contains(err.Error(), "unknown CLI target") {
		t.Errorf("bad target err = %v, want unknown CLI target", err)
	}
}

func TestIsLoopbackBase(t *testing.T) {
	cases := map[string]bool{
		"http://127.0.0.1:8787": true,
		"http://localhost:8787": true,
		"http://[::1]:8787": true,
		"http://192.168.1.5:8787": false,
		"http://gw.example.com": false,
		"://bad": false,
	}
	for base, want := range cases {
		if got := isLoopbackBase(base); got != want {
			t.Errorf("isLoopbackBase(%q) = %v, want %v", base, got, want)
		}
	}
}

func TestWaitHealthy(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	if err := waitHealthy(context.Background(), ok.URL, time.Second); err != nil {
		t.Fatalf("healthy: %v", err)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no free port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	if err := waitHealthy(context.Background(), "http://"+addr, 600*time.Millisecond); err == nil {
		t.Error("unreachable: want timeout error")
	}
}

// TestBindSpawnHelper 是 spawnDetached 测试的 helper 分支：写标记文件
// 后退出（仅在 OFD_RUN_SPAWN_HELPER 指向路径时生效）。
func TestBindSpawnHelper(t *testing.T) {
	if out := os.Getenv("OFD_RUN_SPAWN_HELPER"); out != "" {
		_ = os.WriteFile(out, []byte("spawned"), 0o644)
		os.Exit(0)
	}
}

func TestSpawnDetached(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "spawned")
	t.Setenv("OFD_RUN_SPAWN_HELPER", marker)
	logf := filepath.Join(t.TempDir(), "serve.log")
	if err := spawnDetached(os.Args[0], []string{"-test.run=TestBindSpawnHelper"}, logf); err != nil {
		t.Fatalf("spawnDetached: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			return // 子进程确以后台形态跑过 helper 分支
		}
		if time.Now().After(deadline) {
			t.Fatal("spawned helper did not write marker within 10s")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// writeFakeCLI 在 dir 生成一个名为 target 的假 CLI：把完整环境 dump 到
// $OFD_RUN_OUT 指向的文件（Windows 用 .cmd，其余用 sh 脚本）。
func writeFakeCLI(t *testing.T, dir, target string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// %VAR% 在批处理行内即期展开；set 无参打印全部环境变量。
		script := "@echo off\r\nset > \"%OFD_RUN_OUT%\"\r\nexit /b 0\r\n"
		if err := os.WriteFile(filepath.Join(dir, target+".cmd"), []byte(script), 0o644); err != nil {
			t.Fatalf("write fake %s.cmd: %v", target, err)
		}
		return
	}
	script := "#!/bin/sh\nenv > \"$OFD_RUN_OUT\"\n"
	path := filepath.Join(dir, target)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", target, err)
	}
}

// setEnvOnce 返回 env 的副本并把键 k（大小写不敏感，Windows 语义）
// 替换为 kv，避免环境块里出现重复键。
func setEnvOnce(env []string, kv string) []string {
	key := strings.SplitN(kv, "=", 2)[0]
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if k, _, ok := strings.Cut(e, "="); ok && strings.EqualFold(k, key) {
			continue
		}
		out = append(out, e)
	}
	return append(out, kv)
}

// TestRunE2E 三目标参数化：ofd run <target> 对健康网关注入目标专属
// 变量并拉起假 CLI（参数透传、退出码 0）。
func TestRunE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the real ofd binary")
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer gw.Close()
	bin := buildOfd(t)

	cases := []struct{ target, key, wantVal string }{
		{"claude", "ANTHROPIC_BASE_URL", gw.URL},
		{"claude", "ANTHROPIC_AUTH_TOKEN", "ofg-run-e2e"},
		{"codex", "OPENAI_BASE_URL", gw.URL + "/v1"},
		{"codex", "OPENAI_API_KEY", "ofg-run-e2e"},
		{"gemini", "GOOGLE_GEMINI_BASE_URL", gw.URL},
		{"gemini", "GEMINI_API_KEY", "ofg-run-e2e"},
	}
	for _, tc := range cases {
		t.Run(tc.target+"/"+tc.key, func(t *testing.T) {
			cliDir := t.TempDir()
			writeFakeCLI(t, cliDir, tc.target)
			out := filepath.Join(t.TempDir(), "env.txt")

			cmd := exec.Command(bin, "run", "-gateway-url", gw.URL, "-token", "ofg-run-e2e", tc.target)
			cmd.Env = setEnvOnce(os.Environ(),
				"PATH="+cliDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cmd.Env = setEnvOnce(cmd.Env, "OFD_RUN_OUT="+out)
			if got, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("ofd run %s: %v\n%s", tc.target, err, got)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("fake CLI env dump: %v", err)
			}
			if !strings.Contains(string(data), tc.key+"="+tc.wantVal) {
				t.Errorf("fake %s env missing %s=%s\ngot:\n%s", tc.target, tc.key, tc.wantVal, data)
			}
		})
	}
}
