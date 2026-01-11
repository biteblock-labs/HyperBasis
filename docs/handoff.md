# Session Handoff (Current State + Next Steps)

## What We Verified Live (Mainnet)
- Deposits can appear under `clearinghouseState` (perp wallet) while `spotClearinghouseState` remains empty until funds are moved to spot.
- Spot order constraints observed:
  - Minimum order value: 10 USDC.
  - Price must be divisible by tick size (the verifier now normalizes prices).
- Spot BTC is `UBTC/USDC` (not `BTC/USDC`).
- Spot mids can be keyed by `@index` in `allMids`; spot parsing retains the raw `@index` name for mid lookup.

## Repo State (What Exists Right Now)
- Market data:
  - `/info` parsing for `metaAndAssetCtxs` + `spotMetaAndAssetCtxs` (tokens + @index names).
  - Funding + oracle prices stored; candle-based volatility window is configurable.
- Config:
  - Strategy supports separate `strategy.perp_asset` and `strategy.spot_asset` for spot/perp mapping.
- Account:
  - `/info` parsing for spot balances, perp positions, open orders.
  - WS subscriptions for `openOrders` + `clearinghouseState` with snapshot/delta handling.
- Execution:
  - `internal/hl/exchange`: msgpack + EIP-712 signing compatible with the official SDK.
  - Bot execution now uses signed `/exchange` for order placement/cancel.
  - USD class transfer (perp ↔ spot) supported via signed `/exchange`.
  - ENTER flow uses timeouts + rollback with fill sizing based on WS `userFills` (REST `userFillsByTime` fallback at order close/timeout).
  - `cmd/verify`: places a tiny signed spot IOC order using `.env` and auto-derived mid price.
- CI:
  - `make test` and `make ci` (vet + staticcheck + deadcode).

## Current Gaps (Blocking “Real Bot Trading”)
1) Spot balance updates are still REST-only (WS spot wallet feed not wired).
2) EXIT flow is still fire-and-forget (no timeouts/rollback to guarantee flatness).

## Commands (Local Dev)
- Tests: `go test ./...`
- CI checks: `make ci`
- Verification order preview: `go run ./cmd/verify -config internal/config/config.yaml -dry-run`
- Verification order place: `go run ./cmd/verify -config internal/config/config.yaml`

## Security Notes
- Keep secrets in `.env` only (gitignored); use `.env.example` as the template.
- If a private key was ever pasted into a chat/log, rotate it and fund a new wallet.

## Starter Prompt (Paste Into Next Codex Session)
You are working in the `hl-carry-bot` Go repo (Go 1.22). Read `docs/roadmap.md`, `docs/architecture.md`, and this `docs/handoff.md` first.

We already implemented live Hyperliquid `/info` parsing, candle volatility, account reconciliation parsing, and a signed `/exchange` implementation in `internal/hl/exchange` (msgpack + EIP-712) with tests. We also added `cmd/verify` and confirmed a tiny mainnet IOC spot order fill; observed min order value 10 USDC and tick-size enforcement; deposits can live in perp wallet (`clearinghouseState`) while spot wallet (`spotClearinghouseState`) stays empty until a transfer.

Next engineering goals (highest priority):
1) Implement WS spot wallet updates to keep spot balances current without REST polling.
2) Add EXIT flow timeouts/rollback to mirror ENTER safety.

Do not add heavy logging in hot paths; keep packages modular; add/update tests for all new behavior.
