package server

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"
)

func hashOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// TestInlineScriptHashes 覆盖内联脚本提取的边界：带 src 的不算、标签名
// 不分大小写、正文里有 "<scr" 拼接不误判、空正文也出哈希。
func TestInlineScriptHashes(t *testing.T) {
	inlineA := "\n'use strict';\nconst a = 1 < 2 && \"x\";\n"
	inlineB := "document.title = '</scr' + 'ipt>';\n"
	html := "<html><head>" +
		`<script src="assets/gsap.min.js" defer></script>` +
		"<script>" + inlineA + "</script>" +
		`<script type="module">` + inlineB + "</SCRIPT>" +
		`<SCRIPT SRC=x.js></script>` +
		`<script data-src="no"></script>` +
		"</head><body><p>not &lt;script&gt; text</p></body></html>"
	got := inlineScriptHashes([]byte(html))
	want := []string{hashOf(inlineA), hashOf(inlineB), hashOf("")}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("hashes = %v, want %v", got, want)
	}
	if n := len(inlineScriptHashes([]byte("<p>no scripts</p>"))); n != 0 {
		t.Fatalf("no-script html yielded %d hashes", n)
	}
}

// TestInlineScriptHashesNormalizeNewlines 锁住浏览器口径：脚本哈希按 HTML
// 预处理后的文本算，CRLF / CR 一律视同 LF。Windows 检出（autocrlf）的
// 内嵌页面是 CRLF——不归一的话哈希对不上，Windows 构建上整页脚本被拦。
func TestInlineScriptHashesNormalizeNewlines(t *testing.T) {
	const lf = "<script>\n'use strict';\nconst a = 1;\n</script>"
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	cr := strings.ReplaceAll(lf, "\n", "\r")
	want := []string{hashOf("\n'use strict';\nconst a = 1;\n")}
	for name, html := range map[string]string{"LF": lf, "CRLF": crlf, "CR": cr} {
		if got := inlineScriptHashes([]byte(html)); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s document: hashes = %v, want %v", name, got, want)
		}
	}
}

func TestContentSecurityPolicyDirectives(t *testing.T) {
	p := contentSecurityPolicy([]byte("<script>1</script>"))
	for _, want := range []string{
		"default-src 'self'", "script-src 'self' " + hashOf("1"), "style-src 'self' 'unsafe-inline'",
		"object-src 'none'", "base-uri 'none'", "form-action 'self'", "connect-src 'self'",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("policy missing %q:\n%s", want, p)
		}
	}
	// 脚本面绝不允许内联/eval 兜底：那会让哈希失去意义。
	if strings.Contains(strings.Split(p, ";")[1], "unsafe-inline") || strings.Contains(p, "unsafe-eval") {
		t.Errorf("script-src must not carry unsafe-inline/unsafe-eval:\n%s", p)
	}
}

func TestDashboardCSPModes(t *testing.T) {
	gw, _, _ := newQualityFixture(t)
	get := func(path string) http.Header {
		resp, err := http.Get(gw.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.Header
	}
	page := "/dashboard/chat?key=" + testGatewayToken

	t.Setenv(cspEnv, "")
	h := get(page)
	if h.Get("Content-Security-Policy") == "" || h.Get("Content-Security-Policy-Report-Only") != "" {
		t.Errorf("default mode must enforce: %v", h)
	}
	t.Setenv(cspEnv, "report-only")
	h = get(page)
	if h.Get("Content-Security-Policy") != "" || !strings.Contains(h.Get("Content-Security-Policy-Report-Only"), "script-src") {
		t.Errorf("report-only mode must soften: %v", h)
	}
	t.Setenv(cspEnv, "off")
	h = get(page)
	if h.Get("Content-Security-Policy") != "" || h.Get("Content-Security-Policy-Report-Only") != "" {
		t.Errorf("off mode must drop CSP: %v", h)
	}
	// 开关只管 CSP：companion headers 不受影响。
	for _, k := range []string{"X-Content-Type-Options", "Referrer-Policy", "Permissions-Policy"} {
		if h.Get(k) == "" {
			t.Errorf("%s missing in off mode", k)
		}
	}
}

// TestBrowserSurfacesSecurityHeaders 浏览器形态的每个出口都带安全头；
// CSP 里的脚本哈希与响应正文里的内联脚本一一对应（独立口径重算）。
func TestBrowserSurfacesSecurityHeaders(t *testing.T) {
	gw, _, _ := newQualityFixture(t)
	type surface struct {
		path   string
		status int
	}
	surfaces := []surface{{"/", http.StatusOK}, {"/dashboard/keys", http.StatusUnauthorized}}
	for _, p := range []string{"providers", "keys", "usage", "compression", "resilience", "chat"} {
		surfaces = append(surfaces, surface{"/dashboard/" + p + "?key=" + testGatewayToken, http.StatusOK})
	}
	for _, sf := range surfaces {
		req, _ := http.NewRequest(http.MethodGet, gw.URL+sf.path, nil)
		req.Header.Set("Accept", "text/html") // 浏览器形态：未带 key 时走双语 401 指引页而非裸 JSON
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", sf.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != sf.status {
			t.Fatalf("%s status = %d, want %d", sf.path, resp.StatusCode, sf.status)
		}
		h := resp.Header
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s X-Content-Type-Options = %q", sf.path, got)
		}
		if got := h.Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s Referrer-Policy = %q", sf.path, got)
		}
		if got := h.Get("Permissions-Policy"); !strings.Contains(got, "camera=()") {
			t.Errorf("%s Permissions-Policy = %q", sf.path, got)
		}
		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self'") {
			t.Errorf("%s CSP = %q", sf.path, csp)
		}
		// 独立口径：按 "<script>" 精确切分出每个内联脚本正文，按浏览器
		// 口径归一行尾后逐个核对哈希在头里。
		norm := strings.ReplaceAll(strings.ReplaceAll(string(body), "\r\n", "\n"), "\r", "\n")
		parts := strings.Split(norm, "<script>")
		if len(parts)-1 != strings.Count(csp, "'sha256-") {
			t.Errorf("%s: %d inline scripts but %d hashes in CSP", sf.path, len(parts)-1, strings.Count(csp, "'sha256-"))
		}
		for i, part := range parts[1:] {
			end := strings.Index(strings.ToLower(part), "</script>")
			if end < 0 {
				t.Fatalf("%s: inline script %d unterminated", sf.path, i)
			}
			if !strings.Contains(csp, hashOf(part[:end])) {
				t.Errorf("%s: inline script %d has no matching CSP hash", sf.path, i)
			}
		}
	}

	// 静态资产与 API 也带 nosniff；资产的 MIME 必须正确（嗅探已关闭）。
	resp, err := http.Get(gw.URL + "/dashboard/assets/motion.js")
	if err != nil {
		t.Fatalf("asset: %v", err)
	}
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("asset Content-Type = %q", ct)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("asset missing nosniff")
	}
	resp, err = http.Get(gw.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("healthz missing nosniff")
	}
}
