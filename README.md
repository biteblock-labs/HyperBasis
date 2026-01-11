# hl-carry-bot

Hyperliquid delta-neutral funding-rate carry bot scaffold (long spot + short perp).

## Requirements
- Go 1.22+

## Layout
- `cmd/bot/main.go`: entrypoint
- `cmd/verify/main.go`: tiny live order verifier (spot)
- `internal/`: app wiring, clients, strategy, state, logging
- `scripts/systemd/hl-carry-bot.service`: systemd unit

## Docs
- `docs/roadmap.md`: product plan, state machine, and rollout phases
- `docs/architecture.md`: repo architecture and module responsibilities
- `docs/handoff.md`: current state + next steps + agent prompt
- `docs/ops_runbook.md`: operations runbook (deployment + troubleshooting)

## Architecture Summary
The bot wires configuration + logging, reconciles account state at startup, consumes REST/WS market data, and runs a state machine that gates entry/exit while enforcing risk checks. Orders flow through an idempotent executor backed by a persistent store; the store also persists exchange nonces and a strategy snapshot (last action + exposure + last mids) to make restarts safer.

## Quick start
1. Copy `internal/config/config.yaml` and adjust settings (notably `strategy.perp_asset` and `strategy.spot_asset`).
2. Build: `make build`
3. Run: `./bin/hl-carry-bot -config internal/config/config.yaml`

## Verification order (optional)
1. Copy `.env.example` to `.env` and fill in `HL_WALLET_ADDRESS` + `HL_PRIVATE_KEY` (do not commit `.env`).
2. Optional: set `HL_ACCOUNT_ADDRESS`/`HL_VAULT_ADDRESS` when using subaccounts.
3. Deposit USDC into Hyperliquid and move enough USDC into the spot wallet to satisfy minimum order value (observed: 10 USDC).
4. Set `HL_VERIFY_ASSET` to a spot symbol:
   - BTC-like: `UBTC` (spot pair is `UBTC/USDC`)
   - Small/cheap spot: `PURR/USDC`
3. Run: `go run ./cmd/verify -config internal/config/config.yaml`
4. Use `-dry-run` to print the derived order without placing it.
5. If you pass any positional args after `./cmd/verify`, Go's flag parsing will ignore `-dry-run`; always use `-config` and `-dry-run` flags.

## Notes
- REST endpoints: `POST /info` and `POST /exchange`
- WS endpoint: `wss://api.hyperliquid.xyz/ws`
- WS keepalive: configure `ws.ping_interval` to avoid idle disconnects (default 50s).
- Exchange nonces are persisted in SQLite to avoid reuse after restarts (startup logs nonce key/seed).
- The bot persists a strategy snapshot (last action + exposure + last mids) to SQLite and restores strategy state on startup when available.
- `strategy.min_exposure_usd` treats small residual exposure as dust to avoid tiny exit orders / 422s.
- Placeholder types are used where schemas are unknown.

## Testing
- `make test`

## CI
- `make ci` (go vet + staticcheck + deadcode)
