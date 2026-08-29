package main

import (
	"log/slog"
	"os"

	"github.com/swsgbl/omnifusion/internal/provider"
	"github.com/swsgbl/omnifusion/internal/provider/registry"
	"github.com/swsgbl/omnifusion/internal/routing"
	"github.com/swsgbl/omnifusion/internal/security"
	"github.com/swsgbl/omnifusion/internal/store"
)

// buildRouter 从内置注册表装配分发器：只实例化凭据齐备的 provider。
// 凭据优先级（M1.7）：keyring（connections 表，AES-256-GCM 密文，
// 取用时才解密）→ 环境变量（key_env / vars_env）。无可用 provider
// 时返回空 Router，网关仍可启动，/v1/chat/completions 返回 503。
// 第二返回值是 provider → key 来源描述（M4.8 Dashboard keys 页注入）。
func buildRouter(log *slog.Logger, st *store.Store, kr *security.Keyring) (*routing.Router, map[string]string) {
	entries, err := registry.Load()
	if err != nil {
		log.Error("load provider registry", "err", err)
		return &routing.Router{Log: log}, nil
	}

	var providers []provider.Provider
	quota := routing.NewQuotaTracker()
	keySources := map[string]string{}
	for _, e := range entries {
		creds := registry.Credentials{Vars: map[string]string{}}
		if key, ok := keyringKey(log, st, kr, e.ID); ok {
			creds.Key = key
			keySources[e.ID] = "stored"
		} else if e.KeyEnv != "" && os.Getenv(e.KeyEnv) != "" {
			creds.Key = os.Getenv(e.KeyEnv)
			keySources[e.ID] = "env:" + e.KeyEnv
		} else if e.OptionalKey {
			keySources[e.ID] = "-"
		} else {
			keySources[e.ID] = "none"
		}
		for varName, envName := range e.VarsEnv {
			if v := os.Getenv(envName); v != "" {
				creds.Vars[varName] = v
			}
		}
		if creds.Key == "" && !e.OptionalKey {
			continue // 未配置凭据：等 `ofd key add`
		}
		p, err := registry.Build(e, creds)
		if err != nil {
			log.Warn("skip provider", "provider", e.ID, "err", err)
			continue
		}
		providers = append(providers, p)
		if l := e.RateLimits; l.RPM > 0 || l.RPD > 0 || l.TPM > 0 || l.TPD > 0 {
			quota.SetLimit(e.ID, routing.QuotaLimits{
				RPM: l.RPM, RPD: l.RPD, TPM: l.TPM, TPD: l.TPD,
			})
		}
		log.Info("provider ready", "provider", e.ID)
	}

	// M2.2：三层隔离状态机（冷却/锁定持久化于 SQLite，重启恢复）。
	iso, err := routing.NewIsolation(st, log)
	if err != nil {
		log.Error("init isolation state machine; degrade to no isolation", "err", err)
		iso = nil
	}
	return &routing.Router{Providers: providers, Log: log, Isolation: iso, Quota: quota, Scoring: routing.NewScorer(), Sessions: routing.NewSessionTracker()}, keySources
}

// buildCatalog 装配模型目录（M3.5）：live 拉取用 router 里已实例化的
// provider；静态回落与 free_meta 取自注册表声明（ErrNotSupported 的
// 原生协议家在 M6 前靠静态清单）。
func buildCatalog(log *slog.Logger, st *store.Store, r *routing.Router) *routing.Catalog {
	static := map[string][]provider.ModelInfo{}
	freeMeta := map[string]string{}
	entries, err := registry.Load()
	if err != nil {
		log.Warn("catalog falls back to live lists only; registry unavailable", "err", err)
		entries = nil
	}
	for _, e := range entries {
		if l := e.StaticModels(); len(l) > 0 {
			static[e.ID] = l
		}
		if e.FreeTier != "" {
			freeMeta[e.ID] = e.FreeTier
		}
	}
	return routing.NewCatalog(r.Providers, static, freeMeta, st, log)
}

// keyringKey 解密 connections 表中对应 provider 的密钥；未存储或
// 解密失败返回 ok=false（调用方回退环境变量）。
func keyringKey(log *slog.Logger, st *store.Store, kr *security.Keyring, providerID string) (string, bool) {
	if st == nil || kr == nil {
		return "", false
	}
	conn, err := st.GetConnection(providerID)
	if err != nil {
		log.Warn("keyring lookup failed", "provider", providerID, "err", err)
		return "", false
	}
	if conn == nil {
		return "", false
	}
	plain, err := kr.Decrypt(conn.KeyCipher)
	if err != nil {
		log.Warn("keyring decrypt failed; stored key unusable",
			"provider", providerID, "err", err)
		return "", false
	}
	return string(plain), true
}
