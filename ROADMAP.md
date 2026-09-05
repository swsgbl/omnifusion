# OmniFusion Roadmap

Last updated: 2026-09-05

OmniFusion aims to aggregate free-tier models from providers worldwide behind one local, privacy-first endpoint. The roadmap below is a commitment to focus areas, not a guarantee that every item will ship in the listed window.

## Now (September 2026)

- Keep the provider/free-tier matrix in `PROVIDERS.md` generated from the built-in registry.
- Publish a three-minute Windows desktop getting-started video.
- Add `ofd doctor`: check ports, stored keys, catalog synchronization, upstream reachability, and protocol handshakes.
- Add route explanations: expose health, latency, quota, cooldown, and model-membership inputs used by routing decisions.
- Expand screenshots and troubleshooting in the Agent/CLI recipes for Claude Code, Codex, Gemini CLI, and OpenCode.

## Next (October - November 2026)

- Turn the provider matrix into a regularly refreshed public catalog page.
- Add a free-model leaderboard combining community capability scores, observed availability, latency, and official quota boundaries.
- Publish repeatable same-machine benchmark reports from `bench/`.
- Add a provider cookbook: signup, limits, model recommendations, and fallback strategy.
- Add contribution tooling for community provider declarations.

## Later

- Optional JSON/schema/code guardrails for Agent workflows.
- Quota probing where a provider exposes a safe, official quota endpoint.
- A public comparison guide covering OmniFusion, LiteLLM, Bifrost, and new-api use cases.
- Community case studies in challenge / solution / benefit format.

## Non-goals

- OmniFusion does not sell access to upstream models.
- Core gateway features and security fixes remain open source.
- Provider terms and official free-tier limits always take precedence over marketing copy.

## Sponsorship

Sponsorship funds documentation, provider verification, benchmarking, and maintenance. Public updates and monthly transparency reports are published on Afdian:

<https://ifdian.net/a/hongfu>
