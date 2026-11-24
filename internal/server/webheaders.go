// webheaders.go 是浏览器形态响应的现代 Web 安全头：companion policies
// （X-Content-Type-Options / Referrer-Policy / Permissions-Policy）+ 基于
// 内联脚本哈希的 CSP。
//
// 页面是 go:embed 的静态 HTML——没有构建链，也没有第三方脚本源——所以
// 脚本哈希在写出响应时从这份 HTML 的字节现算：改脚本不必手工维护哈希
// 清单，被哈希的字节与被服务的字节永远同源。CSP 里没有 'unsafe-inline'
// （脚本）与 'unsafe-eval'：页面用字符串拼表格渲染上游厂商返回的数据，
// 一旦转义有遗漏，注入进去的事件属性（<img onerror> 一类）会被浏览器
// 直接拒绝——密钥页与对话页都持有网关令牌，这是值得的一道兜底。
package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"os"
	"strings"
)

// cspEnv 是 CSP 的生效模式开关：enforce（默认）/ report-only / off。
// 本地应用没有遥测上报面，这个开关就是 CSP 的灰度手段与逃生通道——
// 浏览器扩展往页面注入内联脚本被拦时，用户可以降级而不必回滚版本。
const cspEnv = "OFD_DASHBOARD_CSP"

type cspMode int

const (
	cspEnforce cspMode = iota
	cspReportOnly
	cspOff
)

func dashboardCSPMode() cspMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(cspEnv))) {
	case "off", "0", "false", "no":
		return cspOff
	case "report-only", "reportonly", "report":
		return cspReportOnly
	}
	return cspEnforce
}

// nosniff 给所有响应加 X-Content-Type-Options: nosniff（MIME 嗅探拦截）。
// 资产的 Content-Type 由扩展名给出（.js → text/javascript），嗅探关闭后
// 浏览器仍能严格按类型加载。
func nosniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// setHTMLSecurityHeaders 给一张即将写出的 HTML 补安全头：CSP（脚本哈希
// 取自这份 html 的内联脚本）+ Referrer-Policy + Permissions-Policy。
// 调用方仍负责 Content-Type / Cache-Control / 状态码。
func setHTMLSecurityHeaders(w http.ResponseWriter, html []byte) {
	policy := contentSecurityPolicy(html)
	switch dashboardCSPMode() {
	case cspEnforce:
		w.Header().Set("Content-Security-Policy", policy)
	case cspReportOnly:
		w.Header().Set("Content-Security-Policy-Report-Only", policy)
	}
	// 页面 URL 的 ?key= 就是网关令牌：对外一个字节的来源信息都不给。
	w.Header().Set("Referrer-Policy", "no-referrer")
	// 仪表盘不用摄像头/定位/麦克风。
	w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
}

// contentSecurityPolicy 组装一张页面的 CSP。script-src 只认同源文件与
// 本页内联脚本的 SHA-256。
func contentSecurityPolicy(html []byte) string {
	return "default-src 'self'; script-src 'self'" + cspHashes(html) +
		"; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none';" +
		" base-uri 'none'; form-action 'self'; connect-src 'self'"
}

func cspHashes(html []byte) string {
	var b strings.Builder
	for _, h := range inlineScriptHashes(html) {
		b.WriteString(" ")
		b.WriteString(h)
	}
	return b.String()
}

// inlineScriptHashes 提取 html 里每个不带 src 的 <script> 正文，返回
// CSP 哈希源（"'sha256-…'"）。正文取起始标签的 > 与 </script 之间的
// 字节，不做实体解码——与浏览器计算脚本哈希的口径一致。标签名按 HTML
// 规则不分大小写。
func inlineScriptHashes(html []byte) []string {
	var out []string
	rest := html
	for {
		i := indexScriptTag(rest, '<', "script")
		if i < 0 {
			return out
		}
		open := rest[i:]
		gt := bytes.IndexByte(open, '>')
		if gt < 0 {
			return out
		}
		body := open[gt+1:]
		end := indexScriptTag(body, '<', "/script")
		if end < 0 {
			return out
		}
		if !hasSrcAttr(string(open[:gt])) {
			sum := sha256.Sum256(normalizeNewlines(body[:end]))
			out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
		}
		rest = body[end+len("</script"):]
	}
}

// normalizeNewlines 按 HTML 输入流预处理规则把 CRLF 与孤立的 CR 归一成
// LF：浏览器是对归一后的脚本文本算哈希的。内嵌页面在 Windows 检出
// （autocrlf）时是 CRLF——不归一的话哈希永远对不上，整页内联脚本会被
// CSP 拦掉，而且只在 Windows 构建上发作。
func normalizeNewlines(b []byte) []byte {
	if !bytes.ContainsRune(b, '\r') {
		return b
	}
	return bytes.ReplaceAll(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")), []byte("\r"), []byte("\n"))
}

// indexScriptTag 找 <name（name 以 ASCII 不分大小写比较）且其后是
// 标签分隔符的位置；找不到返回 -1。
func indexScriptTag(b []byte, lead byte, name string) int {
	for i := 0; i+len(name) < len(b); i++ {
		if b[i] != lead || !eqFoldASCII(b[i+1:i+1+len(name)], name) {
			continue
		}
		if k := i + 1 + len(name); k == len(b) || isTagSep(b[k]) {
			return i
		}
	}
	return -1
}

func eqFoldASCII(b []byte, s string) bool {
	if len(b) != len(s) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if b[i]|0x20 != s[i] {
			return false
		}
	}
	return true
}

func isTagSep(c byte) bool {
	switch c {
	case '>', '/', ' ', '\t', '\n', '\r', '\f':
		return true
	}
	return false
}

// hasSrcAttr 判断 <script 起始标签（到 > 为止）是否带 src 属性。属性名
// 按 HTML 规则不分大小写（SRC= / Src= 同样是外链脚本）。
func hasSrcAttr(open string) bool {
	fields := strings.Fields(open)
	for _, f := range fields[1:] {
		if f = strings.ToLower(f); f == "src" || strings.HasPrefix(f, "src=") {
			return true
		}
	}
	return false
}
