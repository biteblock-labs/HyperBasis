# hl-carry-bot

Hyperliquid delta-neutral funding-rate carry bot scaffold (long spot + short perp).

## Requirements
- Go 1.22+

## Layout
- `cmd/bot/main.go`: entrypoint
- `internal/`: app wiring, clients, strategy, state, logging
- `scripts/systemd/hl-carry-bot.service`: systemd unit

## Docs
- `docs/roadmap.md`: product plan, state machine, and rollout phases
- `docs/architecture.md`: repo architecture and module responsibilities

## Architecture Summary
The bot wires configuration + logging, reconciles account state at startup, consumes REST/WS market data, and runs a state machine that gates entry/exit while enforcing risk checks. Orders flow through an idempotent executor backed by a persistent store to make restarts safe.

## Quick start
1. Copy `internal/config/config.yaml` and adjust settings.
2. Build: `make build`
3. Run: `./bin/hl-carry-bot -config internal/config/config.yaml`

## Verification order (optional)
1. Fill in `.env` with `HL_WALLET_ADDRESS` and `HL_PRIVATE_KEY`.
2. Set `HL_VERIFY_ASSET` to a spot symbol (e.g. `PURR` or `PURR/USDC`).
3. Run: `go run ./cmd/verify -config internal/config/config.yaml`
4. Use `-dry-run` to print the derived order without placing it.

## Notes
- REST endpoints: `POST /info` and `POST /exchange`
- WS endpoint: `wss://api.hyperliquid.xyz/ws`
- Placeholder types are used where schemas are unknown.

## Testing
- `make test`

## CI
- `make ci` (go vet + staticcheck + deadcode)
