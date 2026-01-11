# Repository Guidelines

## Session Start
Before making changes, read `docs/roadmap.md` and `docs/handoff.md` to align with the current architecture, risk model, and development roadmap.

## Project Structure & Module Organization
Source lives in `cmd/bot/main.go` (entrypoint) and `internal/` for app wiring + domain modules (`hl/`, `market/`, `account/`, `exec/`, `strategy/`, `state/`, `logging/`, `metrics/`, `alerts/`). Tests live alongside code as `*_test.go` (e.g., `internal/strategy/state_machine_test.go`). Ops assets are in `scripts/systemd/`. Sample config is in `internal/config/config.yaml`; runtime data is in `data/` (gitignored).

## Build, Test, and Development Commands
- `make build`: build `bin/hl-carry-bot` from `./cmd/bot`.
- `make run`: run the bot with the sample config (`internal/config/config.yaml`).
- `make test` or `go test ./...`: run unit tests across all packages.

## Coding Style & Naming Conventions
Use Go 1.22+ and keep code `gofmt`-clean (tabs, standard import grouping). Exported identifiers use `CamelCase`, unexported identifiers use `lowerCamel`. Keep packages domain-focused and prefer small interfaces to enable mocking; thread `context.Context` through network and storage paths.

## Architecture & Quality Rules
- Tests are mandatory for changes (add or update tests for new behavior and fixes).
- Keep the repo clean and modular; avoid creating \"god\" packages.
- Avoid heavy math or complex numerical methods unless explicitly required.
- Avoid heavy logging in hot paths; keep logs structured and signal-rich.
- Facts requiring on-chain verification must not be assumed; prefer on-chain/dynamic checks. Only use config assumptions or hardcoded parameters when verification yields no result or dynamicism is unsafe; document the rationale and request user confirmation if needed.

## Testing Guidelines
Use the Go `testing` package. Tests should be named `TestXxx` and placed in `*_test.go` files in the same package. There is no coverage threshold yet; prioritize strategy transitions, idempotent execution behavior, and state persistence. Run tests with `make test`.

## Commit & Pull Request Guidelines
There is no Git history yet, so no established commit convention. Use short, imperative messages; Conventional Commits are welcome (e.g., `feat(strategy): add funding gate`, `fix(exec): backoff on 429`). PRs should include a concise description, testing results, and any config or risk notes; link relevant issues when applicable.

## Security & Configuration Tips
Keep secrets out of the repo. If you add API keys or alert tokens, store them in a local config file and avoid committing it. The SQLite state file is stored under `data/` (ignored by Git). Use `scripts/systemd/hl-carry-bot.service` as a reference for deployment.
