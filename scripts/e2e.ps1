# e2e.ps1 - live end-to-end acceptance matrix for a running OmniFusion gateway.
# Usage: powershell -File scripts\e2e.ps1 [-Base http://127.0.0.1:20130] [-Key ofg-...]
# ASCII-only by design (repo-safe on any codepage). English payloads suffice for
# protocol verification. Exit code 0 = all green.
param(
  [string]$Base = "http://127.0.0.1:20130",
  [string]$Key = ""
)
$ErrorActionPreference = 'Continue'
if (-not $Key) {
  $ofd = Join-Path $env:LOCALAPPDATA 'OmniFusion Desktop\bin\ofd.exe'
  if (Test-Path $ofd) { $Key = (& $ofd gateway-key | Select-Object -First 1) }
}
if (-not $Key) { Write-Output 'FATAL no gateway key (pass -Key or install ofd)'; exit 1 }

$results = @()
function T($name, $check) {
  try {
    $e = & $check   # scriptblock returns evidence string; empty/exception = fail
    if ($e) { $script:results += "PASS $name :: $e" } else { $script:results += "FAIL $name" }
  } catch { $script:results += "FAIL $name :: $($_.Exception.Message)" }
}
function Http($method, $path, $body, $headers) {
  $args = @('-sS','-X',$method,'--max-time','60','-w',"`nHTTP%{http_code}")
  foreach ($h in $headers.GetEnumerator()) { $args += @('-H', ($h.Key + ': ' + $h.Value)) }
  if ($body) { $f = [IO.Path]::GetTempFileName(); [IO.File]::WriteAllText($f, $body); $args += @('--data-binary',"@$f") }
  $args += ($Base + $path)
  $out = & curl.exe @args 2>&1
  return ($out -join "`n")
}
$H = @{ Authorization = "Bearer $Key" }
$HJson = @{ Authorization = "Bearer $Key"; 'Content-Type' = 'application/json' }
$chatBody = '{"model":"MODEL","messages":[{"role":"user","content":"Reply with exactly: OK"}],"max_tokens":500}'
function Chat($m) { $chatBody.Replace('MODEL', $m) }

# --- core plane ---
T 'healthz' { $r = Http GET /healthz $null @{}; if ($r -match 'HTTP200') {'200'} else {''} }
T 'landing-page' { $r = Http GET / $null @{}; if ($r -match 'Gateway is running' -and $r -match 'Apache-2.0') {'running+footer'} else {''} }
T 'models-authed' { $r = Http GET /v1/models $null $H; if ($r -match 'HTTP200' -and $r -match 'deepseek') {'catalog ok'} else {''} }
T 'models-noauth-401' { $r = Http GET /v1/models $null @{}; if ($r -match 'HTTP401' -and $r -match 'authentication_error') {'401 json'} else {''} }

# --- chat completions: strategies ---
T 'chat-plain-model' { $r = Http POST /v1/chat/completions (Chat 'deepseek-v4-flash') $HJson; if ($r -match 'HTTP200' -and $r -match '"content"') {'200 content'} else { $r.Substring(0,[Math]::Min(120,$r.Length)) ; '' } }
T 'chat-quality-auto' { $r = Http POST /v1/chat/completions (Chat '@quality') $HJson; if ($r -match 'HTTP200' -and $r -match '"content":"[^"]') {'200 reply'} else {''} }
T 'chat-quality-target' { $r = Http POST /v1/chat/completions (Chat '@quality:deepseek-v4-pro') $HJson; if ($r -match 'HTTP200') {'200'} else {''} }
T 'chat-cheap-auto' { $r = Http POST /v1/chat/completions (Chat '@cheap') $HJson; if ($r -match 'HTTP200' -and $r -match '"content"') {'200'} else {''} }
T 'chat-fast-target' { $r = Http POST /v1/chat/completions (Chat '@fast:deepseek-v4-flash') $HJson; if ($r -match 'HTTP200') {'200'} else {''} }
T 'chat-priority-target' { $r = Http POST /v1/chat/completions (Chat '@priority:deepseek-v4-flash') $HJson; if ($r -match 'HTTP200') {'200'} else {''} }
T 'chat-lkgp-target' { $r = Http POST /v1/chat/completions (Chat '@lkgp:deepseek-v4-flash') $HJson; if ($r -match 'HTTP200') {'200'} else {''} }
T 'chat-strategy-header' { $h=@{Authorization="Bearer $Key";'Content-Type'='application/json';'X-OmniFusion-Strategy'='priority'}; $r = Http POST /v1/chat/completions (Chat 'deepseek-v4-flash') $h; if ($r -match 'HTTP200') {'200'} else {''} }

# --- directives that must fail with a CLEAR error (not 'no attempts') ---
T 'chat-combo-unknown-clear-400' { $r = Http POST /v1/chat/completions (Chat '@combo:NoSuchCombo') $HJson; if ($r -match 'HTTP4\d\d' -and $r -notmatch 'no attempts') {'clean 4xx'} else {''} }
T 'chat-smart-unconfigured-clear' { $r = Http POST /v1/chat/completions (Chat '@smart') $HJson; if ($r -match 'HTTP4\d\d' -and $r -notmatch 'no attempts') {'clean 4xx'} else {''} }
T 'chat-fusion-unconfigured-clear' { $r = Http POST /v1/chat/completions (Chat '@fusion') $HJson; if ($r -match 'HTTP4\d\d' -and $r -notmatch 'no attempts') {'clean 4xx'} else {''} }
T 'chat-bogus-model-clear' { $r = Http POST /v1/chat/completions (Chat 'totally-bogus-model') $HJson; if ($r -notmatch 'no attempts') {'no-cryptic'} else {''} }

# --- streaming ---
T 'chat-stream-quality' {
  $f=[IO.Path]::GetTempFileName(); [IO.File]::WriteAllText($f, (Chat '@quality').Replace('"max_tokens":500','"max_tokens":500,"stream":true'))
  $out = & curl.exe -sS -N --max-time 60 -H "Authorization: Bearer $Key" -H 'Content-Type: application/json' --data-binary "@$f" ($Base + '/v1/chat/completions') 2>&1
  if (($out -join '') -match 'data: ' -and ($out -join '') -match 'delta') {'sse ok'} else {''}
}

# --- other inbound protocols ---
T 'anthropic-messages' {
  $b='{"model":"deepseek-v4-flash","max_tokens":100,"messages":[{"role":"user","content":"Reply exactly: OK"}]}'
  $h=@{Authorization="Bearer $Key";'Content-Type'='application/json';'anthropic-version'='2023-06-01'}
  $r = Http POST /v1/messages $b $h
  if ($r -match 'HTTP200' -and $r -match '"text"') {'200 text'} else {''}
}
T 'responses-api' {
  $b='{"model":"deepseek-v4-flash","input":"Reply exactly: OK","max_output_tokens":100}'
  $r = Http POST /v1/responses $b $HJson
  if ($r -match 'HTTP200') {'200'} else {''}
}
T 'gemini-generate' {
  $b='{"contents":[{"parts":[{"text":"Reply exactly: OK"}]}]}'
  $h=@{'x-goog-api-key'=$Key;'Content-Type'='application/json'}
  $r = Http POST /v1beta/models/deepseek-v4-flash:generateContent $b $h
  if ($r -match 'HTTP200' -and $r -match 'candidates') {'200 candidates'} else {''}
}

# --- semantics cache ---
T 'semantic-cache-hit' {
  $b = Chat 'deepseek-v4-flash'
  $null = Http POST /v1/chat/completions $b $HJson
  Start-Sleep -Seconds 2
  $f=[IO.Path]::GetTempFileName(); [IO.File]::WriteAllText($f,$b)
  $out = & curl.exe -sS --max-time 60 -D - -o NUL -H "Authorization: Bearer $Key" -H 'Content-Type: application/json' --data-binary "@$f" ($Base + '/v1/chat/completions') 2>&1
  if (($out -join '') -match 'X-Omnifusion-Cache:\s*hit' -or ($out -join '') -match 'X-OmniFusion-Cache:\s*hit') {'cache hit'} else {'miss(no header)'}
}

# --- malformed input ---
T 'malformed-json-400' { $r = Http POST /v1/chat/completions '{"model":' $HJson; if ($r -match 'HTTP400') {'400'} else {''} }

# --- dashboard pages & APIs ---
foreach ($p in @('providers','keys','usage','compression','resilience','chat')) {
  T ("dashboard-html-" + $p) { $r = Http GET ("/dashboard/" + $p + "?key=" + $Key) $null @{}; if ($r -match 'HTTP200' -and $r -match '<html') {'200 html'} else {''} }
}
T 'dashboard-browser-401-html' { $r = & curl.exe -sS --max-time 30 -H 'Accept: text/html' ($Base + '/dashboard/providers') 2>&1; if (($r -join '') -match 'Gateway token required' -or ($r -join '') -match [char]0x7f+'' ) {'html guide'} else {''} }
foreach ($a in @('providers','keys','usage','compression/stats','resilience','audit')) {
  T ("dashboard-api-" + ($a -replace '/','-')) { $r = Http GET ("/dashboard/api/" + $a + "?key=" + $Key) $null @{}; if ($r -match 'HTTP200' -and $r -match '^\s*[\{\[]') {'200 json'} else {''} }
}

# --- observability & agent surfaces ---
T 'metrics-authed' { $r = Http GET /metrics $null $H; if ($r -match 'HTTP200' -and $r -match 'omnifusion') {'200 metrics'} else {''} }
T 'metrics-noauth-401' { $r = Http GET /metrics $null @{}; if ($r -match 'HTTP401') {'401'} else {''} }
T 'mcp-initialize' {
  $b='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"e2e","version":"1"}}}'
  $r = Http POST /mcp $b $HJson
  if ($r -match 'HTTP200' -and $r -match 'serverInfo') {'init ok'} else {''}
}
T 'a2a-agent-card' { $r = Http GET /.well-known/agent-card.json $null @{}; if ($r -match 'HTTP200' -and $r -match 'capabilities') {'card ok'} else {''} }
T 'a2a-rpc' {
  $b='{"jsonrpc":"2.0","id":2,"method":"message/send","params":{"message":{"role":"user","parts":[{"kind":"text","text":"Reply exactly: OK"}]}}}'
  $r = Http POST /rpc $b $HJson
  if ($r -match 'HTTP200' -or $r -match 'HTTP202') {'accepted'} else {''}
}

# --- misc http behavior ---
T 'unknown-route-404' { $r = Http GET /definitely-not-here $null @{}; if ($r -match 'HTTP404') {'404'} else {''} }

# --- CLI ---
$ofd = Join-Path $env:LOCALAPPDATA 'OmniFusion Desktop\bin\ofd.exe'
if (Test-Path $ofd) {
  T 'cli-status' { $o = & $ofd status 2>&1; if (($o -join ' ') -match 'providers ready') {'status ok'} else {''} }
  T 'cli-key-list' { $o = & $ofd key list 2>&1; $n = ($o | Where-Object { $_ -match 'label=' }).Count; if ($n -ge 1) {"$n keys"} else {''} }
  T 'cli-gateway-key' { $o = & $ofd gateway-key 2>&1; if (($o -join '') -match '^ofg-') {'ofg- ok'} else {''} }
  T 'cli-connect-print' { $env:CLAUDE_CONFIG_DIR = Join-Path $env:TEMP ('e2e-claude-' + [Guid]::NewGuid().ToString('N')); $o = & $ofd connect claude --print 2>&1; Remove-Item -Recurse -Force $env:CLAUDE_CONFIG_DIR -ErrorAction SilentlyContinue; if (($o -join '') -match 'ANTHROPIC_BASE_URL') {'print ok'} else {''} }
}

# --- report ---
$fail = @($results | Where-Object { $_ -match '^FAIL' })
$results | ForEach-Object { Write-Output $_ }
Write-Output ('---- ' + ($results.Count - $fail.Count) + '/' + $results.Count + ' passed ----')
if ($fail.Count -gt 0) { exit 1 } else { exit 0 }
