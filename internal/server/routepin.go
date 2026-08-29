// routepin.go 承载 M5.2 运行态控制：全局路由钉选（pin）与默认压缩
// 组合（defaultCombo）的存取 + 数据面注入 helper。均为内存态——重启
// 失效（运维语义：临时切流，与配置文件的声明式事实分离）。
package server

import (
	"time"

	"github.com/swsgbl/omnifusion/internal/routing"
)

// defaultPinTTL 是钉选的默认存活期（防遗忘的永久切换）。
const defaultPinTTL = 30 * time.Minute

// setPin 设置全局路由钉选（ttl<=0 用默认；provider 空 = 清除）。
func (s *Server) setPin(provider string, ttl time.Duration) {
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	if provider == "" {
		s.pinName, s.pinUntil = "", time.Time{}
		return
	}
	if ttl <= 0 {
		ttl = defaultPinTTL
	}
	s.pinName, s.pinUntil = provider, time.Now().Add(ttl)
}

// pinSnapshot 返回当前有效钉选（"" = 未钉或已过期，过期即惰性清除）。
func (s *Server) pinSnapshot() (string, time.Time) {
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	if s.pinName == "" || time.Now().After(s.pinUntil) {
		return "", time.Time{}
	}
	return s.pinName, s.pinUntil
}

// pinOption 把有效钉选转成分发选项（三端点统一注入；无钉选 = nil）。
func (s *Server) pinOption() []routing.DispatchOption {
	name, _ := s.pinSnapshot()
	if name == "" {
		return nil
	}
	return []routing.DispatchOption{routing.WithPinnedProvider(name)}
}

// setDefaultCombo 设置默认压缩组合（空 = 清除；未知组合在控制端点
// 已拒绝，此处不复查）。
func (s *Server) setDefaultCombo(name string) {
	s.defCombo.Store(&name)
}

// defaultCombo 返回默认压缩组合（"" = 未设）。
func (s *Server) defaultCombo() string {
	if p := s.defCombo.Load(); p != nil {
		return *p
	}
	return ""
}
