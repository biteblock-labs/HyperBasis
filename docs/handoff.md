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
  - `cmd/bot` loads `.env` before config; `HL_TELEGRAM_TOKEN`/`HL_TELEGRAM_CHAT_ID` override config values, but `telegram.enabled` must be true in YAML to send alerts.
- Account:
  - `/info` parsing for spot balances, perp positions, open orders.
  - `userFunding` REST helper to pull funding payment history (docs show delta-based events; parser updated; live verification deferred until production runtime).
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
  - HEDGE_OK now rebalances delta with perp-only IOC orders when exposure drifts beyond `strategy.delta_band_usd`.
  - Exit on funding dip is deferred within `strategy.exit_funding_guard` before `nextFundingTime` when predicted funding is positive (configurable via `strategy.exit_funding_guard_enabled`).
  - Risk checks include margin/health thresholds and a connectivity kill switch that cancels open orders when market/account data goes stale.
  - Metrics counters track kill switch events and entry/exit failures; Telegram Bot API alerts fire on kill switch and entry/exit failures when enabled.
  - Prometheus metrics endpoint is enabled by default on `127.0.0.1:9001` (`/metrics`) and can be disabled via `metrics.enabled`.
  - Exchange nonces are monotonic and persisted in SQLite (startup logs nonce key/seed; warn on persistence failure).
  - Strategy snapshots (last action + exposure + last mids) are persisted in SQLite and loaded on startup to restore the state machine (avoids getting stuck in IDLE with exposure after restarts, and supports dust-aware flatness checks).
  - State machine recovers from EXIT -> HEDGE_OK when exposure remains but orders are gone.
  - `cmd/verify`: places a tiny signed spot IOC order using `.env` and auto-derived mid price.
- CI:
  - `make test` and `make ci` (vet + staticcheck + deadcode).

## Current Gaps (Before Unattended Trading)
1) Funding timing/data verification from live observations (predicted fundings verified; `userFunding` parser updated from docs; live verification deferred until production runtime when the bot holds a perp position across a funding event).
2) Telegram operator controls (alerts only; no pause/resume/status or risk override commands yet).

## Commands (Local Dev)
- Tests: `go test ./...`
- CI checks: `make ci`
- Verification order preview: `go run ./cmd/verify -config internal/config/config.yaml -dry-run`
- Verification order place: `go run ./cmd/verify -config internal/config/config.yaml`
- Funding payload fetch: `go run ./cmd/verify -config internal/config/config.yaml -user-funding -funding-hours 2`

## Security Notes
- Keep secrets in `.env` only (gitignored); use `.env.example` as the template.
- If a private key was ever pasted into a chat/log, rotate it and fund a new wallet.

## Starter Prompt (Paste Into Next Codex Session)
You are working in the `hl-carry-bot` Go repo (Go 1.22). Read `docs/roadmap.md`, `docs/architecture.md`, and this `docs/handoff.md` first.

We already implemented live Hyperliquid `/info` parsing, candle volatility, account reconciliation parsing, and a signed `/exchange` implementation in `internal/hl/exchange` (msgpack + EIP-712) with tests. We also added `cmd/verify` and confirmed a tiny mainnet IOC spot order fill; observed min order value 10 USDC and tick-size enforcement; deposits can live in perp wallet (`clearinghouseState`) while spot wallet (`spotClearinghouseState`) stays empty until a transfer.

Since then we wired WS `userFills` for entry fill sizing (REST fallback at close/timeout), added spot wallet balance refresh via WS post `/info` (`spotClearinghouseState`), capped fill tracking with an LRU, added monotonic nonces with SQLite persistence (startup logs nonce key/seed, warn on persistence failure), and implemented safer EXIT flow (wait/cancel/rollback with reduce-only, dust-aware sizing). Strategy snapshots (last action + exposure + mids) persist to SQLite and restore on startup.

Telegram alerts are implemented via Bot API `sendMessage` and wired to kill switch + entry/exit events. `cmd/bot` loads `.env` at startup; `HL_TELEGRAM_TOKEN`/`HL_TELEGRAM_CHAT_ID` override config values, but `telegram.enabled` must be true in YAML (no env override for enabling).

Next engineering goals (highest priority):
1) Live `userFunding` verification once a funding event occurs while the bot holds exposure.
2) Implement Telegram operator controls (status, pause/resume, safe runtime risk overrides with audit).
3) Persist OHLC + position snapshots to TimescaleDB and build Grafana candlestick dashboards.
4) Harden systemd unit and config management.

Do not add heavy logging in hot paths; keep packages modular; add/update tests for all new behavior.
