# insights.ps1 — OmniFusion distribution insights (maintainer tool)
#
# Zero-telemetry analytics: we never instrument the app. Instead this
# script aggregates the PUBLIC signals that already exist on the hosting
# platforms (downloads, clones, views, stars, issues) plus the AtomGit
# mirror's star/watch counters. Run it whenever you want a pulse check;
# it stores a snapshot and reports the delta since the previous run.
#
# Usage:
#   pwsh -NoProfile scripts/insights.ps1              # report + save snapshot
#   pwsh -NoProfile scripts/insights.ps1 -NoSave      # report only
#
# Requirements: gh CLI authenticated for the GitHub repo (read-only), and
# optionally the ag CLI for AtomGit mirror numbers. Missing tools degrade
# to "--" rather than failing the whole report.

param(
  [switch]$NoSave,
  [string]$Repo = 'swsgbl/omnifusion',
  [string]$Mirror = 'hongfu/omnifusion'
)

$ErrorActionPreference = 'Continue'
# gh emits UTF-8; the console codepage (GBK on zh-CN) would mangle it and
# break JSON parsing on any non-ASCII field (repo description contains
# Chinese). Force UTF-8 for native-command output for the whole run.
$prevEnc = [Console]::OutputEncoding
try { [Console]::OutputEncoding = [System.Text.Encoding]::UTF8 } catch {}
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $here
$snapPath = Join-Path $root 'docs-internal/insights-snapshot.json'

# gh JSON helper: dump to a temp file and read as UTF-8 bytes, then parse.
# NEVER pipe gh stdout through PowerShell variables directly: the console
# codepage (GBK on zh-CN) mangles UTF-8 payloads and the repo description
# alone breaks JSON parsing (verified 2026-09-12: "unexpected character at
# position 1408" was mojibake, not real corruption).
function Gh-Json([string[]]$ArgsList) {
  $tmp = [System.IO.Path]::GetTempFileName()
  try {
    & gh @ArgsList 2>$null | Out-File -FilePath $tmp -Encoding utf8
    $raw = [System.IO.File]::ReadAllText($tmp, [System.Text.Encoding]::UTF8)
    if (-not $raw.Trim()) { return $null }
    return ($raw | ConvertFrom-Json)
  } catch { return $null } finally {
    Remove-Item $tmp -Force -ErrorAction SilentlyContinue
  }
}

Write-Output ''
Write-Output ('OmniFusion Insights  ' + (Get-Date -Format 'yyyy-MM-dd HH:mm'))
Write-Output ('=' * 56)

# ---------- platform identity ----------
$repoInfo = Gh-Json @('api', "/repos/$Repo")
Write-Output ''
Write-Output 'PUBLIC SIGNALS (no telemetry involved)'
if ($repoInfo) {
  Write-Output ("  GitHub   stars {0}  forks {1}  issues {2}  watchers {3}" -f `
    $repoInfo.stargazers_count, $repoInfo.forks_count, $repoInfo.open_issues_count, $repoInfo.subscribers_count)
} else {
  Write-Output '  GitHub   -- (gh not authenticated or offline)'
}

$mirrorLine = '  AtomGit  -- (ag CLI unavailable)'
if (Get-Command ag -ErrorAction SilentlyContinue) {
  $mv = & ag repo view $Mirror 2>$null | Out-String
  $ms = if ($mv -match 'Stars:\s*(\d+)') { $Matches[1] } else { '--' }
  $mf = if ($mv -match 'Forks:\s*(\d+)') { $Matches[1] } else { '--' }
  $mw = if ($mv -match 'Watches:\s*(\d+)') { $Matches[1] } else { '--' }
  $mirrorLine = "  AtomGit  stars $ms  forks $mf  watchers $mw"
}
Write-Output $mirrorLine

# ---------- downloads by release ----------
$releases = Gh-Json @('api', "/repos/$Repo/releases", '--paginate', '--slurp')
$downloads = @{}
$totalDl = 0
$desktopDl = 0
if ($releases) {
  foreach ($rel in @($releases)) {
    if (-not $rel) { continue }
    $tag = $rel.tag_name
    foreach ($asset in @($rel.assets)) {
      if (-not $asset) { continue }
      $n = [int]$asset.download_count
      if (-not $downloads.ContainsKey($tag)) { $downloads[$tag] = 0 }
      $downloads[$tag] += $n
      $totalDl += $n
      if ($asset.name -match 'Desktop') { $desktopDl += $n }
    }
  }
}

Write-Output ''
Write-Output 'DOWNLOADS (cumulative, per release)'
if ($downloads.Count -gt 0) {
  foreach ($tag in ($downloads.Keys | Sort-Object -Descending)) {
    Write-Output ("  {0,-9} {1,4}" -f $tag, $downloads[$tag])
  }
  Write-Output ("  {0,-9} {1,4}   (desktop installers: {2})" -f 'TOTAL', $totalDl, $desktopDl)
} else {
  Write-Output '  -- (no release data; gh offline?)'
}

# ---------- traffic (14-day window) ----------
$views = Gh-Json @('api', "/repos/$Repo/traffic/views")
$clones = Gh-Json @('api', "/repos/$Repo/traffic/clones")

Write-Output ''
Write-Output 'TRAFFIC (last 14 days)'
if ($views) { Write-Output ("  views   {0,5}  (unique visitors {1})" -f $views.count, $views.uniques) } else { Write-Output '  views   --' }
if ($clones) { Write-Output ("  clones  {0,5}  (unique cloners  {1})" -f $clones.count, $clones.uniques) } else { Write-Output '  clones  --' }

# ---------- delta vs previous snapshot ----------
$now = [ordered]@{
  at         = (Get-Date -Format 'yyyy-MM-ddTHH:mm:ss')
  stars      = if ($repoInfo) { [int]$repoInfo.stargazers_count } else { $null }
  forks      = if ($repoInfo) { [int]$repoInfo.forks_count } else { $null }
  issues     = if ($repoInfo) { [int]$repoInfo.open_issues_count } else { $null }
  downloads  = $totalDl
  desktop_dl = $desktopDl
  views_14d  = if ($views) { [int]$views.count } else { $null }
  clones_14d = if ($clones) { [int]$clones.count } else { $null }
}

if (Test-Path $snapPath) {
  $prev = Get-Content $snapPath -Raw | ConvertFrom-Json
  Write-Output ''
  Write-Output ('CHANGE SINCE ' + $prev.at)
  function Delta($name, $old, $new) {
    if ($null -eq $old -or $null -eq $new) { Write-Output ("  {0,-12} --" -f $name); return }
    $d = $new - $old
    $sign = if ($d -gt 0) { '+' } else { '' }
    Write-Output ("  {0,-12} {1}{2}" -f $name, $sign, $d)
  }
  Delta 'downloads' $prev.downloads $now.downloads
  Delta 'desktop'   $prev.desktop_dl $now.desktop_dl
  Delta 'stars'     $prev.stars $now.stars
  Delta 'clones14d' $prev.clones_14d $now.clones_14d
  Delta 'views14d'  $prev.views_14d $now.views_14d
} else {
  Write-Output ''
  Write-Output 'CHANGE SINCE  (no previous snapshot - this run creates the baseline)'
}

if (-not $NoSave) {
  $dir = Split-Path -Parent $snapPath
  if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
  ($now | ConvertTo-Json) | Set-Content -Path $snapPath -Encoding UTF8
  Write-Output ''
  Write-Output ('snapshot saved: ' + $snapPath)
}
try { [Console]::OutputEncoding = $prevEnc } catch {}
Write-Output ''
