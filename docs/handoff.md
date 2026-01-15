# Session Handoff (Current State + Next Steps)

## What We Verified Live (Mainnet)
- Deposits can appear under `clearinghouseState` (perp wallet) while `spotClearinghouseState` remains empty until funds are moved to spot.
- Spot order constraints observed:
  - Minimum order value: 10 USDC.
  - Price must be divisible by tick size (the verifier now normalizes prices).
- Spot BTC is `UBTC/USDC` (not `BTC/USDC`).
- Spot mids can be keyed by `@index` in `allMids`; spot parsing retains the raw `@index` name for mid lookup.
- `userFunding` payload shape verified via public address (delta-based entries) and confirmed on our own account.
- Own-account funding receipt verified via `userFunding` (funding credit matches position size and rate).

## Repo State (What Exists Right Now)
- Market data:
  - `/info` parsing for `metaAndAssetCtxs` + `spotMetaAndAssetCtxs` (tokens + @index names).
  - Funding + oracle prices stored; candle-based volatility window is configurable.
- Config:
  - Strategy supports separate `strategy.perp_asset` and `strategy.spot_asset` for spot/perp mapping.
  - Entry/hedge cooldowns (`strategy.entry_cooldown`, `strategy.hedge_cooldown`) and IOC price offset (`strategy.ioc_price_bps`) are configurable.
  - `cmd/bot` loads `.env` before config; `HL_TELEGRAM_TOKEN`/`HL_TELEGRAM_CHAT_ID` override config values, but `telegram.enabled` must be true in YAML to send alerts.
- Account:
  - `/info` parsing for spot balances, perp positions, open orders.
  - `userFunding` REST helper to pull funding payment history (delta-based entries; verified on our own account).
  - WS subscriptions for `openOrders` + `clearinghouseState` with snapshot/delta handling.
  - Spot balances are retrieved via WS post `/info` (`spotClearinghouseState`); there is no spot wallet subscription type.
  - WS `userNonFundingLedgerUpdates` applies spot balance deltas (spot transfers/account-class transfers) with periodic `spotClearinghouseState` reconcile (`strategy.spot_reconcile_interval`).
  - WS `userFills` feed for fill tracking with hash dedupe and LRU-capped cache; entry sizing uses WS fills with REST fallback on close/timeout.
  - Funding receipts are polled via `userFunding` after each funding time and logged as "funding payment received".
- Execution:
  - `internal/hl/exchange`: msgpack + EIP-712 signing compatible with the official SDK.
  - Bot execution now uses signed `/exchange` for order placement/cancel.
  - USD class transfer (perp ↔ spot) supported via signed `/exchange`.
  - Entry now rebalances USDC between spot/perp wallets before placing orders; total USDC should cover spot + perp notional (≈2x `strategy.notional_usd`).
  - Entry/hedge cooldowns prevent repeated orders while account state catches up after fills.
  - Post-entry reconcile refreshes spot/perp state once fills complete.
  - IOC price offset (`strategy.ioc_price_bps`) is applied to entry/hedge/rollback IOC orders.
  - ENTER flow uses timeouts + rollback with fill sizing based on WS `userFills` (REST `userFillsByTime` fallback at order close/timeout).
  - EXIT flow now mirrors ENTER safety: sizes from actual spot/perp exposure, skips dust below `strategy.min_exposure_usd`, waits for fills (cancel on timeout), closes the perp leg with reduce-only, and rolls back spot on failures/partial fills before marking DONE.
  - HEDGE_OK now rebalances delta with perp-only IOC orders when exposure drifts beyond `strategy.delta_band_usd`.
- Exit on funding dip is deferred within `strategy.exit_funding_guard` before `nextFundingTime` when predicted funding is positive (configurable via `strategy.exit_funding_guard_enabled`).
- Risk checks include margin/health thresholds and a connectivity kill switch that cancels open orders when market/account data goes stale.
- Metrics counters track kill switch events and entry/exit failures; Telegram Bot API alerts fire on kill switch and entry/exit failures when enabled.
- Telegram operator controls poll `getUpdates` and support `/status`, `/pause`, `/resume`, and `/risk show|set|reset`; operator commands are authorized by chat ID and optional user allowlist, and are audited to SQLite.
- TimescaleDB persistence is available for OHLC + position snapshots (tables `market_ohlc`, `position_snapshots`), gated by `timescale.enabled`.
- systemd unit hardened with `EnvironmentFile`, `StateDirectory`, and sandboxing settings; ops runbook documents `/etc/hl-carry-bot` and `/var/lib/hl-carry-bot` layout.
- Prometheus metrics endpoint is enabled by default on `127.0.0.1:9001` (`/metrics`) and can be disabled via `metrics.enabled`.
- Exchange nonces are monotonic and persisted in SQLite (startup logs nonce key/seed; warn on persistence failure).
  - Strategy snapshots (last action + exposure + last mids) are persisted in SQLite and loaded on startup to restore the state machine (avoids getting stuck in IDLE with exposure after restarts, and supports dust-aware flatness checks).
  - State machine recovers from EXIT -> HEDGE_OK when exposure remains but orders are gone.
  - `cmd/verify`: places a tiny signed spot IOC order using `.env` and auto-derived mid price.
- CI:
  - `make test` and `make ci` (vet + staticcheck + deadcode).

## Current Gaps (Before Unattended Trading)
1) Build Grafana dashboards and provisioning for the TimescaleDB data.

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

We validated the `userFunding` schema via a public address (delta-based entries) and confirmed our own funding receipt on mainnet.

Since then we wired WS `userFills` for entry fill sizing (REST fallback at close/timeout), added spot wallet balance refresh via WS post `/info` (`spotClearinghouseState`), capped fill tracking with an LRU, added monotonic nonces with SQLite persistence (startup logs nonce key/seed, warn on persistence failure), and implemented safer EXIT flow (wait/cancel/rollback with reduce-only, dust-aware sizing). Strategy snapshots (last action + exposure + mids) persist to SQLite and restore on startup.

Telegram alerts are implemented via Bot API `sendMessage` and wired to kill switch + entry/exit events. `cmd/bot` loads `.env` at startup; `HL_TELEGRAM_TOKEN`/`HL_TELEGRAM_CHAT_ID` override config values, but `telegram.enabled` must be true in YAML (no env override for enabling).

Next engineering goals (highest priority):
1) Build Grafana dashboards/provisioning on top of TimescaleDB (`market_ohlc`, `position_snapshots`).
2) Auto-derive config defaults (min_exposure_usd from exchange constraints, delta_band_usd from notional, risk max ages from intervals).
3) Add dry-run and paper trading modes.

Do not add heavy logging in hot paths; keep packages modular; add/update tests for all new behavior.
