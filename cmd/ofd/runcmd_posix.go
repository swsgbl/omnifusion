//go:build !windows

// runcmd_posix.go 提供 POSIX 的后台进程属性：新会话脱离控制终端，
// autospawn 的网关在 CLI 会话结束后存活。
package main

import "syscall"

func detachedAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
