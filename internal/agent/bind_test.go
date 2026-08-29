// bind_test.go 覆盖 M5.3 CLI 包装的纯逻辑面：目标→环境变量映射、
// 健康探测、进程拉起（环境注入 + 退出码透传，helper process 技巧）。
package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLITargets(t *testing.T) {
	got := CLITargets()
	want := []string{"claude", "codex", "gemini"}
	if len(got) != len(want) {
		t.Fatalf("CLITargets() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("CLITargets()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTargetEnvMapping(t *testing.T) {
	const base, tok = "http://127.0.0.1:8787", "ofg-test"
	cases := []struct {
		target string
		want   []string
	}{
		{"claude", []string{
			"ANTHROPIC_BASE_URL=" + base,
			"ANTHROPIC_AUTH_TOKEN=" + tok,
		}},
		{"codex", []string{
			"OPENAI_BASE_URL=" + base + "/v1",
			"OPENAI_API_KEY=" + tok,
		}},
		{"gemini", []string{
			"GOOGLE_GEMINI_BASE_URL=" + base,
			"GEMINI_API_KEY=" + tok,
		}},
	}
	for _, tc := range cases {
		got, err := TargetEnv(tc.target, base, tok)
		if err != nil {
			t.Fatalf("TargetEnv(%s): %v", tc.target, err)
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("TargetEnv(%s) = %v, want %v", tc.target, got, tc.want)
		}
	}
	if _, err := TargetEnv("cursor", base, tok); err == nil || !strings.Contains(err.Error(), "unknown CLI target") {
		t.Errorf("TargetEnv(unknown) err = %v, want unknown CLI target", err)
	}
}

func TestProbeGateway(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	if err := ProbeGateway(context.Background(), ok.URL); err != nil {
		t.Fatalf("healthy gateway: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if err := ProbeGateway(context.Background(), bad.URL); err == nil {
		t.Error("500 healthz: want error")
	}

	closed, err := net.Listen("tcp", "127.0.0.1:0") // 取一个空闲端口后立刻关闭
	if err != nil {
		t.Skipf("no free port: %v", err)
	}
	addr := closed.Addr().String()
	closed.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := ProbeGateway(ctx, "http://"+addr); err == nil {
		t.Error("unreachable gateway: want error")
	}
}

// TestBindLaunchHelper 是 LaunchCLI 测试的 helper 分支：验证注入变量
// 可见后写文件并以码 7 退出（仅在 OFD_BIND_HELPER=1 时生效）。
func TestBindLaunchHelper(t *testing.T) {
	if os.Getenv("OFD_BIND_HELPER") != "1" {
		return
	}
	_ = os.WriteFile(os.Getenv("OFD_BIND_OUT"),
		[]byte(os.Getenv("OFD_BIND_PROBE")), 0o644)
	os.Exit(7)
}

func TestLaunchCLIEnvAndExitCode(t *testing.T) {
	out := filepath.Join(t.TempDir(), "probe.txt")
	t.Setenv("OFD_BIND_HELPER", "1")
	t.Setenv("OFD_BIND_OUT", out)
	extra := []string{"OFD_BIND_PROBE=injected-via-bind"}
	code, err := LaunchCLI(os.Args[0], extra, []string{"-test.run=TestBindLaunchHelper"})
	if err != nil {
		t.Fatalf("LaunchCLI: %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("helper output: %v", err)
	}
	if string(got) != "injected-via-bind" {
		t.Errorf("helper saw env %q, want injected-via-bind", got)
	}
}

func TestLaunchCLIMissingTarget(t *testing.T) {
	_, err := LaunchCLI("ofd-no-such-cli-xyz", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "not found in PATH") {
		t.Errorf("missing target err = %v, want 'not found in PATH'", err)
	}
}
