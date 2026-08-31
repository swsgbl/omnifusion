@echo off
rem Build helper for the OmniFusion desktop app.
rem MSVC environment is injected explicitly because rustc cannot always
rem auto-locate link.exe (stale vswhere installer DB etc.). Override the
rem location via VCVARS64 if your installation differs.
rem Usage: build.cmd check   -> cargo check in src-tauri
rem        build.cmd [args]  -> pnpm tauri build [args] (e.g. --no-bundle)
rem The installer bundles the gateway (tauri resources bin/ofd.exe), so the
rem release path first (re)builds ofd from the repo root and copies it in.
setlocal
if "%VCVARS64%"=="" set "VCVARS64=C:\Program Files (x86)\Microsoft Visual Studio\2022\BuildTools\VC\Auxiliary\Build\vcvars64.bat"
call "%VCVARS64%" >nul
if errorlevel 1 (
  echo [build.cmd] failed to initialize MSVC environment from "%VCVARS64%" 1>&2
  exit /b 1
)
cd /d "%~dp0"
if not "%~1"=="check" (
  echo [build.cmd] building ofd.exe for bundling...
  pushd ..\..
  go build -ldflags "-X main.version=v0.1.4" -o apps\desktop\src-tauri\bin\ofd.exe ./cmd/ofd
  if errorlevel 1 ( popd & echo [build.cmd] ofd build failed 1>&2 & exit /b 1 )
  popd
)
if "%~1"=="check" (
  cd src-tauri
  cargo check
  exit /b %errorlevel%
)
pnpm tauri build %*
exit /b %errorlevel%
