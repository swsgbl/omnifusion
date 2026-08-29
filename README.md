# OmniFusion

**English** | [简体中文](README.zh-CN.md)

> BYOK-first · single Go binary · free-tier maximization — a high-performance AI gateway
>
> Bifrost-class performance (Go, µs-level overhead) carrying OmniRoute-class feature depth (multi-strategy routing, three-layer resilience isolation, token compression pipeline, MCP).

**Status**: ✅ M0–M6 complete (MVP / three-layer resilience / four inbound protocols / compression + semantic cache / MCP + CLI + observability / Fusion + ML routing + memory) → 🚧 M7 in progress on demand (Go SDK, A2A v1.0, Tauri desktop, Responses API inbound, quality capability ranking, and the novice-friendliness round — chat page / first-run guide / desktop key management / bundled gateway — delivered; K8s/WASM deferred); `v0.1.1` released.
> Deployment chain ready: Docker two-stage image + compose with three profiles (incl. a zero-key full-chain mock stack) + systemd units; local smoke test `sh scripts/smoke.sh` with 17 assertions.

## The problem it solves

Aggregate multiple LLM providers (free tiers first, BYOK — bring your own keys) behind one local OpenAI-compatible endpoint:

- **Never stalls**: three-layer fault isolation (provider circuit breaker ⊃ key cooldown ⊃ model lock) + buffer-first-chunk (auto-switch upstream before the first chunk; keep the stream intact after it);
- **Free-quota maximization**: multi-dimensional scoring routing (health · latency · remaining quota) drains every free tier; candidates filtered by model membership (providers whose catalog lacks the model are skipped, so a new session's first request never burns timeouts on dead upstreams); capability-ranked routing (`@quality`) auto-selects the strongest free model by community-catalog capability score;
- **Token compression**: session-dedup → tool-output folding → Caveman rule compression, optional LLMLingua-2 semantic compression, all guarded by a Fidelity Gate against degradation;
- **Agent-native**: built-in MCP server + `ofd run claude` one-command binding;
- **Trustworthy**: local-first, keys encrypted with AES-256-GCM, zero telemetry, loopback-only by default.

## Docs

- User-visible changes: [CHANGELOG.md](CHANGELOG.md);
- Deployment & orchestration: [deploy/README.md](deploy/README.md) (Docker/compose/systemd/Prometheus/Grafana);
- Desktop build: [apps/desktop](apps/desktop) (build.cmd).

## Quick start

**Option 1: download & run (recommended for everyone)**

1. Download `ofd` for your platform from [Releases](https://github.com/swsgbl/omnifusion/releases) (`ofd.exe` on Windows); desktop users simply run the `OmniFusion.Desktop` setup (gateway bundled — works out of the box);
2. Get a free key (e.g. at [OpenRouter](https://openrouter.ai/keys)) and add it: `ofd key add openrouter` (interactive, hidden input);
3. Start `ofd` and open `http://127.0.0.1:20130/dashboard/chat?key=$(ofd gateway-key)` — **built-in chat page; "⚡ Auto" by default picks the strongest free model by capability**. Chat right after install.

**Option 2: build from source (developers)**

```bash
go build -o ofd ./cmd/ofd
./ofd serve                     # 127.0.0.1:20130 by default; template in config.yaml.example
```

**Plug into your existing clients** (Claude Code / Codex / Gemini CLI — one command, gateway auto-started):

```bash
ofd run claude                  # injects env vars and launches the official CLI
ofd gateway-key                 # prints the data-plane token (ofg-…); any OpenAI-compatible client:
                                #   base_url: http://127.0.0.1:20130/v1
```

**Zero-key smoke test** (mock upstreams, 17 end-to-end assertions): `sh scripts/smoke.sh` (on Windows: `powershell -File scripts/smoke.ps1`)

**Containers** (incl. mock verification stack / Prometheus+Grafana, see deploy/README.md):

```bash
docker compose -f deploy/docker-compose.yml --profile mock up -d --build
```

## License

Apache-2.0 (see [LICENSE](LICENSE))
