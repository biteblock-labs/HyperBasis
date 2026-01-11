# Operations Runbook — `hl-carry-bot`

This document is an operator-focused guide for running `hl-carry-bot` safely and understanding its intended trading purpose.

## Purpose (What This Bot Tries To Do)

`hl-carry-bot` aims to run a delta-neutral funding “carry” strategy on Hyperliquid:
- Long spot (e.g., `UBTC/USDC`)
- Short perpetual (e.g., `BTC` perp)
- When perp funding is sufficiently positive, hold the hedge to earn funding payments.

This is not risk-free or profit-guaranteed. Profitability depends on funding staying positive long enough to outweigh:
- Fees, spread, slippage, and execution failures
- Basis changes between spot and perp
- Liquidation / margin risk on the perp leg
- Exchange / counterparty risk and API/WS outages

## Current Reality (Read Before Running With Real Size)

The repo is a scaffold with meaningful safety work, but it is not a “set and forget” production system yet.

Notable limitations (as of this repo state):
- **EXIT flow is safer but not foolproof**: the bot sizes from actual exposure, skips dust below `strategy.min_exposure_usd`, waits for fills (cancel on timeout), closes the perp leg with reduce-only, and rolls back spot on failures/partial fills before marking the state done. If rollback fails, manual intervention may still be required.
- **Spot balance tracking is snapshot+delta-based**: `userNonFundingLedgerUpdates` applies spot deltas and the bot periodically reconciles via `spotClearinghouseState` (tune `strategy.spot_reconcile_interval` as needed).
- **Restart behavior is improved**: the bot persists a strategy snapshot (last action + exposure + last mids) to SQLite and restores the state machine on startup (including promoting IDLE → HEDGE_OK when exposure exists), but steady-state delta management is still minimal.
- Risk checks are currently minimal (notional/open-orders) and do not yet include margin health, connectivity kill-switches, or fee-aware carry estimates.

Recommendation: treat this bot as **supervised** until the roadmap items in `docs/roadmap.md` Phase 4+ are complete (delta-band hedging, margin/health checks, connectivity kill switch, fee-aware carry).

## Repo Quick Reference

- Bot entrypoint: `cmd/bot/main.go`
- Core runner: `internal/app/app.go`
- Strategy/risk/state machine: `internal/strategy/`
- Market data (REST + WS): `internal/market/`
- Account state (REST + WS): `internal/account/`
- Signed exchange client: `internal/hl/exchange/`
- WebSocket client (reconnect + resubscribe): `internal/hl/ws/`
- Persistent KV store (SQLite): `internal/state/sqlite/`
- Example config: `internal/config/config.yaml`
- systemd unit example: `scripts/systemd/hl-carry-bot.service`

## Prerequisites

- Go 1.22+ (for building/running)
- A Hyperliquid wallet funded with USDC
- Operationally: run from a dedicated machine/user and keep the private key isolated

## Secrets / Environment Variables

Both `cmd/bot` and `cmd/verify` load `.env` (if present) at startup.

Required for the bot:
- `HL_WALLET_ADDRESS`: the EVM address corresponding to the private key
- `HL_PRIVATE_KEY`: hex private key (never commit this)

Optional:
- `HL_ACCOUNT_ADDRESS`: the account to subscribe to for account state (defaults to wallet address)
- `HL_VAULT_ADDRESS`: subaccount/vault address used for signed `/exchange` actions (if applicable)

See `.env.example` for the full template.

## Configuration (`config.yaml`)

The bot is configured via YAML (see `internal/config/config.yaml`).

Key settings:
- `rest.base_url`: `https://api.hyperliquid.xyz` (mainnet) or testnet URL
- `ws.url`: `wss://api.hyperliquid.xyz/ws`
- `ws.ping_interval`: keepalive for idle WS connections (default is 50s)
- `state.sqlite_path`: local SQLite KV store path (default `data/hl-carry-bot.db`)

Strategy settings:
- `strategy.perp_asset`: perp symbol (e.g., `BTC`)
- `strategy.spot_asset`: spot symbol/pair root (e.g., `UBTC`)
- `strategy.notional_usd`: desired notional for the position sizing
- `strategy.min_funding_rate`: minimum funding rate to consider entry
- `strategy.max_volatility`: volatility gate (from candle feed)
- `strategy.min_exposure_usd`: treat smaller residuals as dust to avoid tiny exit orders / 422s
- `strategy.entry_interval`: how often to evaluate entry/exit
- `strategy.spot_reconcile_interval`: periodic spot balance refresh cadence (WS post `spotClearinghouseState`)
- `strategy.entry_timeout` / `strategy.entry_poll_interval`: how long to wait for entry fills
- `strategy.exit_on_funding_dip`: whether to exit when expected funding drops below threshold

Risk settings (currently enforced in code):
- `risk.max_notional_usd`
- `risk.max_open_orders`

Spot balance source:
- `spotClearinghouseState` is an `/info` request (HTTP) and can also be called via WebSocket `method: "post"`. It is not a WS subscription type.
- For live deltas, use `userNonFundingLedgerUpdates` (spot transfers/account-class transfers) + fills and periodically reconcile with `spotClearinghouseState` using `strategy.spot_reconcile_interval`.

## State / Data (`hl-carry-bot.db`)

The bot uses a simple SQLite KV store (table `kv`) for restart safety:
- Executor idempotency: maps `cloid:<clientOrderID>` → `<exchange order id>`
- Exchange nonces: `exchange:nonce:<baseURL>:<wallet>:<vault>` → `<last used nonce>`
- Strategy snapshot: `strategy:last_snapshot` → JSON (last action + exposure + last mids), used at startup to restore strategy state

Inspect:
```bash
sqlite3 -readonly data/hl-carry-bot.db 'SELECT key, value FROM kv ORDER BY key;'
```

Backup before upgrades:
```bash
cp data/hl-carry-bot.db data/hl-carry-bot.db.bak.$(date -u +%Y%m%dT%H%M%SZ)
```

## Local Usage

Build:
```bash
make build
```

Run:
```bash
./bin/hl-carry-bot -config internal/config/config.yaml
```

Stop:
- Ctrl+C (SIGINT) locally

### Verification Order (Recommended First)

This places a tiny signed spot IOC order to validate signing + asset IDs:
```bash
go run ./cmd/verify -config internal/config/config.yaml -dry-run
go run ./cmd/verify -config internal/config/config.yaml
```

Notes:
- Set `HL_VERIFY_ASSET` (e.g., `UBTC`) and optionally `HL_VERIFY_NOTIONAL` in `.env`.
- `cmd/verify` seeds nonces from the same SQLite path when you provide `-config` (so it won’t reuse old nonces after the bot has run).

## Deployment (systemd)

The repo includes a reference unit: `scripts/systemd/hl-carry-bot.service`.

Typical layout:
- `/opt/hl-carry-bot/hl-carry-bot` (binary)
- `/opt/hl-carry-bot/config.yaml` (config)
- `/opt/hl-carry-bot/.env` (secrets; restrict permissions)
- `/opt/hl-carry-bot/data/hl-carry-bot.db` (state)

Hardening tips:
- Run as a dedicated user (`hlbot`) with minimal permissions.
- Keep `.env` readable only by that user (`chmod 600`).
- Consider an `EnvironmentFile=` directive in the unit to load secrets from a protected file.
- Use log shipping for zap JSON logs (journald → your collector).

Watch logs:
```bash
journalctl -u hl-carry-bot -f
```

## Operational Procedures

### Startup Checklist (Small Pilot)
- Confirm wallet/private key match (bot validates this on startup).
- Ensure you have sufficient USDC for the configured `strategy.notional_usd`.
- Confirm spot wallet funding: spot buys require spot wallet USDC; the bot may transfer USDC to spot if short.
- Prefer starting **flat exposure** on first runs; on restarts with existing exposure, keep the SQLite DB so the bot can restore the last state and avoid getting stuck in IDLE-with-exposure.
- Start with small notional (e.g., $25–$50) and conservative thresholds.

### Emergency Stop
If behavior is unexpected:
- Stop the process/service immediately.
- Cancel all open orders.
- Verify current spot and perp exposure, and manually flatten if needed.

### Common Issues
- “wallet address does not match private key”: wrong `HL_WALLET_ADDRESS` or `HL_PRIVATE_KEY`.
- “spot asset not found”: mismatch between `strategy.spot_asset` and Hyperliquid’s spot symbols (often `UBTC`, not `BTC`).
- “limit price <= 0 after rounding”: invalid mid price or invalid tick-size inputs.
- Nonce errors from exchange: inspect `exchange:nonce:*` keys in SQLite; ensure only one bot instance is signing for that wallet/vault.

## Profitability Notes (How This Makes/Loses Money)

The intended edge is **positive funding** on the perp short while holding an offsetting spot long.

Rough estimate (ignoring fees/basis):
```
expectedFundingUSD ≈ notionalUSD * fundingRate
```

In practice, you must account for:
- Fees on entry/exit and any re-hedges
- Spread/slippage (especially on illiquid spot)
- Funding regime changes (can flip negative)
- Basis risk (spot/perp diverge and you may realize PnL on exit)

## Next Steps (Roadmap Alignment)

For “unattended” production readiness, prioritize:
- Delta-band re-hedging and steady-state exposure management in HEDGE_OK.
- More comprehensive risk controls: margin/health checks and a connectivity kill switch.
- Fee-aware carry estimation and funding-regime rules.

See `docs/roadmap.md` and `docs/handoff.md`.
