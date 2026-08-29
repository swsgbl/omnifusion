# ============================================================
# smoke.ps1 — OmniFusion 全链路本地冒烟测试（PowerShell 版，Windows）
#
# 对应 docs/05-工程实施计划.md「工作流纪律」第 3 条，与 scripts/smoke.sh
# 同口径：mockup(127.0.0.1:11434) → ofd(127.0.0.1:20130)，12 项断言
# 全 PASS 才退出 0。控制台输出用英文，规避中文代码页乱码。
#
# 用法（仓库根目录执行）：
#   powershell -NoProfile -ExecutionPolicy Bypass -File scripts\smoke.ps1
# 前置条件：
#   1. go 工具链可用（脚本现场构建 mockup 与 ofd 两个二进制）；
#   2. 本机 11434 与 20130 端口空闲——被占用时直接退出码 1，
#      请先清理占用进程（脚本不会杀不属于自己的进程）。
# ============================================================

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Net.Http

$RepoRoot = Split-Path -Parent $PSScriptRoot
$Base = 'http://127.0.0.1:20130'
$Tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("ofd-smoke-" + [System.IO.Path]::GetRandomFileName())

$script:PassCount = 0
$script:FailCount = 0
$script:Fatal = $false
$script:OfdProc = $null
$script:MockupProc = $null

function Report([bool]$Ok, [string]$Name, [string]$Detail) {
    if ($Ok) { $script:PassCount++ } else { $script:FailCount++ }
    $tag = 'FAIL'; if ($Ok) { $tag = 'PASS' }
    if ($Detail) { Write-Host ("[{0}] {1} - {2}" -f $tag, $Name, $Detail) }
    else { Write-Host ("[{0}] {1}" -f $tag, $Name) }
}

function Assert-Equal($Got, $Want, $Name) {
    Report ("$Got" -eq "$Want") $Name ("got='$Got' want='$Want'")
}

function Wait-Until([scriptblock]$Probe, [int]$TimeoutMs) {
    # 轮询探测直到成功或超时（毫秒）；探测异常（连接拒绝等）视作未就绪
    $deadline = [DateTime]::UtcNow.AddMilliseconds($TimeoutMs)
    while ([DateTime]::UtcNow -lt $deadline) {
        try { if (& $Probe) { return $true } } catch { }
        Start-Sleep -Milliseconds 300
    }
    return $false
}

function Test-PortFree([int]$Port) {
    $conns = @(Get-NetTCPConnection -LocalPort $Port -ErrorAction SilentlyContinue)
    return ($conns.Count -eq 0)
}

# HTTP 探针：HttpClient 且关闭系统代理（不受 sing-box 等代理干扰）；
# 401 不抛异常，统一返回 @{ Code = 状态码; Body = 响应体 }
$handler = New-Object System.Net.Http.HttpClientHandler
$handler.UseProxy = $false
$client = New-Object System.Net.Http.HttpClient($handler)
$client.Timeout = [TimeSpan]::FromSeconds(30)

function Send-Http([string]$Method, [string]$Url, [string]$Json, [string]$Token) {
    $req = New-Object System.Net.Http.HttpRequestMessage ([System.Net.Http.HttpMethod]::new($Method), $Url)
    if ($Token) {
        $req.Headers.Authorization = New-Object System.Net.Http.Headers.AuthenticationHeaderValue('Bearer', $Token)
    }
    if ($Json) {
        $req.Content = New-Object System.Net.Http.StringContent ($Json, [System.Text.Encoding]::UTF8, 'application/json')
    }
    $resp = $client.SendAsync($req).GetAwaiter().GetResult()
    $body = $resp.Content.ReadAsStringAsync().GetAwaiter().GetResult()
    $req.Dispose(); $resp.Dispose()
    return @{ Code = [int]$resp.StatusCode; Body = $body }
}
function Get-Http([string]$Url, [string]$Token) { Send-Http 'GET' $Url $null $Token }
function Post-Http([string]$Url, [string]$Json, [string]$Token) { Send-Http 'POST' $Url $Json $Token }

function Stop-SmokeProcs {
    # 只停自己启动的进程（Start-Process -PassThru 拿到的句柄）
    foreach ($p in @($script:OfdProc, $script:MockupProc)) {
        if ($p -and -not $p.HasExited) {
            Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
        }
    }
    foreach ($p in @($script:OfdProc, $script:MockupProc)) {
        if ($p) { $null = $p.WaitForExit(3000) }
    }
}

try {
    Write-Host '==> checking ports 11434 / 20130 are free ...'
    foreach ($p in @(11434, 20130)) {
        if (-not (Test-PortFree $p)) {
            Write-Host "[FATAL] port $p is occupied; free it first (this script kills only its own processes)"
            exit 1
        }
    }

    New-Item -ItemType Directory -Path $Tmp | Out-Null
    $mockupExe = Join-Path $Tmp 'mockup.exe'
    $ofdExe = Join-Path $Tmp 'ofd.exe'
    Write-Host "==> building mockup + ofd into $Tmp ..."
    Push-Location $RepoRoot
    try {
        & go build -o $mockupExe '.\scripts\mockup'
        if ($LASTEXITCODE -ne 0) { throw 'go build mockup failed' }
        & go build -o $ofdExe '.\cmd\ofd'
        if ($LASTEXITCODE -ne 0) { throw 'go build ofd failed' }
    }
    finally { Pop-Location }

    Write-Host '==> starting mockup on 127.0.0.1:11434 ...'
    $script:MockupProc = Start-Process -FilePath $mockupExe -ArgumentList '-addr', '127.0.0.1:11434' `
        -WorkingDirectory $Tmp -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput (Join-Path $Tmp 'mockup.log') `
        -RedirectStandardError (Join-Path $Tmp 'mockup.err.log')
    if (-not (Wait-Until { (Get-Http 'http://127.0.0.1:11434/healthz' $null).Code -eq 200 } 10000)) {
        throw 'mockup not ready within 10s'
    }

    Write-Host "==> starting ofd on 127.0.0.1:20130 (store under $Tmp\data\) ..."
    $script:OfdProc = Start-Process -FilePath $ofdExe `
        -WorkingDirectory $Tmp -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput (Join-Path $Tmp 'ofd.log') `
        -RedirectStandardError (Join-Path $Tmp 'ofd.err.log')
    if (-not (Wait-Until { (Get-Http "$Base/healthz" $null).Code -eq 200 } 15000)) {
        throw 'ofd not ready within 15s'
    }

    Write-Host '==> reading gateway key (ofd gateway-key) ...'
    Push-Location $Tmp
    try { $Key = ([string](& $ofdExe gateway-key | Out-String)).Trim() }
    finally { Pop-Location }
    if (-not $Key) { throw 'ofd gateway-key printed nothing' }

    # catalog 异步同步坑：healthz OK 不代表能聊天，必须轮询目录含 mock 模型
    Write-Host '==> waiting for catalog sync to list mock-model-1 (up to 60s) ...'
    if (-not (Wait-Until { ((Get-Http "$Base/v1/models" $Key).Body -match 'mock-model-1') } 60000)) {
        throw 'catalog did not include mock-model-1 within 60s'
    }

    Write-Host '==> assertions ...'
    $h = Get-Http "$Base/healthz" $null
    Assert-Equal $h.Code 200 'GET /healthz -> 200'
    Report ($h.Body -match '"ok"') 'GET /healthz body contains ok' $h.Body

    Assert-Equal (Get-Http "$Base/v1/models" $null).Code 401 'GET /v1/models without key -> 401'
    $m = Get-Http "$Base/v1/models" $Key
    Assert-Equal $m.Code 200 'GET /v1/models with key -> 200'
    Report ($m.Body -match 'mock-model-1') 'GET /v1/models lists mock-model-1'

    $jsonNs = '{"model":"mock-model-1","stream":false,"messages":[{"role":"user","content":"smoke-nostream"}]}'
    $c = Post-Http "$Base/v1/chat/completions" $jsonNs $Key
    Assert-Equal $c.Code 200 'chat non-stream -> 200'
    $content = $null
    try { $content = ($c.Body | ConvertFrom-Json).choices[0].message.content } catch { }
    Report (-not [string]::IsNullOrEmpty($content)) 'chat non-stream content non-empty' ("content='" + $content + "'")

    $jsonSs = '{"model":"mock-model-1","stream":true,"messages":[{"role":"user","content":"smoke-stream"}]}'
    $s = Post-Http "$Base/v1/chat/completions" $jsonSs $Key
    $dataLines = ([regex]::Matches($s.Body, '(?m)^data:')).Count
    $hasDone = $s.Body.Contains('[DONE]')
    Report (($s.Code -eq 200) -and ($dataLines -ge 2) -and $hasDone) `
        'chat stream SSE (>=2 data lines + [DONE])' ("data lines=$dataLines done=$hasDone code=$($s.Code)")

    foreach ($page in @('providers', 'keys', 'usage', 'compression', 'resilience')) {
        Assert-Equal (Get-Http "$Base/dashboard/$page`?key=$Key" $null).Code 200 "dashboard /$page -> 200"
    }

    Assert-Equal (Get-Http "$Base/metrics" $null).Code 401 'GET /metrics without key -> 401'
    $mt = Get-Http "$Base/metrics" $Key
    Assert-Equal $mt.Code 200 'GET /metrics with key -> 200'
    if ($mt.Body -match 'omnifusion_requests_total') {
        Report $true 'metrics contains omnifusion_requests_total'
    }
    else {
        Write-Host '    (info) metric family omnifusion_requests_total absent (possible with zero samples)'
    }

    Push-Location $Tmp
    $statusRc = 1
    try { & $ofdExe status *> $null; $statusRc = $LASTEXITCODE }
    finally { Pop-Location }
    Assert-Equal $statusRc 0 'ofd status exit code 0'

    Write-Host '============================================================'
    Write-Host ("smoke result: PASS={0} FAIL={1}" -f $script:PassCount, $script:FailCount)
    if ($script:FailCount -eq 0) { Write-Host 'ALL PASS' }
    else { Write-Host 'FAILURES present - see [FAIL] lines above' }
}
catch {
    $script:Fatal = $true
    Write-Host ("[FATAL] " + $_.Exception.Message)
    foreach ($log in @('ofd.err.log', 'ofd.log', 'mockup.err.log', 'mockup.log')) {
        $p = Join-Path $Tmp $log
        if (Test-Path $p) {
            Write-Host "---- $log (tail 15) ----"
            Get-Content $p -Tail 15 | ForEach-Object { Write-Host $_ }
        }
    }
}
finally {
    Stop-SmokeProcs
    if (Test-Path $Tmp) { Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue }
}

exit ($(if ($script:FailCount -gt 0 -or $script:Fatal) { 1 } else { 0 }))
