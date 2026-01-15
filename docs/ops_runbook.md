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
- Risk checks include margin/health thresholds, a connectivity kill switch, fee-aware carry estimation, and funding-regime confirmations.

Recommendation: treat this bot as **supervised** until live funding verification and operator controls (Telegram commands + dashboards) are in place.

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
- `HL_TELEGRAM_TOKEN`: bot token (used when `telegram.enabled` is true)
- `HL_TELEGRAM_CHAT_ID`: chat or channel id (bot must be admin for channels)

Telegram alerts are disabled unless `telegram.enabled` is true in config; `.env` only supplies credentials.

See `.env.example` for the full template.

## Configuration (`config.yaml`)

The bot is configured via YAML (see `internal/config/config.yaml`).

Key settings:
- `rest.base_url`: `https://api.hyperliquid.xyz` (mainnet) or testnet URL
- `ws.ping_interval`: keepalive for idle WS connections (default is 50s)
- `state.sqlite_path`: local SQLite KV store path (default `data/hl-carry-bot.db`)
- `metrics.enabled`: expose Prometheus metrics when true (default true)
- `metrics.address`: listen address for metrics (default `127.0.0.1:9001`)
- `metrics.path`: HTTP path for metrics (default `/metrics`)
- `telegram.enabled`: enable Telegram alerts (must be true to send)
- `telegram.operator_enabled`: enable Telegram operator controls (requires `telegram.enabled`)
- `telegram.operator_poll_interval`: `getUpdates` long-poll interval (e.g., `3s`)
- `telegram.operator_allowed_user_ids`: optional list of Telegram user IDs allowed to send commands
- `HL_TELEGRAM_TOKEN`: bot token (keep secret, stored in `.env`)
- `HL_TELEGRAM_CHAT_ID`: chat or channel id (bot must be admin for channels, stored in `.env`)

Strategy settings:
- `strategy.perp_asset`: perp symbol (e.g., `BTC`)
- `strategy.spot_asset`: spot symbol/pair root (e.g., `UBTC`)
- `strategy.notional_usd`: desired notional for the position sizing
- `strategy.min_funding_rate`: minimum funding rate to consider entry
- `strategy.max_volatility`: volatility gate (from candle feed)
- `strategy.fee_bps`: estimated per-leg fee (basis points), used for carry estimation
- `strategy.slippage_bps`: estimated per-leg slippage (basis points), used for carry estimation
- `strategy.carry_buffer_usd`: extra USD buffer required after estimated costs
- `strategy.funding_confirmations`: consecutive ticks above thresholds before entry
- `strategy.funding_dip_confirmations`: consecutive ticks below thresholds before exit
- `strategy.delta_band_usd`: delta drift band before re-hedging with perp IOC (default `max(2, notional_usd*0.05)`)
- `strategy.min_exposure_usd`: treat smaller residuals as dust to avoid tiny exit orders / 422s (default 10 USDC)
- `strategy.entry_interval`: how often to evaluate entry/exit
- `strategy.spot_reconcile_interval`: periodic spot balance refresh cadence (WS post `spotClearinghouseState`)
- `strategy.entry_timeout` / `strategy.entry_poll_interval`: how long to wait for entry fills
- `strategy.exit_on_funding_dip`: whether to exit when expected funding drops below threshold
- `strategy.exit_funding_guard`: minimum time before `nextFundingTime` to defer exits when predicted funding is positive
- `strategy.exit_funding_guard_enabled`: toggle for the exit funding guard (default true)

Risk settings (currently enforced in code):
- `risk.max_notional_usd`
- `risk.max_open_orders`
- `risk.min_margin_ratio`: gate trading when reported margin ratio falls below this threshold
- `risk.min_health_ratio`: gate trading when account health ratio falls below this threshold
- `risk.max_market_age`: kill switch if market data age exceeds this window (default `max(entry_interval*4, ws.ping_interval*2)`)
- `risk.max_account_age`: kill switch if account data age exceeds this window (default `max(spot_reconcile_interval*2, entry_interval*4, ws.ping_interval*2)`)

Timescale settings (telemetry storage):
- `timescale.enabled`: enable TimescaleDB persistence for OHLC + position snapshots
- `timescale.dsn`: PostgreSQL/Timescale connection string (or `HL_TIMESCALE_DSN`)
- `timescale.schema`: schema for tables (default `public`)
- `timescale.queue_size`: in-memory write queue size
- `timescale.max_open_conns` / `timescale.max_idle_conns` / `timescale.conn_max_lifetime`

## Telegram Operator Controls
Enable `telegram.operator_enabled` and send these commands in the configured chat:
- `/status`: show current state, balances, funding, cooldowns, and last funding receipt
- `/pause`: pause new entry/hedge actions
- `/resume`: resume new trading actions
- `/risk show`: show effective and override risk values
- `/risk set key=value ...`: override risk limits (keys: `max_notional_usd`, `max_open_orders`, `min_margin_ratio`, `min_health_ratio`, `max_market_age`, `max_account_age`)
- `/risk reset`: clear overrides

Operator commands are audited in SQLite (`ops:audit:*`) and offsets are persisted (`telegram:operator:last_update_id`).

Spot balance source:
- `spotClearinghouseState` is an `/info` request (HTTP) and can also be called via WebSocket `method: "post"`. It is not a WS subscription type.
- For live deltas, use `userNonFundingLedgerUpdates` (spot transfers/account-class transfers) + fills and periodically reconcile with `spotClearinghouseState` using `strategy.spot_reconcile_interval`.

## TimescaleDB + Grafana (Tailscale)

Timescale tables are created automatically when `timescale.enabled` is true:
- `market_ohlc` (OHLC per candle interval)
- `position_snapshots` (bot state + exposure)

Example DSN:
```
postgres://hlbot:secret@127.0.0.1:5432/hl_carry?sslmode=disable
```

Grafana via Tailscale (do not bind to localhost):
- Bind Grafana to your Tailscale IP (this host): `100.116.249.72`
- Example env:
```
GF_SERVER_HTTP_ADDR=100.116.249.72
GF_SERVER_HTTP_PORT=3000
```

Access from mobile: `http://100.116.249.72:3000`

Template: `scripts/grafana/grafana.env.example`

Dashboard import: `scripts/grafana/hl-carry-bot-dashboard.json`

Provisioning (recommended):
- Copy `scripts/grafana/provisioning/datasources/timescale.yaml` to `/etc/grafana/provisioning/datasources/`.
- Copy `scripts/grafana/provisioning/dashboards/hl-carry-bot.yaml` to `/etc/grafana/provisioning/dashboards/`.
- Copy the dashboard JSON from `scripts/grafana/provisioning/dashboards/hl-carry-bot/` to `/etc/grafana/provisioning/dashboards/hl-carry-bot/`.
- Set the `GF_TIMESCALE_*` vars from `scripts/grafana/grafana.env.example` in your Grafana environment.
- Set `PROVISIONING_CFG_DIR` to the same provisioning root used above (ex: `/etc/grafana/provisioning`) so the dashboard provider path resolves.
- If the OHLC/Volume panels are empty, widen the Grafana time range to cover at least the candle interval (default `1h`) or shorten `strategy.candle_interval`; the "Latest Candle" panel shows the newest stored candle.

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
- `/etc/hl-carry-bot/config.yaml` (config)
- `/etc/hl-carry-bot/hl-carry-bot.env` (secrets; restrict permissions)
- `/var/lib/hl-carry-bot/hl-carry-bot.db` (state, via `StateDirectory=hl-carry-bot`)

Quick setup:
```bash
sudo install -m 755 -d /opt/hl-carry-bot /etc/hl-carry-bot
sudo install -m 600 scripts/systemd/hl-carry-bot.env.example /etc/hl-carry-bot/hl-carry-bot.env
sudo install -m 644 internal/config/config.yaml /etc/hl-carry-bot/config.yaml
sudo install -m 644 scripts/systemd/hl-carry-bot.service /etc/systemd/system/hl-carry-bot.service
sudo systemctl daemon-reload
sudo systemctl enable --now hl-carry-bot
```

Remember to update `/etc/hl-carry-bot/config.yaml`:
- Set `state.sqlite_path` to `/var/lib/hl-carry-bot/hl-carry-bot.db`.
- Tune `strategy.perp_asset`, `strategy.spot_asset`, and `strategy.notional_usd`.

Repo-based unit (development):
- Use `scripts/systemd/hl-carry-bot.repo.service` if you want systemd to read `.env` + `config.yaml` from a working copy.
- Update the `WorkingDirectory`, `EnvironmentFile`, `ExecStart`, and `ReadWritePaths` values in that file to match your local repo path and user.

Hardening tips:
- Run as a dedicated user (`hlbot`) with minimal permissions.
- Keep `/etc/hl-carry-bot/hl-carry-bot.env` readable only by that user (`chmod 600`).
- The unit already uses `EnvironmentFile=` and `StateDirectory=`; avoid storing secrets in the repo or `/opt`.
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
- Live `userFunding` verification once the bot holds exposure across a funding event.
- Grafana dashboards and alerting on top of TimescaleDB.

See `docs/roadmap.md` and `docs/handoff.md`.
