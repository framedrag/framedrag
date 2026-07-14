# framedrag

Curated IP reputation feeds, dragged into the null route. The IP-blocking
half of pfBlockerNG, decoupled from pfSense, aimed at OPNsense. Single
static Go binary, one-shot run mode (cron/systemd), Apache-2.0.

Module `framedrag.dev/framedrag`, binary `framedrag`. Full product spec
lives in `docs/SPEC.md`; read it before changing behavior.

## Invariants

- **Never fail open.** A FAILED feed falls back to its last-good cached
  copy; after `stale_max` days it is dropped loudly. The run exits
  non-zero if any feed is FAILED or SUSPECT.
- **Never touch objects we do not own.** API targets only modify
  aliases/rules carrying the `fd_` prefix.
- `net/netip` everywhere, never `net.IP`. IPv4 and IPv6 from day one.
- Determinism: sorted output, stable serialization, reproducible runs.
- CIDR aggregation must cover exactly the same address space as its
  input, never more, never less. The property test enforces this.
- Suppression of the user's own networks is logged, always.
- API keys live only in the local overlay; never committed, logged, or
  sent anywhere except the feed provider.
- No network access in unit tests; fetching is behind `fetch.Fetcher`.
- Dependencies: stdlib + cobra + yaml.v3. Adding anything else needs a
  strong reason.
- TDD for core packages: write the failing test first.
- No em-dashes in user-facing copy (README, website, CLI help).

## Package ownership

- `internal/health` and `internal/normalize` are the core; changes
  there need tests written first.
- `catalog/feeds.json` is vendored from pfBlockerNG upstream; never
  hand-edit it. `framedrag catalog sync` refreshes it and records the
  upstream commit SHA in `catalog/UPSTREAM`.

## Org rules

- No GitHub PATs for automation; use a GitHub App with short-lived
  installation tokens (`actions/create-github-app-token`).
- After every push to main, verify the README is still accurate.
