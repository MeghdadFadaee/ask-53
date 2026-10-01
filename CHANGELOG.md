# Changelog

## Unreleased

Initial implementation:

- Direct IN/TXT question interface with case normalization and optional namespace.
- OpenAI-compatible completion client with configurable endpoint/model/token cap.
- Bounded result cache, concurrent request coalescing, source/global rate limits,
  durable daily attempt budget, and provider circuit breaker.
- Public UDP-to-TCP fallback policy and bounded DNS protocol handling.
- Graceful shutdown, loopback health/readiness/statistics endpoints, and local mock provider.
- Race, protocol, failure, concurrency, process restart, and fuzz tests.
- Linux/macOS build support, systemd and container deployment examples, and
  versioned release archives with checksums and dependency notices.
