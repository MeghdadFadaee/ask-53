# Repository Guidelines

## Project Structure & Module Organization

Ask53 is a Go 1.26+ command-only module supporting Linux and macOS. `cmd/ask53/` manages startup and shutdown; `cmd/mock-provider/` provides a deterministic local provider. `internal/config/`, `internal/ai/`, `internal/service/`, and `internal/dnsserver/` contain configuration, provider integration, caching/budgets/rate limits, and DNS transport respectively. Tests live beside source in `*_test.go` files. `deploy/` contains systemd and configuration examples; `scripts/release.sh` packages releases. `third_party/` preserves dependency notices. Generated `bin/`, `dist/`, and `var/` directories are ignored.

## Build, Test, and Development Commands

- `make build`: build `bin/ask53`; optionally set `VERSION=v0.1.0`.
- `make check`: run `go vet ./...`.
- `make test`: run all tests with race detection and a 90-second timeout.
- `make mock`: start the loopback-only fake provider. In another terminal, run `./bin/ask53 -config config.local.json`.
- `dig @127.0.0.1 -p 5353 capital.of.iran. TXT +short`: query the local service.
- `make linux ARCH=arm64`: cross-compile a static Linux binary.
- `make release VERSION=v0.1.0`: build four platform archives and checksums without publishing.

## Coding Style & Naming Conventions

Run `gofmt -w cmd internal` after Go edits. Use tabs for Go and Makefile recipes; follow `.editorconfig` for two-space indentation in configuration and documentation. Use lowercase package names, idiomatic exported Go names, and snake_case JSON configuration fields. Keep dependencies minimal and preserve upstream license files verbatim.

## Testing Guidelines

Use Go's standard `testing` package, `httptest` providers, and local DNS sockets. Name tests `TestBehavior`, fuzz targets `FuzzBehavior`, and benchmarks `BenchmarkBehavior`. Add regression coverage for meaningful behavior changes, especially concurrency, limits, malformed packets, failures, and restart behavior. There is no fixed coverage-percentage gate. Tests must never require paid APIs or real keys. For parser changes, run the relevant `FuzzQuestion` or `FuzzTXTWire` target; see README commands.

## Commit & Pull Request Guidelines

No commits exist yet, so no historical message convention is established. Use concise imperative subjects and focused commits. Preserve existing staged work and configured commit signing. PRs should explain the problem, resulting behavior, validation, linked issues where relevant, and operational tradeoffs. Follow the PR template and update affected documentation.

## Security & Configuration

Public UDP must never initiate paid work or return TXT answers. Keep resources bounded and reserve budgets durably before provider calls. Never log questions, answers, credentials, or client addresses. Exclude secrets, private configuration, and quota state from commits. Follow `SECURITY.md` for vulnerability reporting.
