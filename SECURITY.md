# Security policy

This project is maintained on `main`. Security fixes will be made there; no
older release branch currently has a separate support commitment.

## Reporting a vulnerability

If this repository is hosted on GitHub and private vulnerability reporting is
enabled, use **Security → Report a vulnerability**. Include the affected commit
or version, a minimal reproduction using fake credentials/providers, the
expected behavior, and the impact. Do not include live API keys or sensitive
DNS questions.

Do not post exploitable details in a public issue. If private reporting is not
available, open a minimal issue asking maintainers to provide a private contact
channel without including the vulnerability details. There is no promised
response time or bug bounty.

## Scope and deployment assumptions

Relevant issues include paid-request limit bypass, unbounded resource growth,
UDP amplification, DNS parser failures, credential disclosure, unauthorized
provider requests, or failures of cache/coalescing/shutdown protections.

The service is a direct-query DNS application, with no recursive resolution or
DNS-over-TLS/HTTPS. DNS questions and answers are plaintext, and the configured
provider receives the normalized question. TCP establishes reachability, not
end-user authentication. An explicitly open CIDR allowlist allows reachable
clients to consume the configured budget. Use an allowlist or VPN for private
access and network filtering for floods.

Budget guarantees assume durable local storage, retained state files, an
accurate clock, and a single process per budget. Operators can bypass their own
budget by deleting state, using independent budget files, or changing limits.
Provider billing and model output accuracy are outside the transport's control.
Never rely on AI answers as authoritative DNS records.

## Before publishing on GitHub

Enable private vulnerability reporting and secret scanning/push protection where
available. Keep the default branch protected, require the test checks for
changes, and restrict workflow-token permissions. Dependency updates are
configured in `.github/dependabot.yml`; review them before merging.
