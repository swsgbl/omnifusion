//go:build windows

// runcmd_windows.go 提供 Windows 的后台进程属性：新进程组（子进程不
// 收控制台 Ctrl-C，autospawn 的网关在 CLI 会话结束后存活）+ 脱离
// 控制台（输出已重定向到日志文件）。
package main

import "syscall"

// detachedProcess = CREATE_DETACHED_PROCESS：新进程不挂控制台（输出
// 已由 spawnDetached 重定向到日志文件），不被 Windows 导出故本地定义。
const detachedProcess = 0x00000008

func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	}
}
