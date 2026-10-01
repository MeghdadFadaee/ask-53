# Contributing

Bug reports, documentation improvements, and focused pull requests are welcome.
Please use [SECURITY.md](SECURITY.md) for vulnerabilities rather than a public issue.

## Development

Use Go 1.26 or newer, Git, and `dig`. Linux and macOS are supported. The runtime
uses Unix file locking; Windows is not currently supported.

```sh
make build
make check
make test
```

For an end-to-end local session, run `make mock` in one terminal and
`./bin/ask53 -config config.local.json` in another. See [README.md](README.md)
for queries and provider configuration. Tests use fake providers and must never
require a real API key, a paid account, or a public DNS server.

Before submitting a change:

- Format Go files with `gofmt` and keep dependencies minimal.
- Run `make check` and `make test`. Run the relevant fuzz target when changing
  question parsing or TXT encoding; commands are in the README.
- Add regression coverage for meaningful behavior changes, especially limits,
  concurrency, DNS wire handling, provider failures, and shutdown.
- Update documentation when configuration or operational behavior changes.
- Keep credentials, actual client networks, local configurations, quota state,
  generated binaries, and coverage output out of commits.

Keep changes focused. Explain the problem, resulting behavior, validation, and
any operational tradeoffs in the pull request. No contributor agreement or
sign-off is required. Contributions are provided under the repository's MIT
license; third-party code must retain its own license notices.

## Invariants to preserve

- Public UDP never causes paid work or returns a TXT answer.
- Cache, client accounting, in-flight calls, waiting requests, and connections
  remain bounded; exhaustion sheds work rather than growing a queue.
- A paid attempt requires a durable reservation. Restart and uncertain provider
  outcomes do not refund attempts.
- Invalid input and provider failures do not become successful TXT answers.
- Questions, answers, provider credentials, and client addresses stay out of logs.
- No recursive resolution, DNS forwarding, tools, or unconfigured AI endpoints.

The module is currently a command-only project with local module name `ask53`.
Clone and build it from the checkout. A canonical remote module path can be set
once the repository URL is chosen; do not add a `go install ...@latest` example
with an invented URL.
