# Verification record

Verified on 2026-10-01 using Go 1.26.5 on macOS arm64. No paid provider was called.

| Check | Result |
|---|---|
| Full test suite with race detector | Passed across application, AI client, config, DNS transport, and service packages |
| `go vet ./...` | Passed |
| `go mod verify` | All modules verified |
| Question parser fuzzing, 10 seconds, 2 workers | 433,221 executions; passed |
| TXT wire encoding fuzzing, 10 seconds, 2 workers | 384,336 executions; passed |
| Real `dig` through public-style UDP→TCP fallback | Returned `"Tehran"` |
| Case-insensitive repeat via built service + mock provider | Returned `"Tehran"` from cache; one provider call for the question |
| Unsupported ANY query | REFUSED; no provider call |
| Simulated HTTP 503 provider response | SERVFAIL; failed result suppressed on repeat |
| Real process SIGTERM and restart | Clean exit; persisted daily budget still enforced after restart |
| Active query drain and idle connection shutdown | Passed |
| Rebind same TCP/UDP port after shutdown | Passed |
| Static Linux amd64 and arm64 builds | Passed; both ELF binaries verified |
| Local cache-hit microbenchmark | ~35 ns/op, 0 B/op, 0 allocations on this machine; excludes DNS/network/HTTP latency |
| Offline config validation | Passed; no listeners, quota state, or provider requests created |

Tests exercise malformed DNS packets (including compression loops), unsupported classes/types/opcodes, EDNS version errors, maximum name lengths, TXT byte boundaries and escaping, UDP message ceilings, bounded TCP connections and DNS waiters, 64-way request coalescing, client/IPv6 rate accounting, failed-result caching, provider timeouts/auth/rate/server/JSON/size errors, credential redirect rejection, circuit probing and concurrent Retry-After handling, concurrent durable reservations, corrupt quota state, storage failures, date rollover/rollback, canceled callers, and shutdown/restart behavior. See the test files for reproducible scenarios.

Docker was not available here. Container execution and systemd unit startup were not performed; use the supplied deployment instructions on the target Linux host. The Linux binaries were cross-compiled, not executed locally. CI executes the automated suite on Linux and macOS. A live provider's model availability, authentication, latency, and token-field compatibility require an operator-supplied endpoint/model/key and have not been exercised.

## Public repository preparation

The repository preparation checks also passed on 2026-10-01:

- MIT license and copied dependency/runtime notices are present.
- Both GitHub workflows pass `actionlint`; action references are pinned to
  verified upstream commit hashes. Build/test jobs use read-only repository
  permissions; only the tag-triggered draft release job can write releases.
- `make check`, `make test`, and all four Linux/macOS release builds pass.
- Packaged binaries include the requested version; archives have valid SHA-256
  checksums, dependency notices, normalized ownership, and no macOS resource
  metadata, private configuration, runtime state, or IDE files.
- Publishable source and documentation were reviewed for credential patterns and
  personal absolute paths. Local binaries, release archives, and test output are
  ignored. Relative documentation links resolve.

No public release or image-registry publication was created by these checks.
Host-side branch protections, private reporting, and secret scanning are managed
separately from the source checkout.

## v0.1.0 release preparation

Local checks on 2026-10-01 passed after migrating to the canonical
`github.com/MeghdadFadaee/ask-53` module path: module checksum verification, static
analysis, and the full race/integration suite. All four v0.1.0 archives were built
and verified for checksums and exact package membership. The macOS arm64 release
binary reported `v0.1.0` and validated its bundled local configuration offline.

Release validation tests also reject invalid versions, overwrites, missing or
corrupt archives, incomplete checksum manifests, extra private files, symlinks,
and an incorrect native binary version. CI runs these packaging regressions on
Linux using an unpublished version.

The existing `main` commit `cf6ae40` passed hosted Linux/macOS CI before these
preparation changes. The new tag/draft workflow passed local static validation;
its hosted execution requires committing/pushing the prepared changes and tag.
Live provider, Docker, and systemd validation remain outside this local record.
