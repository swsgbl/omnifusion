// gateway_test.go 覆盖黑盒环境的网关子进程装配：构建二进制、定位仓库根、
// 启动子进程与取回统一 API key。
package bench_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// prepareArtifacts 创建 temp 目录并从仓库根构建网关二进制；tempdir 后续
// 作为网关子进程 cwd（store 落其下 data/omnifusion.db，天然隔离）。
func prepareArtifacts() (tmp, bin string) {
	var err error
	if tmp, err = os.MkdirTemp("", "ofdbench-*"); err != nil {
		fmt.Fprintf(os.Stderr, "bench: create tempdir: %v\n", err)
		os.Exit(1)
	}
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "bench: %v\n", err)
		os.Exit(1)
	}
	bin = filepath.Join(tmp, "ofd.exe")
	build := exec.Command("go", "build", "-o", bin, "./cmd/ofd")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "bench: go build ./cmd/ofd: %v\n%s\n", err, out)
		os.RemoveAll(tmp)
		os.Exit(1)
	}
	return tmp, bin
}

// repoRoot 从测试 cwd（bench/）向上找 go.mod 定位仓库根。
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		if parent := filepath.Dir(dir); parent != dir {
			dir = parent
			continue
		}
		return "", fmt.Errorf("go.mod not found from %s", dir)
	}
}

// startGateway 以 temp 为 cwd 启动网关子进程（不带 -config，全默认值），
// stdout/stderr 重定向到 temp 下 ofd.log 防管道缓冲死锁；父进程侧日志
// 句柄随手关闭（子进程持有继承句柄，不受影响）。
func startGateway(bin, tmp string) (*exec.Cmd, <-chan struct{}, string, error) {
	logPath := filepath.Join(tmp, "ofd.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, nil, logPath, err
	}
	cmd := exec.Command(bin)
	cmd.Dir = tmp
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, nil, logPath, err
	}
	logFile.Close()
	gwPID = cmd.Process.Pid
	gwLogPath = logPath
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	gwExited = exited
	return cmd, exited, logPath, nil
}

// fetchGatewayKey 执行 `ofd gateway-key` 取网关统一 API key（派生自机器
// 身份、不落盘，与子进程 cwd 无关）。
func fetchGatewayKey(bin string) (string, error) {
	cmd := exec.Command(bin, "gateway-key")
	cmd.Dir = filepath.Dir(bin)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ofd gateway-key: %w", err)
	}
	key := strings.TrimSpace(string(out))
	if !strings.HasPrefix(key, "ofg-") {
		return "", fmt.Errorf("网关 key 格式异常：%q", key)
	}
	return key, nil
}
