# Build the release assets that match .goreleaser.yaml naming, without needing
# goreleaser installed (it is not available in this environment).
#
#   powershell -File scripts/release-assets.ps1 -Version 0.1.12
#
# Produces, in -OutDir (default %TEMP%\ofd-rel-<version>):
#   omnifusion_<version>_<os>_<arch>.zip   x6  (windows/linux/darwin x amd64/arm64)
#   checksums.txt                              (sha256, sorted)
# English-only source on purpose: PowerShell 5.1 reads BOM-less files as ANSI.
param(
  [Parameter(Mandatory = $true)][string]$Version,
  [string]$OutDir = "",
  [string]$Installer = ""
)
$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent $PSScriptRoot
if (-not $OutDir) { $OutDir = Join-Path $env:TEMP ("ofd-rel-" + $Version) }
New-Item -ItemType Directory -Force -Path $OutDir | Out-Null

$env:CGO_ENABLED = '0'
foreach ($os in 'windows', 'linux', 'darwin') {
  foreach ($arch in 'amd64', 'arm64') {
    $env:GOOS = $os
    $env:GOARCH = $arch
    # Stage under the canonical binary name so the zip contains exactly what the
    # README tells users to run: ofd.exe on Windows, ofd elsewhere.
    $bin = 'ofd'
    if ($os -eq 'windows') { $bin += '.exe' }
    $stage = Join-Path $OutDir ("stage_" + $os + "_" + $arch)
    New-Item -ItemType Directory -Force -Path $stage | Out-Null
    $binPath = Join-Path $stage $bin
    Write-Host "[assets] building $os/$arch"
    & go build -trimpath -ldflags "-s -w -X main.version=v$Version" -o $binPath ./cmd/ofd
    if ($LASTEXITCODE -ne 0) { throw "go build failed for $os/$arch" }
    $zip = Join-Path $OutDir ("omnifusion_" + $Version + "_" + $os + "_" + $arch + ".zip")
    if (Test-Path $zip) { Remove-Item $zip -Force }
    Compress-Archive -Path $binPath -DestinationPath $zip
    Remove-Item $stage -Recurse -Force
  }
}

if ($Installer -and (Test-Path $Installer)) {
  Copy-Item $Installer (Join-Path $OutDir (Split-Path -Leaf $Installer)) -Force
}

# checksums.txt: sha256 over the zips (plus the installer when copied in).
$lines = @()
Get-ChildItem -Path $OutDir -File | Where-Object { $_.Extension -in '.zip', '.exe' } | Sort-Object Name | ForEach-Object {
  $h = (Get-FileHash -Algorithm SHA256 $_.FullName).Hash.ToLower()
  $lines += "$h  $($_.Name)"
}
[System.IO.File]::WriteAllText((Join-Path $OutDir 'checksums.txt'), ($lines -join "`n") + "`n", (New-Object System.Text.UTF8Encoding($false)))
Write-Host "[assets] done -> $OutDir"
Get-ChildItem -Path $OutDir -File | ForEach-Object { Write-Host ("  " + $_.Name) }
