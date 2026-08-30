// landing.go 是根路径 "/" 的双语落地页（2026-08-30）：裸 IP:端口 访问
// 不再 404——小白第一眼看到「网关运行中」与各入口，而不是裸错误页。
// 页面不含任何敏感信息（不回显令牌），无需鉴权，与 /healthz 同级开放。
package server

import "net/http"

const landingHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>OmniFusion</title>
<style>
  :root { color-scheme: dark; }
  body { font: 15px/1.7 system-ui, sans-serif; background: #0d1117; color: #e6edf3;
         margin: 0; min-height: 100vh; display: grid; place-items: center; }
  .card { max-width: 560px; padding: 36px 40px; border: 1px solid #30363d;
          border-radius: 12px; background: #161b22; }
  h1 { font-size: 20px; margin: 0 0 4px; }
  .ok { color: #3fb950; font-size: 13px; margin-bottom: 20px; }
  a { color: #58a6ff; text-decoration: none; }
  .row { padding: 10px 0; border-top: 1px solid #21262d; }
  .row b { display: block; font-size: 14px; }
  .row span { color: #8b949e; font-size: 12.5px; }
  code { background: #0d1117; border: 1px solid #30363d; border-radius: 4px;
         padding: 1px 6px; font-size: 12.5px; }
  .foot { margin-top: 22px; color: #8b949e; font-size: 12px;
          border-top: 1px solid #21262d; padding-top: 12px; }
</style>
</head>
<body>
<div class="card">
  <h1>OmniFusion · AI Gateway</h1>
  <div class="ok">● 网关运行中 · Gateway is running</div>
  <div class="row"><b><a id="chat" href="/dashboard/chat">💬 对话页 · Chat</a></b>
    <span>装完即聊，默认「⚡ 自动」选最强免费模型 / chat with free models right away</span></div>
  <div class="row"><b><a id="dash" href="/dashboard">📊 控制台 · Dashboard</a></b>
    <span>提供商 / 密钥 / 用量 / 压缩 / 弹性 / providers · keys · usage · resilience</span></div>
  <div class="row"><b>🔌 OpenAI 兼容 API</b>
    <span><code id="base">/v1</code> · Anthropic <code>/v1/messages</code> · Gemini <code>/v1beta</code> · Responses <code>/v1/responses</code></span></div>
  <div class="row"><b>🔑 密钥 · API key</b>
    <span>页面与 API 需要网关令牌（<code>?key=…</code> / <code>Authorization: Bearer …</code>），终端执行 <code>ofd gateway-key</code> 获取 / run <code>ofd gateway-key</code> to get the token</span></div>
  <div class="foot">Apache-2.0 · 无遥测 · 数据本机 · No telemetry · data stays local · <a href="https://github.com/swsgbl/omnifusion/blob/main/SECURITY.md">安全 · Security</a></div>
</div>
<script>
'use strict';
const key = new URLSearchParams(location.search).get('key') || '';
document.getElementById('chat').href = '/dashboard/chat' + (key ? '?key=' + encodeURIComponent(key) : '');
document.getElementById('dash').href = '/dashboard' + (key ? '?key=' + encodeURIComponent(key) : '');
document.getElementById('base').textContent = location.origin + '/v1';
</script>
</body>
</html>`

// handleRoot 服务根路径落地页（无鉴权：不含任何敏感信息）。
func (s *Server) handleRoot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(landingHTML))
}
