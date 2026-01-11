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
- Perp positions/margin: `clearinghouseState`

REST (POST https://api.hyperliquid.xyz/exchange):
- Place/cancel orders

WebSocket (wss://api.hyperliquid.xyz/ws):
- `allMids` (prices)
- `openOrders` (your open orders)
- `clearinghouseState` (perp positions/margin)
- `candle` (volatility filter)

## Architecture Overview
Core modules and responsibilities:
- MarketData (`internal/market`): merges REST + WS for mids, funding, volatility.
- AccountState (`internal/account`): spot balances, perp positions, open orders.
- Execution (`internal/exec`): order placement/cancel with idempotency and retries.
- RiskEngine (`internal/strategy/risk.go`): delta band, margin buffer, funding regime.
- StateMachine (`internal/strategy`): single source of truth for flow state.
- Store (`internal/state/sqlite`): restart-safe state + last action.
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
- Enforce delta neutral band, rebalance with small perp adjustments.
- Enforce margin buffer, reduce or exit if health degrades.
- Exit on funding deterioration or expected carry < fees + buffer.

EXIT flow:
- Reduce-only close perp short.
- Sell spot back to quote asset.
- Confirm flat, return to IDLE.

## Restart Safety
On startup, always reconcile:
- Spot balances (`spotClearinghouseState`).
- Perp positions (`clearinghouseState`).
- Open orders (`openOrders`).

If exposure exists: enter HEDGE_OK and fix delta.
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
- [ ] Partial fills: reconcile fills via WS events or user fills; hedge only the executed size; consider IOC for spot to avoid lingering partials.
- [ ] Funding timing: confirm hourly funding schedule and next funding timestamp; avoid closing right before a positive funding event unless risk dictates.
- [ ] Funding data sources: verify availability and schema for `predictedFundings` and `userFunding` before relying on them.
- [ ] Fees and slippage: compute expected carry net of fees/spread before entry; avoid churn when funding is low.
- [ ] Margin and collateral: verify how spot value contributes to perp margin on-chain; maintain buffers and avoid full-capital spot buys.
- [ ] WS semantics: handle `isSnapshot: true` correctly to avoid double-counting; resubscribe on reconnect.

## Roadmap Checklist
- [x] Phase 0: Scaffold repo, state machine, SQLite store, REST/WS clients, tests.
- [x] Phase 1: Implement concrete JSON decoding for `metaAndAssetCtxs`, `spotMetaAndAssetCtxs` (validated against live `/info`).
- [x] Phase 1: Populate funding rates, oracle prices, and spot context (validated against live `/info`).
- [x] Phase 1: Implement candle volatility feed and calculations (tune window/interval as needed).
- [x] Phase 2 (REST): Parse spot balances, perp positions, and open orders from /info.
- [ ] Phase 2 (WS): Parse spot/perp/open-order updates from WebSocket feeds.
- [ ] Phase 2: Persist last action and exposure in SQLite for restart safety.
- [x] Phase 2: Add signed /exchange order action (EIP-712) and CLI verification order.
- [ ] Phase 3: Add order timeouts, cancel/replace logic, and rollback on partial fills.
- [ ] Phase 3: Enforce reduce-only on exit.
- [ ] Phase 4: Delta band checks, margin buffer thresholds, connectivity kill switch.
- [ ] Phase 4: Funding regime rules and expected carry estimation.
- [ ] Phase 5: Expand structured logging fields.
- [ ] Phase 5: Add metrics counters and alert routing.
- [ ] Phase 6: Harden systemd unit and config management.
- [ ] Phase 6: Add dry-run and paper trading modes.

## Suggested Initial Parameters (Small-Cap Trial)
- Market: BTC only
- Notional: $25-$50
- Delta band: $2-$5
- Funding threshold: conservative to avoid fee churn
- Perp leverage: keep ~1x with large margin buffer
