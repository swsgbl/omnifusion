# Official catalog feed

Signed community catalog data consumed by OmniFusion gateways
(`catalog.feed_url` defaults to this directory over raw.githubusercontent):

- `feed.json` — the feed document (context windows, capability scores
  0–100 for `@quality`, declared prices for `@cheap`, community review status)
- `feed.json.sig` — detached Ed25519 signature over the exact bytes of
  `feed.json` (sidecar; the gateway falls back to it when the
  `x-catalog-signature` response header is absent)

## Trust model

Gateways pin the maintainer public key (`catalog.feed_pubkey`; the
built-in default matches the key that signs the files here). Feeds are
verified before use; version numbers are monotonic and persisted as an
anti-rollback baseline — replayed or rolled-back feeds are rejected.

## Maintenance

Regenerate and re-sign after editing `feed.json` (bump `version`):

```bash
ofd catalog sign catalog/feed.json <seed.hex>   # writes catalog/feed.json.sig
ofd catalog verify catalog/feed.json --pubkey <pubkey-hex>
```

Entries start as `probation` and graduate to `stable` based on real
traffic evidence (`ofd catalog report`).
