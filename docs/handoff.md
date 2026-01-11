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
  - Spot balances are retrieved via WS post `/info` (`spotClearinghouseState`); there is no spot wallet subscription type.
  - WS `userNonFundingLedgerUpdates` applies spot balance deltas (spot transfers/account-class transfers) with periodic `spotClearinghouseState` reconcile (`strategy.spot_reconcile_interval`).
  - WS `userFills` feed for fill tracking with hash dedupe and LRU-capped cache; entry sizing uses WS fills with REST fallback on close/timeout.
- Execution:
  - `internal/hl/exchange`: msgpack + EIP-712 signing compatible with the official SDK.
  - Bot execution now uses signed `/exchange` for order placement/cancel.
  - USD class transfer (perp ↔ spot) supported via signed `/exchange`.
  - ENTER flow uses timeouts + rollback with fill sizing based on WS `userFills` (REST `userFillsByTime` fallback at order close/timeout).
  - EXIT flow now mirrors ENTER safety: sizes from actual spot/perp exposure, skips dust below `strategy.min_exposure_usd`, waits for fills (cancel on timeout), closes the perp leg with reduce-only, and rolls back spot on failures/partial fills before marking DONE.
  - Exchange nonces are monotonic and persisted in SQLite (startup logs nonce key/seed; warn on persistence failure).
  - Strategy snapshots (last action + exposure + last mids) are persisted in SQLite and loaded on startup to restore the state machine (avoids getting stuck in IDLE with exposure after restarts, and supports dust-aware flatness checks).
  - State machine recovers from EXIT -> HEDGE_OK when exposure remains but orders are gone.
  - `cmd/verify`: places a tiny signed spot IOC order using `.env` and auto-derived mid price.
- CI:
  - `make test` and `make ci` (vet + staticcheck + deadcode).

## Current Gaps (Blocking “Real Bot Trading”)
1) HEDGE_OK management is still minimal (no delta-band re-hedging yet; it mostly holds or exits).
2) Risk controls are still incomplete (margin/health checks and connectivity kill switches).
3) Fee-aware carry estimation and funding-regime rules to avoid churn and manage exits intelligently.

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

Since then we wired WS `userFills` for entry fill sizing (REST fallback at close/timeout), added spot wallet balance refresh via WS post `/info` (`spotClearinghouseState`), capped fill tracking with an LRU, and added monotonic nonces with SQLite persistence (startup logs nonce key/seed, warn on persistence failure).

We also implemented EXIT flow safety (wait/cancel/rollback with reduce-only, dust-aware sizing) and persisted a strategy snapshot (last action + exposure + last mids) to SQLite with startup restore so restarts don’t get stuck in IDLE with exposure.

Next engineering goals (highest priority):
1) Add delta-band re-hedging and steady-state exposure management in HEDGE_OK.
2) Add margin/health risk checks and connectivity kill switches.
3) Add fee-aware carry estimation and funding-regime rules.

Do not add heavy logging in hot paths; keep packages modular; add/update tests for all new behavior.
