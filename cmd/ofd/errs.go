// errs.go 是 CLI 用户可见错误的双语助手（中文在前、英文括注）：
// 小白读中文即懂，英文保留在括号里供终端检索与报 issue。服务端 API
// 错误不在此层（HTTP 客户端面向程序，维持英文契约）。
package main

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
)

// syscallEADDRINUSE 是跨平台的"地址已被占用"哨兵。
var syscallEADDRINUSE error = syscall.EADDRINUSE

// biErr 构造 "中文（English）" 形态的错误。
func biErr(zh, en string) error {
	return fmt.Errorf("%s (%s)", zh, en)
}

// biWrap 给底层错误附双语前缀（保留原文与错误链）。
func biWrap(err error, zh, en string) error {
	return fmt.Errorf("%s (%s): %w", zh, en, err)
}

// isAddrInUse 判定监听地址被占（Windows/Unix 双口径）。
func isAddrInUse(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscallEADDRINUSE) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "Only one usage of each socket address") ||
		strings.Contains(msg, "access is denied") && strings.Contains(msg, "bind") // Windows 偶发形态
}
