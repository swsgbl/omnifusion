# 安全策略 · Security Policy

## 支持版本 · Supported versions

最新发布线的最新一个版本（v0.1.x 的最新 tag）。

The latest tag of the current release line (v0.1.x).

## 报告漏洞 · Reporting a vulnerability

**请勿在公开 Issue 中披露安全细节。** 请使用 GitHub 的「私下报告漏洞」（Private vulnerability reporting，仓库 Security 标签页）提交；这是唯一接收渠道，仓库不含任何个人邮箱。

**Do not open a public issue with security details.** Use GitHub's Private vulnerability reporting (the repository's Security tab) — the only accepted channel; this repository publishes no personal email addresses.

报告请包含：受影响版本/提交、复现步骤或 PoC、影响评估。修复发布后我们会在 Release 说明中致谢（除非你要求匿名）。

Please include: affected version/commit, reproduction steps or PoC, and impact assessment. Fixes are credited in release notes (unless you prefer anonymity).

## 范围与硬化建议 · Scope & hardening notes

- 本网关设计为**仅本机服务**：默认只监听 127.0.0.1 回环。改绑非回环地址属于显式改配置，风险自负（网关令牌 `ofg-…` 等于完整数据面权限）。
- 密钥以 AES-256-GCM 加密落盘于本地 `data/` 目录；网关 API Key 在桌面端设置里为**明文存储**（界面已明示）。
- MCP 使用 scoped token（工具可见性按 scope 收敛）；不需要时不要开启。
- 目录 feed 为签名数据（Ed25519 pinned 公钥 + 防回滚），验签失败即丢弃；但**能力分/定价是社区维护的参考数据，不是安全边界**。

The gateway is designed to be a **local-only service** (loopback bind by default). Rebinding beyond loopback is an explicit configuration change at your own risk — the gateway token grants full data-plane access. Keys are AES-256-GCM encrypted at rest; the desktop app stores the gateway API key in plaintext (disclosed in its UI). Catalog feeds are signed data, but capability scores and prices are community reference data, not a security boundary.
