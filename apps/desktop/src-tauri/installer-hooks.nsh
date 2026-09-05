; installer-hooks.nsh — 小白友好升级钩子（v0.1.6 实测教训）：
; 静默/双击安装时旧程序若在运行（含托盘驻留/网关子进程），NSIS 对
; 被锁文件的处理不可靠（实测 bin\ofd.exe 换新而主 exe 静默跳过）。
; PREINSTALL 无条件杀整棵进程树并等待句柄释放，让“覆盖安装”永远
; 成功——用户不需要先手动退出旧版本。
!macro NSIS_HOOK_PREINSTALL
  ; 主程序整树（含其拉起的网关子进程与 WebView2 子进程）
  nsExec::ExecToLog 'taskkill /F /T /IM "omnifusion-desktop.exe"'
  ; 网关可能独立运行（终端手动启动/开机自启），按映像名兜底
  nsExec::ExecToLog 'taskkill /F /IM "ofd.exe"'
  ; 句柄释放留时间：杀进程是异步的，立刻 File 会撞锁
  Sleep 1500
!macroend
