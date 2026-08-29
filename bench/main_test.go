// 黑盒环境自举：TestMain 负责 build 网关二进制、起进程内 mock 上游、
// 拉起网关子进程并轮询等待模型目录同步；测试结束 Kill+Wait 回收子进程
// 并清理 temp 目录（防孤儿进程）。端口被占等软性失败写入 skipReason，
// 由各用例统一 t.Skip；build 失败属硬错误，直接非零退出。
package bench_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// 地址全部写死默认值：ollama provider 硬编码 http://localhost:11434/v1，
// 网关不带 -config 时监听 127.0.0.1:20130、store 落 cwd 下 data/。
const (
	benchModel   = "bench-model"     // mock 上游暴露的模型 id
	benchSession = "bench-session"   // 固定会话 id：sticky 绑定 mock 上游
	upstreamAddr = "127.0.0.1:11434" // mock 上游监听（ollama 位置）
	gatewayAddr  = "127.0.0.1:20130" // 网关默认监听
	catalogWait  = 60 * time.Second  // 模型目录同步轮询上限
)

// 黑盒共享环境。benchClient 的连接池按 1000 并发压测放足。
var (
	gatewayURL  string
	gwAPIKey    string
	gwPID       int
	gwLogPath   string
	gwExited    <-chan struct{}
	skipReason  string
	benchClient = &http.Client{
		Transport: &http.Transport{
			MaxIdleConnsPerHost: 1200,
			IdleConnTimeout:     90 * time.Second,
		},
	}
)

// TestMain：搭建环境 → 跑用例 → 无条件回收（子进程、mock、temp 目录）。
func TestMain(m *testing.M) {
	cleanup := startBenchEnv()
	if skipReason != "" {
		fmt.Fprintf(os.Stderr, "bench: 环境不可用，全部用例跳过：%s\n", skipReason)
	}
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// startBenchEnv 搭建黑盒环境，返回清理函数。
func startBenchEnv() func() {
	tmp, bin := prepareArtifacts()
	upLn, err := listenWithRetry(upstreamAddr)
	if err != nil {
		skipReason = "mock 上游端口被占（" + err.Error() + "）；bench 需独占 127.0.0.1:11434（ollama provider 硬编码上游），请先停掉占用者（如 ollama）"
		return func() { os.RemoveAll(tmp) }
	}
	up := startMockUpstream(upLn)
	if err := probeWithRetry(gatewayAddr); err != nil {
		skipReason = "网关端口被占（" + err.Error() + "）；bench 需独占 127.0.0.1:20130（网关默认监听），请先停掉占用者"
		up.Close()
		return func() { os.RemoveAll(tmp) }
	}
	gwCmd, exited, logPath, err := startGateway(bin, tmp)
	if err != nil {
		skipReason = "网关子进程启动失败：" + err.Error()
		up.Close()
		return func() { os.RemoveAll(tmp) }
	}
	// gatewayURL/gwAPIKey 先行就位：waitCatalog 与会话绑定预热都要用它们
	// 发请求（后续失败由 skipReason 接管，用例不会再真正发请求）。
	key, envErr := fetchGatewayKey(bin)
	if envErr == nil {
		gatewayURL, gwAPIKey = "http://"+gatewayAddr, key
		envErr = waitCatalog(gatewayURL, key, benchModel, exited)
	}
	if envErr == nil {
		envErr = warmSessionBinding()
	}
	if envErr != nil {
		skipReason = fmt.Sprintf("黑盒环境未就绪：%v\n网关日志尾部：\n%s", envErr, tailFile(logPath, 1024))
	}
	return func() {
		if gwCmd.Process != nil {
			_ = gwCmd.Process.Kill()
			<-exited // 等 Wait 返回：防孤儿进程与 Windows 文件锁
		}
		up.Close()
		os.RemoveAll(tmp)
	}
}

// listenWithRetry 绑定并持有 listener（供 mock 上游使用）。Windows 上
// 前一轮压测遗留的 TIME_WAIT（网关→上游连接，约 60s 过期）会让立即重绑
// 失败，故有界重试；超窗仍失败视为端口被真实占用。
func listenWithRetry(addr string) (net.Listener, error) {
	var lastErr error
	for i := 0; i < 16; i++ {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, nil
		}
		lastErr = err
		// 能拨通说明有真实监听者（如 Docker 端口转发/已装的 ollama），
		// 重试无意义，立即失败交由调用方 skip。
		if portHasListener(addr) {
			return nil, lastErr
		}
		time.Sleep(5 * time.Second)
	}
	return nil, lastErr
}

// portHasListener 探测端口上是否有进程在监听（TCP 可建连即视为有）。
func portHasListener(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err == nil {
		conn.Close()
		return true
	}
	return false
}

// probeWithRetry 探测端口可绑定即释放（紧随其后的网关子进程会真正占用）。
func probeWithRetry(addr string) error {
	ln, err := listenWithRetry(addr)
	if err != nil {
		return err
	}
	return ln.Close()
}

// waitCatalog 轮询 GET /v1/models（带 Bearer key）直到目标模型出现。
// 模型目录由网关后台任务异步同步，只等 healthz 不够；子进程提前退出则
// 立即报错不再空等。
func waitCatalog(base, key, model string, exited <-chan struct{}) error {
	deadline := time.Now().Add(catalogWait)
	lastErr := fmt.Errorf("尚未发起探测")
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return fmt.Errorf("网关子进程提前退出，最后探测错误：%w", lastErr)
		default:
		}
		req, err := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := benchClient.Do(req)
		if err != nil {
			lastErr = err
		} else {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(body), model) {
				return nil
			}
			lastErr = fmt.Errorf("/v1/models 状态 %d，响应 %.200s", resp.StatusCode, body)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("等待模型目录同步超时（%s）：%w", catalogWait, lastErr)
}

// warmSessionBinding 用固定会话头发一个非流式请求，把 sticky 会话绑到
// mock 上游（ollama）。模型成员过滤（modelfilter.go）生效后 bench-model
// 的候选已收敛到 ollama 一家，首请求不再吃不可达外部层的上游超时回退；
// 预热保留作确定性兜底（目录首轮同步完成前无快照家会被保守保留），绑定
// 后（30 分钟滑动续期）基准请求始终直达 mock 上游。绑定失败视为环境不可用。
func warmSessionBinding() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	resp, err := doChatCtx(ctx, chatBody("会话绑定预热", false))
	if err != nil {
		return fmt.Errorf("会话绑定预热请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("会话绑定预热状态 %d", resp.StatusCode)
	}
	return nil
}

// tailFile 读文件末尾至多 n 字节，用于把网关日志尾部带进 skip 提示。
func tailFile(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Sprintf("(无法读取 %s: %v)", path, err)
	}
	defer f.Close()
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil || size == 0 {
		return "(空日志)"
	}
	if size > int64(n) {
		_, _ = f.Seek(-int64(n), io.SeekEnd)
	}
	buf, _ := io.ReadAll(f)
	return string(buf)
}

// skipIfUnavailable 让单个用例在环境不可用时统一跳过（带原因与处置提示）。
func skipIfUnavailable(tb testing.TB) {
	tb.Helper()
	if skipReason != "" {
		tb.Skip(skipReason)
	}
}
