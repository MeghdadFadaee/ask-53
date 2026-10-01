# Ask53

A small AI question service accessed using DNS TXT queries. Go 1.26+, Linux or macOS. One process, one pinned DNS dependency, no database or queue service.

```sh
dig @127.0.0.1 -p 5353 capital.of.iran. TXT +short
# "Tehran"
```

[Quick start](#run-locally-without-a-paid-api) · [Configuration](#configuration) · [Deployment](#linux-deployment-on-port-53) · [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md)

The question becomes `capital of iran`. A configured OpenAI-compatible Chat Completions provider supplies the answer. AI output is probabilistic; the transport does not make its answers authoritative facts.

## Design and constraints

DNS names are case-insensitive and cannot carry arbitrary prose. This interface deliberately accepts only ASCII letters, digits, and hyphens in nonempty labels. Dots separate words; hyphens remain hyphens. Uppercase normalizes to lowercase before caching and provider calls. Escapes, underscores, raw Unicode, binary labels, the root name, and empty questions are refused. Punycode is accepted as literal ASCII, not automatically decoded. There is no secret-token label or arbitrary-text encoding scheme.

DNS limits are 63 bytes per label and 255 bytes per encoded name (253 presentation characters, or 254 including the final dot). Answers use **one TXT record**, split into UTF-8-safe character strings of at most 255 wire bytes. Concatenate its quoted strings without adding spaces. The default entire answer limit is 256 bytes, configurable up to 2048. Overlong, empty, invalid, or token-truncated provider responses produce `SERVFAIL`; we never silently shorten a potentially meaningful answer.

**Internet-facing UDP never starts AI work or returns TXT answers**, even on a cache hit. An allowed, valid TXT query receives a minimal `TC=1` response, prompting standard clients such as `dig` to retry over TCP. A completed TCP handshake makes source-address spoofing much harder. Loopback clients may receive UDP answers by default. `udp_mode: "tcp-only"` also enables the fallback policy locally. TCP and UDP listen on the same port. UDP replies respect the client's EDNS buffer, the 512-byte floor, and a server ceiling of 1232 bytes. Oversized loopback UDP answers omit the entire TXT record and set TC; a subsequent TCP retry uses the cached result. EDNS version errors use `BADVERS`; cookies, client subnet, DNSSEC flags, and other request options are not echoed.

This is a **direct-query application endpoint**, not a recursive resolver or a complete authoritative zone server. It never resolves a question's name, forwards DNS traffic, performs updates/transfers, or advertises recursion or DNSSEC validation. Only one IN/TXT question is supported. ANY/A/AAAA/AXFR/IXFR/other types are refused. The optional `zone` is a namespace filter, not zone delegation support; AA remains clear. Keep conventional DNS hosting for the A/AAAA records of `ask.example.com`, then query this service with `@ask.example.com`. Ordinary recursive lookups of `capital.of.iran.ask.example.com` are not supported by this design.

## Run locally, without a paid API

Install Go 1.26 or newer and `dig` (`dnsutils` on Debian/Ubuntu, `bind-utils` on many RPM systems; macOS includes it).

Clone the source before running the commands below:

```sh
git clone https://github.com/MeghdadFadaee/ask-53.git
cd ask-53
```

In terminal one, start the deterministic fake provider:

```sh
go run ./cmd/mock-provider
```

In terminal two, build and start Ask53:

```sh
make build
./bin/ask53 -config config.local.json
```

The fake provider answers `capital of iran` → `Tehran`, `capital of france` → `Paris`, and everything else → `Unknown`. `provider fail` simulates HTTP 503. It is a local test tool and cannot listen on a public address. No API key or Internet access is needed at runtime in this mode. The initial Go build downloads pinned dependencies.

```sh
dig @127.0.0.1 -p 5353 capital.of.iran. TXT +short
dig @127.0.0.1 -p 5353 CAPITAL.of.IRAN. TXT +tcp +short
dig @127.0.0.1 -p 5353 capital.of.france. TXT +time=10 +tries=1
curl -s http://127.0.0.1:9090/stats
curl -s http://127.0.0.1:8081/stats
```

Repeated and differently cased queries share a cached answer. Compare Ask53's `ai_calls` and the fake provider's `calls`; repeat requests should not increase either. The local test still maintains its budget in `var/budget.json`. Use a separate state file for experiments that require a new budget; retain the real deployment's state across restarts.

Press Ctrl-C for graceful shutdown. SIGTERM is also supported.

## Connect a real provider

Supply the **complete POST URL**, including `/v1/chat/completions` if required by your provider. There is no automatic URL suffix, model selection, or endpoint discovery.

```sh
export ASK53_AI_ENDPOINT=https://api.openai.com/v1/chat/completions
export ASK53_MODEL=your-provider-model
# Prefer a protected file or your secret manager rather than shell history.
export ASK53_API_KEY_FILE=/absolute/path/to/api-key
./bin/ask53 -check-config
./bin/ask53
```

`ASK53_API_KEY` can supply the key directly and overrides the key file. Never put keys in DNS names. A key is optional for local providers that do not need one. HTTPS with system certificate verification is required for remote providers unless `allow_insecure_ai` is explicitly enabled. HTTP is permitted automatically only for literal loopback endpoints. HTTP redirects are never followed, preventing credential forwarding. Standard `HTTP_PROXY`, `HTTPS_PROXY`, and `NO_PROXY` apply through Go's HTTP transport.

We use Chat Completions for cross-provider compatibility rather than the OpenAI-specific Responses interface. Requests contain a concise-answer system message, the normalized question as a user message, `n=1`, `stream=false`, and a required token cap. `token_field` defaults to `max_completion_tokens`; use `max_tokens` for older compatible providers. Only one choice with textual content and `finish_reason: "stop"` is accepted. Temperature, tools, and provider-specific options are omitted. An optional `reasoning_effort` can be set if your selected model supports it. A 128-token cap may be insufficient for reasoning models: select a suitable low-latency model and test its settings rather than removing the cap.

## Configuration

Use `-config /path/config.json`. Missing fields keep safe defaults; unknown fields, invalid values, null/non-object files, and trailing JSON are errors. Duration fields are strings such as `"8s"` or `"1h"`. Validate without opening listeners, creating budget files, or calling the provider:

```sh
./bin/ask53 -config deploy/config.example.json -check-config
```

The production example deliberately requires editing its model, source CIDRs, endpoint, and key file. `-check-config` checks syntax and limits, and reads a configured key file; it does not verify model availability, provider authentication, state-directory permissions, or port availability.

Only these environment overrides are supported: `ASK53_LISTEN`, `ASK53_ADMIN_LISTEN`, `ASK53_AI_ENDPOINT`, `ASK53_MODEL`, `ASK53_API_KEY_FILE`, and `ASK53_API_KEY`. Overrides, including empty values, take precedence over JSON. Other operational settings belong in the configuration file. Reload by restarting; there is no unsafe hot reload or SIGHUP behavior.

| Setting | Default | Purpose |
|---|---|---|
| `listen` | `127.0.0.1:5353` | Literal bind IP and port for UDP and TCP; port 0 is for tests |
| `admin_listen` | `127.0.0.1:9090` | Loopback-only HTTP operational endpoints; empty disables it |
| `allowed_cidrs` | `127.0.0.0/8`, `::1/128` | Client source allowlist, enforced before DNS processing |
| `udp_mode` | `loopback` | Only loopback gets UDP answers; `tcp-only` forces TCP everywhere |
| `zone` | empty | Optional suffix, e.g. `ask.example.com.`; removed from questions |
| `ai_endpoint`, `model` | required | Complete completion URL and explicit provider model |
| `api_key_file` | empty | Protected file; trailing whitespace removed |
| `allow_insecure_ai` | false | Explicit opt-in for remote plaintext HTTP |
| `token_field`, `max_tokens` | `max_completion_tokens`, 128 | Provider-side cap (1–4096 tokens) |
| `reasoning_effort` | empty | Optional model-specific reasoning setting |
| `ai_timeout` | `8s` | Entire provider request deadline, including response body |
| `request_timeout` | `9s` | Maximum DNS answer wait; must be at least AI timeout |
| `shutdown_timeout` | `12s` | Concurrent listener drain deadline; at least request timeout |
| `cache_ttl`, `failure_ttl` | `1h`, `5s` | Successful result TTL and internal failed-result suppression |
| `cache_entries` | 4096 | Combined bounded LRU for successes and failures |
| `max_answer_bytes` | 256 | UTF-8 answer byte cap, maximum 2048 |
| `max_inflight` | 8 | Concurrent distinct provider calls; no work queue |
| `max_handlers` | 128 | DNS callers allowed to wait, including duplicate callers |
| `max_tcp_connections` | 128 | Maximum admitted TCP connections |
| `tcp_idle_timeout` | `5s` | Timeout between queries; first TCP read timeout is 2 seconds |
| `client_entries`, `client_idle_ttl` | 4096, `10m` | Bounded source-rate table and idle retention |
| `ingress_rate` | 200/s, burst 400 | Global pre-parsing UDP packets plus admitted TCP connections |
| `client_rate` | 10/s, burst 20 | Per-source TCP/loopback requests, including cache hits |
| `ai_rate` | 1/s, burst 4 | Global admission rate for new paid attempts |
| `client_ai_rate` | 0.1/s, burst 2 | Per-source rate for new paid attempts |
| `daily_calls`, `budget_file` | 100, `var/budget.json` | Durable UTC-day attempt cap and state file |
| `circuit_failures`, `circuit_cooldown` | 3, `30s` | Provider circuit threshold and cooldown |

All rate settings use `{"per_second": NUMBER, "burst": INTEGER}`. Zeros do not mean unlimited; they are rejected. Values and memory/concurrency settings have validated upper bounds. Only verified TCP and trusted loopback requests allocate source-rate entries; spoofed public UDP cannot fill the client table. Source accounting groups IPv4 by address and IPv6 by /64; IPv4-mapped IPv6 normalizes to IPv4. Clients sharing a NAT share limits. A full client table rejects new sources instead of evicting active rate limits. Idle entries are pruned only after their buckets could fully refill.

## Request and failure behavior

1. Source allowlist and ingress rate checks run before creating UDP handler goroutines or admitting TCP connections. Malformed short packets are dropped; parseable malformed headers can receive minimal errors.
2. Validate the question, class, type, opcode, and additional section. Public UDP gets only a minimal TCP fallback indication.
3. Admit a bounded DNS waiter. Return unexpired cached success or failure, or join an existing identical provider call.
4. On a new miss, enforce provider capacity, circuit state, global/source AI rate, and durable daily budget. No queue grows behind a slow provider.
5. Persist a call reservation, then make one HTTP request with a bounded body (64 KiB), bounded headers (16 KiB), connection pool, dial/TLS deadlines, and full-call deadline. There are no application-level retries, so a timeout cannot automatically double the paid work.
6. Validate and normalize answer whitespace/control characters, cache the result, and release all waiters. DNS TTL counts down from the original completion time; hits do not extend freshness.

A timed-out DNS waiter does not cancel work shared with other callers; a successful result can still populate the cache. Shutdown cancels remaining AI work after listener draining. Failures are never successful TXT error messages. Provider bodies, keys, questions, answers, and client addresses are excluded from logs and HTTP stats. Provider failures log only a bounded category/status. Questions still travel in plaintext DNS and are sent to the configured provider; this service is not a channel for private data.

Three completed consecutive provider failures open the circuit. One probe is admitted after its cooldown, and success recovers it. A provider `Retry-After` also opens an immediate cooldown, capped at one hour; an older concurrent success cannot cancel that cooldown. Cached answers remain usable during circuit, rate, and budget exhaustion. Error caching suppresses identical failing questions for five seconds. No stale-success serving or background refresh is performed, avoiding extra hidden spending.

| Condition | DNS behavior |
|---|---|
| Good answer | NOERROR with one TXT record |
| Allowed public UDP / `tcp-only` | NOERROR, TC set, no answer; retry TCP |
| Unsupported name, namespace, class, or record type | REFUSED |
| Unsupported opcode | NOTIMP |
| Parseable malformed request | FORMERR; severely malformed packets may be dropped |
| EDNS version other than zero | BADVERS with EDNS version zero |
| AI error, capacity/rate/budget rejection, timeout | SERVFAIL, no answer |
| Denied source or client request-rate exhaustion | UDP silently dropped / TCP closed |
| TCP connection cap or oversized TCP request | Connection closed |

Requests at or above 1232 received bytes are dropped/closed before parsing. This comfortably accommodates a maximum-length question and ordinary EDNS. TCP frames are inherently at most 65535 bytes and are checked before DNS record parsing; the library can temporarily allocate that much per admitted connection. A connection serves at most 100 queries. Provider and waiter concurrency, result cache, client table, and connection count all remain bounded.

## Durable spending limit

Each new provider attempt consumes a reservation **before** HTTP transmission. Failures, cancellations, and uncertain timeouts still count: a disconnected client does not prove the provider did not bill the request. Budget writes use a private temporary file, file sync, atomic rename, and directory sync. A local advisory lock prevents two processes from using the same state file. Corrupt state prevents startup; write failure disables new AI attempts until the process restarts with healthy storage. A backward UTC date refuses new reservations until time catches up. UTC rollover occurs lazily on the next attempt.

Keep the state directory on a durable local filesystem and keep it across releases, reboots, and containers. Do not delete/edit budget files during normal operation or use an ephemeral volume. Use a single instance per budget; multiple independent files mean independent budgets. Distributed replicas require a separate shared quota/coalescing design, not an NFS directory.

The limit is **attempts, not dollars**. Choose the provider's model and token cap carefully, and set provider-side project spending limits. Provider billing, token-cap enforcement, clock correctness, and operator deletion of state are outside this process's guarantees. Token buckets, circuits, failure caches, and successful caches are in memory and reset at restart; the daily budget survives. Restart can therefore cause a cache miss, but cannot bypass the retained attempt cap.

## Linux deployment on port 53

The systemd deployment runs as a dedicated unprivileged user with only permission to bind privileged ports. It uses a private persistent state directory, read-only system files, a memory limit, and automatic restart on failure.

Build for your architecture (`arm64` is also supported):

```sh
make linux
# or: CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/ask53-linux-arm64 ./cmd/ask53
```

On the target Linux server:

```sh
sudo useradd --system --home /var/lib/ask53 --shell /usr/sbin/nologin ask53
sudo install -m 0755 bin/ask53-linux-amd64 /usr/local/bin/ask53
sudo install -d -m 0750 -o root -g ask53 /etc/ask53
sudo install -m 0640 -o root -g ask53 deploy/config.example.json /etc/ask53/config.json
# Create /etc/ask53/api-key with your secret, owner root:ask53, mode 0640.
# Edit /etc/ask53/config.json: model, endpoint, allowed source networks, budget.
sudo install -m 0644 deploy/ask53.service /etc/systemd/system/ask53.service
sudo -u ask53 /usr/local/bin/ask53 -config /etc/ask53/config.json -check-config
sudo systemctl daemon-reload
sudo systemctl enable --now ask53
sudo journalctl -u ask53 -f
```

The example includes a documentation-only source IP (`198.51.100.42`), not a real client grant. Replace it with your client/VPN network. An explicitly open service can set `allowed_cidrs` to `["0.0.0.0/0", "::/0"]`, but any reachable TCP client can then ask questions up to the configured limits. An allowlist or VPN is the recommended deployment. DNS has no built-in authenticated end-user identity here; reachability is not authorization.

Allow **both TCP and UDP port 53** in the host and cloud firewall for the approved networks. Keep the admin port private. Add connection/packet limits at the network firewall for floods and slow-client attacks; application limits cannot prevent NIC saturation or TCP SYN floods. Under sustained attack, Ask53 may shed useful requests to preserve bounded spending and memory.

If port 53 is already used (commonly by `systemd-resolved` or another DNS server), bind your actual public interface IP rather than a wildcard. Do not disable other system DNS services blindly. `0.0.0.0` binds IPv4 only; use an explicit IPv6 address or an appropriately tested dual-stack `::` listener when needed. The application supports one DNS listen address per process. Confirm your OS's dual-stack behavior and A/AAAA records.

Publish conventional A/AAAA records mapping `ask.example.com` to the server's reachable address. No port is embedded in those records:

```sh
dig @ask.example.com capital.of.iran. TXT +short +time=10 +tries=1
dig @ask.example.com capital.of.iran. TXT +tcp +short +time=10 +tries=1
```

For a namespaced interface, set `zone` to `ask.example.com.` and ask:

```sh
dig @ask.example.com capital.of.iran.ask.example.com. TXT +tcp +short
```

Public UDP is intentionally minimal. `dig +ignore` shows the truncated reply without following it. Configure a DNS wait longer than `ai_timeout` for cold questions; DNS clients commonly time out sooner than an AI call. Retransmitted identical questions coalesce while pending, then hit cache. There is no guarantee an arbitrary resolver will wait for AI latency.

On upgrades, retain `/var/lib/ask53`, replace the binary/configuration, and restart. Tune systemd's `TimeoutStopSec`, memory, task, and file-descriptor limits alongside any increased application limits. Shutdown drains both listeners concurrently and closes idle connections; a drain failure exits unsuccessfully and cancels remaining provider work.

## Optional container deployment

Systemd is the simplest normal-server deployment. A Dockerfile is also provided; it builds a static binary and runs it as UID/GID 10001 with CA certificates. Use a configuration with `listen: "0.0.0.0:5353"`, `admin_listen: ""`, `udp_mode: "tcp-only"`, an appropriate allowlist, and `budget_file: "/var/lib/ask53/budget.json"`.

```sh
docker build -t ask53:local .
# Precreate a persistent writable directory owned by UID/GID 10001.
docker run --name ask53 --restart unless-stopped --read-only \
  --cap-drop ALL --security-opt no-new-privileges:true \
  --memory 256m --pids-limit 256 --stop-timeout 20 \
  --tmpfs /tmp:rw,noexec,nosuid,size=8m \
  -p 53:5353/udp -p 53:5353/tcp \
  -v /absolute/path/ask53-state:/var/lib/ask53 \
  -v /absolute/path/container-config.json:/etc/ask53/config.json:ro \
  -v /absolute/path/api-key:/etc/ask53/api-key:ro \
  ask53:local
```

Ensure mounted config and secret files are readable by UID 10001 and the state directory is writable by it. Use the host/cloud firewall as well. Container NAT/proxies can change visible source IPs, merging rate limits and defeating an intended source allowlist; verify actual ingress behavior before exposure. Port publishing does not preserve identities on every platform. Refresh supported base-image patch releases for security and pin audited image digests in your release pipeline. Docker execution is not part of the local automated tests.

## Operational checks and `dig` cases

The loopback HTTP endpoints are `GET /healthz` (process alive), `GET /readyz` (listeners running, not draining), and `GET /stats` (fixed counters and bounded state). Readiness is local transport readiness, not provider availability, truthfulness, or remaining budget. Probe it independently of DNS to avoid spending paid calls. Stats include cache/coalescing counts, provider failures, in-flight work, circuit state, the UTC budget day, reservations, and persistence failure. They expose no configuration secrets or question labels. Counters reset on restart; the budget does not.

```sh
# Full header and TTL, no EDNS, explicit TCP, and case-insensitive repeat:
dig @127.0.0.1 -p 5353 capital.of.iran. TXT +time=10 +tries=1
dig @127.0.0.1 -p 5353 capital.of.iran. TXT +noedns
dig @127.0.0.1 -p 5353 capital.of.iran. TXT +tcp +short
dig @127.0.0.1 -p 5353 CAPITAL.OF.IRAN. TXT +short

# Expected REFUSED, with no provider work:
dig @127.0.0.1 -p 5353 capital.of.iran. A
dig @127.0.0.1 -p 5353 capital.of.iran. ANY
dig @127.0.0.1 -p 5353 _unsupported.name. TXT
dig @127.0.0.1 -p 5353 . TXT

# Expected SERVFAIL twice, one fake-provider attempt during failure_ttl:
dig @127.0.0.1 -p 5353 provider.fail. TXT
dig @127.0.0.1 -p 5353 provider.fail. TXT

# With udp_mode=tcp-only: inspect TC, then get the answer over TCP:
dig @127.0.0.1 -p 5353 capital.of.iran. TXT +ignore
dig @127.0.0.1 -p 5353 capital.of.iran. TXT +tcp +short
```

Use full `dig` output to diagnose refusals and `SERVFAIL`; `+short` suppresses most useful error information. A cache TTL can make time-sensitive answers old; tune it for the intended workload. Cache entries are not persisted, provider calls do not include live browsing tools, and “current” questions may be unanswerable or inaccurate. There is no DNSSEC signing, TSIG auth, DNS-over-TLS, or DNS-over-HTTPS support. Use a VPN for private access.

## Tests and project layout

```sh
make test       # Race detector; fake HTTP providers and real TCP/UDP sockets
make check      # Static analysis

go test ./internal/dnsserver -run='^$' -fuzz=FuzzQuestion -fuzztime=10s -parallel=2
go test ./internal/dnsserver -run='^$' -fuzz=FuzzTXTWire -fuzztime=10s -parallel=2
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

Tests never use a real AI key. They cover normalized cache keys, real `dig` TCP fallback, simultaneous UDP/TCP queries, 64-way request coalescing, canceled waiters, LRU eviction/expiry, failure suppression, provider HTTP/JSON/body/text errors, redirect rejection, token and byte limits, timeouts, circuit recovery and Retry-After races, daily quota races/persistence/clock rollover, corrupt/missing budget fields, storage failure, IPv6 source grouping, bounded client/connection/waiter tables, TXT wire escaping and UTF-8 boundaries, 512/1232-byte UDP limits, malformed packets and compression loops, EDNS errors, unsupported types/opcodes/classes, allowlists, active shutdown draining, idle connection shutdown, port rebinding, offline config validation, and real process SIGTERM/restart with persistent budget. Fuzzing checks name interpretation and lossless TXT encoding. CI runs these checks on Linux and macOS.

- `cmd/ask53`: configuration, signals, startup, and shutdown.
- `cmd/mock-provider`: deterministic, loopback-only test provider.
- `internal/config`: strict configuration parsing and bounded validation.
- `internal/ai`: minimal compatible completion client and answer validation.
- `internal/service`: rate gates, durable budget, bounded LRU, request coalescing, circuit breaker.
- `internal/dnsserver`: DNS validation, transport policy, bounded listeners, operational HTTP.
- `deploy`: systemd unit and production configuration example.
- `scripts`: release packaging, archive verification, and packaging regression tests.
- `docs/releases`: version-specific release notes.

## Protocol and API references

The wire-size, label, and TXT decisions follow [RFC 1035](https://www.rfc-editor.org/rfc/rfc1035.html). TCP fallback/support follows [RFC 7766](https://www.rfc-editor.org/rfc/rfc7766.html). EDNS negotiation and `BADVERS` follow [RFC 6891](https://www.rfc-editor.org/rfc/rfc6891.html). The implementation uses [miekg/dns](https://github.com/miekg/dns). Token fields follow the [official Chat Completions API reference](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create): `max_completion_tokens` includes visible and reasoning tokens, while the legacy `max_tokens` field has more limited model compatibility.

## Releases and license

Build versioned archives locally with `make release VERSION=v0.1.0`. The command
creates Linux and macOS archives for amd64 and arm64, a `SHA256SUMS` manifest,
configuration/deployment examples, and license notices under `dist/v0.1.0/`.
It refuses to overwrite an existing release directory and does not publish.
`ask53 -version` reports the embedded version. Regular `make build` defaults to
`dev`; `make build VERSION=v0.1.0` embeds an explicit version.

Run `make verify-release VERSION=v0.1.0` to check all archive hashes and contents
and exercise the native binary's version/configuration. Docker builds accept
`--build-arg VERSION=v0.1.0` to embed the same version in a locally built image.

The first release is prepared as **v0.1.0**; see its
[release notes](docs/releases/v0.1.0.md). After publication, download the matching
OS/architecture archive and `SHA256SUMS` from
[GitHub Releases](https://github.com/MeghdadFadaee/ask-53/releases). Verify the
checksum before extraction, then use the bundled deployment instructions.

Pushing a version tag runs Linux/macOS checks, builds the archives, and creates a
draft GitHub release for maintainer review. Manual workflow runs produce only
downloadable artifacts. For publication steps, see [PUBLISHING.md](PUBLISHING.md).

Ask53 is distributed under the [MIT license](LICENSE). Dependencies retain their
own licenses, listed in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
