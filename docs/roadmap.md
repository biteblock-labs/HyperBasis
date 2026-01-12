# hl-carry-bot Roadmap and Architecture

## Goal
Hold equal and opposite exposure so price moves cancel and PnL is dominated by funding payments when funding is positive:
- Long spot (e.g., BTC spot)
- Short BTC perpetual
- Maintain delta near zero (matched notional)

Funding is paid hourly (1/8 of the 8-hour rate). Funding payment uses oracle price for notional, so PnL estimates should use oracle or accept a small error.

## Hyperliquid Mechanics to Respect
- Funding cadence: hourly payouts derived from an 8-hour rate.
- Funding notional: size * oracle price * funding rate.
- Spot vs perp asset IDs differ (spot uses 10000 + index).
- Orders support reduce-only, TIF, and optional client order ID.
- WebSocket feeds provide snapshots (`isSnapshot: true`) and deltas.

## Minimal API Surface
REST (POST https://api.hyperliquid.xyz/info):
- Perps ctx: `metaAndAssetCtxs`
- Spot ctx: `spotMeta` and/or `spotMetaAndAssetCtxs`
- Spot balances: `spotClearinghouseState` with user address
- Open orders: `openOrders`
- User fills: `userFillsByTime` (fallback)
- User funding: `userFunding` (funding payment history; schema validated and used to log receipts)
- Perp positions/margin: `clearinghouseState`

REST (POST https://api.hyperliquid.xyz/exchange):
- Place/cancel orders

WebSocket (wss://api.hyperliquid.xyz/ws):
- `allMids` (prices)
- `openOrders` (your open orders)
- `clearinghouseState` (perp positions/margin)
- `userFills` (fills for your orders)
- `userNonFundingLedgerUpdates` (spot wallet deltas)
- `candle` (volatility filter)
- `method: "post"` `/info`: `spotClearinghouseState` (spot balances)

## Architecture Overview
Core modules and responsibilities:
- MarketData (`internal/market`): merges REST + WS for mids, funding, volatility.
- AccountState (`internal/account`): spot balances, perp positions, open orders.
- Execution (`internal/exec`): order placement/cancel with idempotency and retries.
- RiskEngine (`internal/strategy/risk.go`): delta band, margin buffer, funding regime.
- StateMachine (`internal/strategy`): single source of truth for flow state.
- Store (`internal/state/sqlite`): restart-safe KV (executor idempotency, exchange nonces, strategy snapshot).
- Logging/Metrics/Alerts (`internal/logging`, `internal/metrics`, `internal/alerts`).

## State Machine (Restart-Safe)
States:
- IDLE: flat, monitoring entry signals.
- ENTER: open two-leg position with tight timeouts.
- HEDGE_OK: steady state; enforce delta, margin, funding regime.
- EXIT: close both legs, then return to IDLE.

Entry conditions:
- Funding positive above threshold.
- Volatility below threshold.
- Flat exposure.

ENTER flow:
- Place spot buy, confirm fill (WS `openOrders`/fills or REST fallback).
- Place perp short, confirm position (WS `clearinghouseState`).
- If either leg fails to fill quickly: cancel outstanding orders, revert to flat, return to IDLE.

HEDGE_OK flow:
- Enforce delta neutral band, rebalance with small perp IOC adjustments.
- Enforce margin/health thresholds to gate actions when buffers are thin.
- Connectivity kill switch: cancel open orders and pause trading when market/account data is stale.
- Exit when net expected carry (after fees/slippage + buffer) stays below threshold for N ticks.
- Throttle entry/hedge actions with cooldowns to avoid duplicate orders while account state catches up.

EXIT flow:
- Size from actual spot/perp exposure (not notional); skip legs below `strategy.min_exposure_usd` to avoid dust/tiny orders.
- Close spot exposure (sell if long / buy if short) and wait for fill (cancel on timeout).
- Close perp exposure with reduce-only (buy if short / sell if long) and wait for fill (cancel on timeout).
- On partial/no-fill: cancel best-effort, roll back any filled spot to restore delta, return to HEDGE_OK; otherwise confirm flat (dust-aware) and return to IDLE.

## Restart Safety
On startup, always reconcile:
- Spot balances (`spotClearinghouseState`).
- Perp positions (`clearinghouseState`).
- Open orders (`openOrders`).

Exchange nonces are persisted in SQLite to avoid reuse after restarts.
The last strategy action and exposure (plus last mid prices) are persisted as a strategy snapshot and loaded on startup to restore the state machine.

If exposure exists: enter HEDGE_OK (delta-band re-hedging is a Phase 4 item; current behavior mostly holds or exits).
If orders exist but no exposure: cancel.
If exposure exists and funding is bad: exit.

## Failure Modes and Mitigations
1) Partial hedge (one leg fills, other doesn't)
- Mitigate with strict timeouts, immediate rollback, and WS confirmation.

2) Restart with open exposure
- Mitigate with hard reconcile on startup and state machine correction.

3) Funding flip or compression
- Exit on funding <= 0 for N intervals or when expected carry is negative.

## Nuances and Verification Checklist
- [x] Validate `metaAndAssetCtxs` and `spotMetaAndAssetCtxs` schemas against live `/info` responses.
- [x] Spot meta uses `tokens` mapping and many pair names are `@index`; derive base/quote from token indices.
- [x] Spot mids can use `@index` keys; retain raw names for mid lookup.
- [x] Asset IDs: perp uses universe index and spot uses 10000 + spot index (implemented per docs).
- [x] Signed `/exchange` order action verified on mainnet with a tiny spot IOC fill.
- [x] Exchange constraints observed: minimum order value (10 USDC) and tick-size enforcement for price formatting.
- [x] Dust handling: skip exits below `strategy.min_exposure_usd` to avoid tiny orders / 422s.
- [x] Funds placement matters: spot orders require spot wallet funds (`spotClearinghouseState`); perp wallet funds appear under `clearinghouseState`.
- [x] Entry rebalances USDC between spot/perp wallets before placing orders; total USDC should cover both legs.
- [x] Partial fills: reconcile fills via WS events or user fills; hedge only the executed size; consider IOC for spot to avoid lingering partials.
- [x] Exchange nonces persist in SQLite to avoid reuse on restart.
- [x] Funding timing: predictedFundings live verified (HlPerp hourly + nextFundingTime ms); exit-timing guard around nextFundingTime implemented.
- [x] Funding data sources: predictedFundings verified and parsed; `userFunding` schema verified via public address (delta-based entries) and confirmed on our own account; funding receipts are logged after each funding time.
- [x] IOC price offsets applied to entry/hedge/rollback orders to improve fill reliability.
- [x] Post-entry reconcile and entry/hedge cooldowns to avoid repeated orders during state catch-up.
- [x] Fees and slippage: compute expected carry net of fees/spread before entry; avoid churn when funding is low (uses configured bps).
- [ ] Margin and collateral: verify how spot value contributes to perp margin on-chain; maintain buffers and avoid full-capital spot buys.
- [ ] WS semantics: handle `isSnapshot: true` correctly to avoid double-counting; resubscribe on reconnect.

## Roadmap Checklist
- [x] Phase 0: Scaffold repo, state machine, SQLite store, REST/WS clients, tests.
- [x] Phase 1: Implement concrete JSON decoding for `metaAndAssetCtxs`, `spotMetaAndAssetCtxs` (validated against live `/info`).
- [x] Phase 1: Populate funding rates, oracle prices, and spot context (validated against live `/info`).
- [x] Phase 1: Implement candle volatility feed and calculations (tune window/interval as needed).
- [x] Phase 2 (REST): Parse spot balances, perp positions, and open orders from /info.
- [x] Phase 2 (WS): Parse perp/open-order updates from WebSocket feeds.
- [x] Phase 2 (WS): Parse user fills feed for order fill tracking.
- [x] Phase 2 (WS): Spot balances via WS post snapshots (`spotClearinghouseState`).
- [x] Phase 2 (WS): Spot balance deltas via `userNonFundingLedgerUpdates`.
- [x] Phase 2: Persist exchange nonces in SQLite for restart safety.
- [x] Phase 2: Persist last action and exposure in SQLite for restart safety.
- [x] Phase 2: Add signed /exchange order action (EIP-712) and CLI verification order.
- [x] Phase 2: Support USDC class transfers between perp/spot (needed to fund spot buys without draining perp margin).
- [x] Phase 3: Add ENTER/EXIT timeouts, cancel-on-timeout, and rollback on partial fills.
- [x] Phase 3: Enforce reduce-only on exit.
- [x] Phase 3: Add dust threshold (`strategy.min_exposure_usd`) to avoid tiny exit orders.
- [x] Phase 4: Delta band checks, margin buffer thresholds, connectivity kill switch.
- [x] Phase 4: Funding regime rules and expected carry estimation.
- [x] Phase 5: Expand structured logging fields.
- [x] Phase 5: Add metrics counters and alert routing.
- [x] Phase 5: Implement Telegram alert transport (Bot API sendMessage, gated by `telegram.enabled`).
- [ ] Phase 6: Harden systemd unit and config management.
- [ ] Phase 6: Telegram operator controls (status, pause/resume, safe runtime risk overrides with atomic apply + audit).
- [ ] Phase 6: Persist OHLC + position snapshots to TimescaleDB and build Grafana candlestick dashboards (ECharts/Plotly).
- [ ] Phase 6: Auto-derive config defaults (min_exposure_usd from exchange constraints, delta_band_usd from notional, risk max ages from intervals).
- [ ] Phase 6: Add dry-run and paper trading modes.

## Suggested Initial Parameters (Small-Cap Trial)
- Market: BTC only
- Notional: $25-$50
- Delta band: $2-$5
- Funding threshold: conservative to avoid fee churn
- Perp leverage: keep ~1x with large margin buffer
