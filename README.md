# OmniFusion

**English** | [简体中文](README.zh-CN.md)

> BYOK-first · single Go binary · free-tier maximization — a high-performance AI gateway
>
> Bifrost-class performance (Go, µs-level overhead) carrying OmniRoute-class feature depth (multi-strategy routing, three-layer resilience isolation, token compression pipeline, MCP).

**In one line**: aggregate free tiers from 12 LLM providers behind one local endpoint (OpenAI / Anthropic / Gemini / Responses inbound), with a built-in chat page, bilingual dashboard, desktop app and CLI — chat right after install, never stall.

## The problem it solves

Aggregate multiple LLM providers (free tiers first, BYOK — bring your own keys) behind one local OpenAI-compatible endpoint:

- **Never stalls**: three-layer fault isolation (provider circuit breaker ⊃ key cooldown ⊃ model lock) + buffer-first-chunk (auto-switch upstream before the first chunk; keep the stream intact after it);
- **Free-quota maximization**: multi-dimensional scoring routing (health · latency · remaining quota) drains every free tier; candidates filtered by model membership (providers whose catalog lacks the model are skipped, so a new session's first request never burns timeouts on dead upstreams); capability-ranked routing (`@quality`) auto-selects the strongest free model by community-catalog capability score;
- **Token compression**: session-dedup → tool-output folding → Caveman rule compression, optional LLMLingua-2 semantic compression, all guarded by a Fidelity Gate against degradation;
- **Agent-native**: built-in MCP server + `ofd run claude` one-command binding;
- **Trustworthy**: local-first, keys encrypted with AES-256-GCM, zero telemetry, loopback-only by default.

## Docs

- User-visible changes: [CHANGELOG.md](CHANGELOG.md);
- Deployment & orchestration: [deploy/README.md](deploy/README.md) (Docker/compose/systemd/Prometheus/Grafana, incl. a zero-key mock verification stack and smoke script);
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

## Design references

OmniFusion is an **independent, original implementation** (not a fork of any existing project). Its architecture and features draw on ideas and lessons from several excellent open-source projects:

- **[Bifrost](https://github.com/maximhq/bifrost)** — the per-provider dedicated HTTP client isolation philosophy;
- **[RouteLLM](https://github.com/LMCache/RouteLLM)** — the weak/strong split and confidence-threshold ML routing ideas;
- **[OmniRoute](https://github.com/diegosouzapw/OmniRoute)** — provider registry layout and free-tier fact-checking reference;
- **[FreeLLMAPI](https://github.com/Shaivpidadi/FreeLLMAPI)** — the ideas behind signed catalog feeds and QUORUM synthesis gating;
- **[FreeRide](https://github.com/Shaivpidadi/FreeRideV3)** (MIT) — the `ofd run` CLI-wrapper flow is ported from it (original Go rewrite; attribution in [NOTICE](NOTICE)).

All of the above are idea- and lesson-level references; third-party **code** dependencies are limited to the components listed in [NOTICE](NOTICE).

## Privacy & compliance

- **Local-first**: keys encrypted at rest (AES-256-GCM), session data / audit logs / cache in the local `data/` directory, loopback-only by default; zero telemetry — the code contains no analytics or reporting.
- **Outbound connections, two kinds only**: ① the LLM upstreams you configure (your own keys; request content is subject to each upstream's privacy policy); ② the periodic official catalog feed fetch (public data, fails silently to the previous state, can be disabled in config).
- **Third-party services disclaimer**: model outputs are generated by the respective upstream providers, who own their content, availability, quotas and terms of service; you are responsible for complying with the terms of each configured provider and with applicable local laws (especially regarding free-tier usage).
- **Provided "as is"**: no warranty per Apache-2.0 §7; see [LICENSE](LICENSE), third-party notices in [NOTICE](NOTICE), and the vulnerability-reporting process in [SECURITY.md](SECURITY.md).

## License

Apache-2.0 (see [LICENSE](LICENSE))
