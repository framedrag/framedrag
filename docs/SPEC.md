# framedrag — Spec

**Curated IP reputation feeds, dragged into the null route.**

| | |
|---|---|
| Repo | `github.com/framedrag/framedrag` |
| Site | `framedrag.dev` |
| Binary | `framedrag` |
| Module | `framedrag.dev/framedrag` |
| License | Apache 2.0 |
| Alias prefix | `fd_` (reserved: framedrag will only ever modify objects it owns) |

*Frame-dragging (the Lense-Thirring effect) is what a rotating black hole does to the spacetime
around it: nothing nearby can stay still. It is also what this does to Ethernet frames.*

**Prior art, credited in `NOTICE`:** pfBlockerNG (BBcan177 / Rubicon Communications,
Apache 2.0), the feed catalog and a decade of curation. And before that, Vixie & Rand's RBL
(1997), which was originally a BGP feed that null-routed spammer IPs. Blackholing curated IP
feeds is not a metaphor here; it is the actual technique, known in the trade as RTBH
(Remotely Triggered Black Hole) filtering. framedrag is that idea, at the edge, without BGP.

## 1. Purpose

A platform-agnostic engine that consumes curated IP-reputation feeds and turns them into
live firewall policy: the IP-blocking half of pfBlockerNG, decoupled from pfSense.

The goal is to remove the single largest blocker preventing pfSense users from migrating to
OPNsense. Today those users stay on pfSense because pfBlockerNG has no equivalent anywhere else.

**License: Apache 2.0.** Explicitly so it can be forked. Ship a `NOTICE` file crediting
pfBlockerNG (BBcan177 / Rubicon Communications) as prior art and as the source of the feed catalog.

## 2. Scope

### In scope
- Fetching, parsing, normalizing, deduplicating, and CIDR-aggregating IP feeds
- **Silent-failure detection** (see section 6, the highest-value component, built first)
- Emitting consolidated lists in a form a firewall can consume
- Pluggable target backends (pfSense first for testing, OPNsense as the real destination)

### Explicitly NOT in scope for v1
- **DNS blocking / DNSBL.** AdGuard Home already solves this well on OPNsense, with better
  per-client policy than pfBlockerNG's DNSBL. Do not reimplement Unbound Python modules,
  sinkhole VIPs, or block pages. If DNS is ever added, it will be by pushing domain lists to
  AdGuard Home over *its* API, nothing more.
- GUI / OPNsense plugin. That is a later phase and a separate repo.
- Republishing or re-hosting aggregated feeds. See section 9 (redistribution terms).

## 3. Architecture

Single static Go binary. No daemon dependencies beyond what's in the standard library plus
a YAML parser and an HTTP client.

```
cmd/framedrag/         CLI entrypoint
internal/catalog/      Feed catalog: load, track upstream, diff
internal/fetch/        HTTP fetching with etag/last-modified caching, retries, timeouts
internal/parse/        Format parsers (see section 5)
internal/normalize/    Dedup, CIDR aggregation, suppression of local networks
internal/health/       Feed health checks and failure detection (see section 6)
internal/target/       Pluggable output backends (see section 8)
internal/state/        On-disk state: last-good lists, entry counts, feed history
```

Run mode: one-shot, invoked by cron/systemd timer. Not a long-running daemon in v1.
Exit codes matter (see section 8).

## 4. Feed catalog

The curation, *which* feeds are trustworthy, tiered by quality, is the expensive asset, and it
already exists. Do not recreate it.

**Source of truth:** `pfblockerng_feeds.json` from the pfBlockerNG community fork
(`github.com/pfBlockerNG/pfBlockerNG`, Apache 2.0). It contains, per feed: URL, description,
category, quality tier (PRI1 = most reputable through PRI5 = may contain false positives),
and update cadence. Some entries carry `_API_KEY_` placeholders for registration-gated feeds.

**Do not fork the catalog. Track it.**

- `framedrag catalog sync` fetches the upstream JSON, diffs it against the vendored copy, and
  reports added / removed / changed feeds.
- Vendored copy lives at `catalog/feeds.json` with the upstream commit SHA recorded alongside.
- A CI job runs `catalog sync` weekly and opens an issue on any diff. This is how the project
  inherits upstream's ongoing maintenance instead of duplicating it.
- Users can layer a local overlay (`feeds.local.yaml`) to add feeds, disable feeds, or supply
  API keys, without touching the vendored catalog.

**Known coupling risk, accepted consciously:** feed health depends on the pfBlockerNG fork
staying alive. If it dies, the vendored catalog freezes at last-known-good and maintenance
becomes ours. That's a future problem with a clear trigger, not a reason to defer.

**Verified 2026-07-14:** the catalog lives at
`src/usr/local/www/pfblockerng/pfblockerng_feeds.json` on branch `devel` of
`github.com/pfBlockerNG/pfBlockerNG`.

## 5. Parsers

The catalog gives URLs, not shapes. Implement parsers for, at minimum:

| Format | Notes |
|---|---|
| Plain CIDR / IP, one per line | The common case |
| IP ranges (`a.b.c.d-w.x.y.z`) | Convert to minimal CIDR set |
| CSV with header | Column index configurable per feed |
| Comment-bearing lines | `#`, `;`, inline comments after entries |
| Spamhaus DROP/EDROP | `CIDR ; SBL#####` |
| Emerging Threats | Plain, but watch for HTML error pages served with 200 |

Parsers are selected per-feed via the catalog/overlay, with a `detect` fallback that sniffs
the first N non-comment lines. Every parser returns `([]netip.Prefix, ParseStats, error)`.

`ParseStats` must include: lines seen, entries parsed, lines rejected. Rejected-line ratio is
a health signal (see section 6).

Use `net/netip` (not `net.IP`) throughout. Support IPv4 and IPv6 from day one.

## 6. Health & silent-failure detection — BUILT FIRST

This is the core of the project, not a feature. A blocklist that quietly stopped updating is
visually indistinguishable from one that works. pfBlockerNG's real value over ten years was a
human noticing when feeds broke; this replaces the human.

**Per-feed checks, evaluated on every run:**

1. **HTTP failure**: non-2xx, timeout, TLS error, feed marked `FAILED`
2. **Zero entries**: parsed successfully but produced 0 prefixes, `FAILED`
   (This is the #1 real-world failure: a 404 page or Cloudflare interstitial served with HTTP 200.)
3. **Count delta**: entry count changed by more than +/-X% versus last good run, `SUSPECT`
   (default threshold 40%, configurable per feed; some feeds legitimately swing)
4. **Rejected-line ratio**: >10% of non-comment lines failed to parse, `SUSPECT` (format drift)
5. **Staleness**: feed content byte-identical for more than N days past its stated cadence, `STALE`
6. **Sanity floor**: feed contains `0.0.0.0/0`, `::/0`, RFC1918, or the user's own networks;
   entry dropped and feed marked `SUSPECT` (a feed that would blackhole your own LAN)

**Failure policy, never fail open, never fail catastrophically:**

- A `FAILED` feed does **not** produce an empty list. It falls back to the last-good cached
  copy from `internal/state`, and the run is flagged.
- If a feed has been `FAILED` for more than `stale_max` (default 14 days), stop using the
  cached copy and drop the feed from output, with a loud error. Stale threat intel is worse
  than none.
- The overall run exits non-zero if any feed is `FAILED` or `SUSPECT`, so cron/systemd surfaces it.
- `framedrag health` prints a table of every feed with status, entry count, delta, and last-good
  timestamp. This is the command a user runs when they wonder "is this actually working?"

Notifications: pluggable, minimal. stdout/exit-code is the baseline. Add a generic webhook
(POST JSON) so people can wire Slack/Discord/ntfy. Do not build integrations.

## 7. Normalization

Applied after parsing, before emitting:

1. Merge all feeds belonging to the same target alias
2. Deduplicate exact prefixes
3. **CIDR-aggregate**: collapse adjacent/contained prefixes into the minimal covering set.
   This matters enormously: naive concatenation of PRI1-PRI5 produces alias tables large
   enough to blow pf's table limits. Aggregation is not an optimization, it's a requirement.
4. **Suppression**: remove any prefix that contains or overlaps the user's own networks,
   configured explicitly (`suppress: [192.168.0.0/16, 10.0.0.0/8, <WAN /32>]`). Log every
   suppression. This is the guardrail that prevents locking yourself out of your own firewall.
5. Emit deterministic, sorted output (stable diffs matter for review and for change detection)

## 8. Targets (output backends)

Define an interface, implement backends behind it (see `internal/target/target.go`).

### v1: `file` target (built first)

Writes consolidated lists to a directory and optionally serves them over localhost HTTP.
The firewall consumes them via a **URL Table alias**, which both pfSense and OPNsense support
natively, with zero API integration.

This is deliberately the dumbest possible target, and it's why it's first:
- Works against the existing pfSense box today, unmodified, non-destructively
- Works against OPNsense identically
- Lets the entire fetch -> parse -> normalize -> health pipeline be validated before touching any API
- If everything after this point failed, `file` alone would still be a useful tool

### v2: `opnsense` target

Drives the OPNsense REST API:
- Create/update **aliases** (type: external/URL table or network group)
- Generate firewall rules via the **Automation -> Filter** API (programmatic rule creation).
  Rules carry Deny / Permit / Match semantics, direction (in/out), and per-interface binding.
- Reconcile: rules and aliases owned by framedrag are prefixed (`fd_`) and are the only ones it
  will ever modify or delete. **Never touch a rule or alias it does not own.**

**Verify before designing against it:** confirm the current name, shape, and stability of the
OPNsense Automation filter-rule API in the version being targeted. Do not assume the endpoint
shape from memory; read the current API docs and, if possible, the plugin source.

### v3 (optional): `pfsense` target

Only if a clean API exists on the target version. Otherwise `file` + URL table alias is a
perfectly good pfSense story and needs no code.

## 9. Redistribution — legal guardrail

`framedrag` **fetches at the edge**. Each user's box pulls feeds directly from the provider.

Do **not** build a service that aggregates feeds centrally and re-serves them. Several
providers' terms forbid redistribution of their lists. The localhost HTTP server in the `file`
target serves only to the local firewall and must bind to loopback by default.

If API keys are needed for gated feeds, they live in the user's local overlay config and are
never committed, logged, or transmitted anywhere except to the feed provider.

## 10. Configuration

Single YAML file. Example shape:

```yaml
state_dir: /var/lib/framedrag

suppress:
  - 192.168.0.0/16
  - 10.0.0.0/8

health:
  delta_threshold_pct: 40
  stale_max_days: 14
  webhook: ""            # optional

aliases:
  - name: fd_pri1
    action: deny
    direction: both
    feeds: [PRI1]        # references catalog tiers/feeds
  - name: fd_pri2
    action: deny
    direction: in
    feeds: [PRI2]

targets:
  - type: file
    dir: /var/lib/framedrag/lists
    serve: 127.0.0.1:8080
```

## 11. CLI surface

```
framedrag update              # fetch, parse, normalize, apply to targets
framedrag update --dry-run    # everything except Apply; print what would change
framedrag health              # per-feed status table
framedrag catalog sync        # diff vendored catalog against upstream
framedrag catalog list        # show available feeds/tiers
framedrag version
```

Global flags: `--config`, `--verbose`, `--json` (machine-readable output for all commands).

## 12. Testing

- **Golden-file tests for every parser.** Vendor a small real sample of each feed format into
  `testdata/`. These are the regression net for format drift.
- **Health-check tests must include the adversarial cases**, because these are the real bugs:
  - HTTP 200 returning an HTML error page
  - HTTP 200 returning a Cloudflare challenge
  - Feed that suddenly returns 3 entries instead of 30,000
  - Feed containing `0.0.0.0/0`
  - Feed containing the user's own LAN
- CIDR aggregation: property test that the aggregated set covers exactly the same address
  space as the input set. This must never drop or add coverage.
- No network access in unit tests. Fetching is behind an interface; use a fake.

## 13. Milestones

1. **M1 — Pipeline + health.** Catalog load, fetch, parsers, normalize, health checks, `file`
   target, `health` command. Run against the existing pfSense box via URL table aliases.
   *At the end of M1, the project is already useful and already proves the thesis.*
2. **M2 — Hardening.** Golden tests, catalog sync + CI, webhook notifications, aggregation
   property tests.
3. **M3 — OPNsense target.** Aliases + Automation filter rules, reconciliation, dry-run parity.
4. **M4 — Community.** README with an honest comparison table vs pfBlockerNG (including what
   it does *not* do), migration guide from pfBlockerNG config, `NOTICE` attribution.
   Then open a discussion on `opnsense/plugins` before building any GUI.

## 14. Notes for the implementer

- Ship `--dry-run` before shipping `Apply`. The first time this touches a real firewall, the
  author must be able to see exactly what it would do.
- Failing loudly is a feature. A security tool that fails quietly is worse than no tool.
- Determinism everywhere: sorted output, stable serialization, reproducible runs.
- One fact remains marked *verify* (section 8 OPNsense API shape). Confirm against live
  sources before designing around it. Do not guess. (The section 4 catalog path was verified
  2026-07-14.)
