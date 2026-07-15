# framedrag

**Curated IP reputation feeds, dragged into the null route.**

*Frame-dragging (the Lense-Thirring effect) is what a rotating black hole
does to the spacetime around it: nothing nearby can stay still. It is also
what this does to Ethernet frames.*

The technique is older than the name. Vixie and Rand's RBL (1997) was
originally a BGP feed that null-routed spammer IPs, a practice known in the
trade as RTBH (Remotely Triggered Black Hole) filtering. pfBlockerNG carried
that idea into pfSense and spent a decade curating which feeds are worth
trusting. framedrag is the same idea, at the edge, without BGP: it consumes
that curated catalog and turns it into live firewall policy on any firewall
that can read a list of prefixes.

## What it is

framedrag is a platform-agnostic engine for IP blocklists: the IP-blocking
half of [pfBlockerNG](https://github.com/pfBlockerNG/pfBlockerNG), decoupled
from pfSense. A single static Go binary fetches curated IP reputation feeds,
parses and normalizes them, CIDR-aggregates the result, checks every feed's
health, and emits consolidated lists a firewall can consume. It runs
one-shot from cron or a systemd timer. It is not a daemon.

**Why.** Many pfSense users stay on pfSense for exactly one reason:
pfBlockerNG has no equivalent anywhere else. framedrag exists to remove that
blocker so those users can migrate to OPNsense. It works against pfSense
today (non-destructively, via URL table aliases), and it works against
OPNsense identically.

The feed curation itself, which feeds to trust and how much, is
pfBlockerNG's decade of work. framedrag does not fork that catalog; it
tracks it, and inherits upstream's ongoing maintenance.

## Status

Pre-release. M1 (pipeline + health: catalog load, fetch, parsers,
normalization, health checks, `file` target, `health` command) is in
progress. There are no releases yet. Expect breaking changes until v0.1.0.

The integration story is proven against a real firewall: `make
opnsense-lab` boots an actual OPNsense under QEMU, points a URL Table
alias at framedrag's served lists, and asserts the resulting pf table
contents, including update propagation (see `test/opnsense-lab/`).

## The health model

This is the core of the project, not a feature. A blocklist that quietly
stopped updating looks identical to one that works. pfBlockerNG's real value
over ten years was a human noticing when feeds broke; framedrag replaces the
human. Every run evaluates every feed:

| Status | Meaning | Triggered by |
|---|---|---|
| `OK` | Feed fetched, parsed, and passed all checks | The happy path |
| `SUSPECT` | Feed produced output, but something is off | Entry count swung more than the delta threshold (default 40%) versus the last good run; more than 10% of non-comment lines failed to parse (format drift); feed contained `0.0.0.0/0`, `::/0`, RFC1918, or your own networks |
| `STALE` | Feed content byte-identical for longer than its stated cadence allows | Upstream stopped updating |
| `FAILED` | No usable content this run | Non-2xx, timeout, TLS error, or zero prefixes parsed (the classic: a 404 page or Cloudflare interstitial served with HTTP 200) |

**Failure policy: never fail open, never fail catastrophically.**

- A `FAILED` feed does not produce an empty list. It falls back to the
  last-good cached copy, and the run is flagged.
- If a feed stays `FAILED` past `stale_max` (default 14 days), the cached
  copy is dropped from output with a loud error. Stale threat intel is worse
  than none.
- The run exits non-zero if any feed is `FAILED` or `SUSPECT`, so cron and
  systemd surface it.
- `framedrag health` prints a per-feed table of status, entry count, delta,
  and last-good timestamp. This is the command you run when you wonder
  "is this actually working?"

## Quickstart

There are no binary releases yet; build from source:

```sh
git clone https://github.com/framedrag/framedrag.git
cd framedrag
make build    # binary lands at bin/framedrag
```

### 1. Configure

One YAML file:

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

`suppress` is the guardrail that keeps a bad feed from blackholing your own
LAN: list your local networks and your WAN address, and any prefix that
contains or overlaps them is removed, and logged.

### 2. Run it, on a schedule

Do a dry run first, then wire it to cron:

```sh
framedrag update --dry-run    # print what would change, apply nothing
framedrag update              # fetch, parse, normalize, apply to targets
```

```cron
# /etc/cron.d/framedrag: hourly refresh. Non-zero exit means a feed is
# FAILED or SUSPECT; make sure cron mail goes somewhere a human reads.
0 * * * *  root  /usr/local/bin/framedrag update --config /usr/local/etc/framedrag.yaml
```

### 3. Point the firewall at the lists

The `file` target writes one consolidated list per alias and, optionally,
serves them over loopback HTTP. Both pfSense and OPNsense consume them
natively via URL table aliases, no API integration required.

**pfSense:** Firewall > Aliases > Add. Type: `URL Table (IPs)`. URL:
`http://127.0.0.1:8080/fd_pri1.txt` (or a `file://` path to the list if
framedrag runs on the box). Then reference the `fd_pri1` alias from a block
rule on WAN.

**OPNsense:** Firewall > Aliases > Add. Type: `URL Table (IPs)`. Same URL,
same idea: reference the alias from a block rule on the interfaces you care
about.

If framedrag runs on a different host than the firewall, bind `serve` to an
address the firewall can reach, and restrict access to it. It binds to
loopback by default on purpose: several feed providers forbid
redistribution, so framedrag fetches at the edge and never re-serves feeds
beyond your own firewall.

A native OPNsense target (aliases and rules driven over the REST API, with
`fd_`-prefixed ownership so framedrag never touches objects it does not own)
is planned for M3.

## CLI

```
framedrag update              # fetch, parse, normalize, apply to targets
framedrag update --dry-run    # everything except Apply; print what would change
framedrag health              # per-feed status table
framedrag catalog sync        # diff vendored catalog against upstream
framedrag catalog list        # show available feeds/tiers
framedrag version
```

Global flags: `--config`, `--verbose`, `--json` (machine-readable output
for all commands).

## framedrag vs pfBlockerNG, honestly

| | pfBlockerNG | framedrag |
|---|---|---|
| IP blocklists: fetch, dedup, CIDR-aggregate | Yes | Yes |
| Feed catalog and quality tiers (PRI1 to PRI5) | Yes, the original | Tracks pfBlockerNG's catalog upstream |
| Silent-failure detection | A human reading logs | First-class: health states, non-zero exit codes, webhook notifications planned for M2 |
| DNSBL / DNS blocking | Yes | **No.** Use [AdGuard Home](https://github.com/AdguardTeam/AdGuardHome); it does per-client DNS policy better than a firewall package can |
| GeoIP country blocking | Yes | No, not planned for v1 |
| GUI | Yes, pfSense package UI | **No.** CLI plus cron; a GUI or OPNsense plugin would be a later phase and a separate repo |
| Reports and dashboards | Yes | `framedrag health` and `--json`, nothing more |
| pfSense | Native package | Via URL table aliases: non-destructive, nothing installed on the firewall |
| OPNsense | Not available | The point: URL table aliases today, native API backend planned for M3 |
| Feed re-hosting / central aggregation | n/a | **Never.** Fetches at the edge; several providers' terms forbid redistribution |
| Runs as | pfSense package | Single static binary, one-shot, cron/systemd |

If you want DNSBL, a GUI, or GeoIP blocking on pfSense, pfBlockerNG remains
the right tool, and this project is glad it exists.

## Credits

- **pfBlockerNG** by BBcan177, maintained with Rubicon Communications, LLC,
  Apache 2.0. The feed catalog, the quality tiering, and a decade of
  curation are pfBlockerNG's work. See [NOTICE](NOTICE).
- **Paul Vixie and Dave Rand's RBL** (1997): the origin of null-routing
  curated reputation feeds, and the RTBH lineage this tool descends from.

## License

Apache 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE). Licensed
permissively, explicitly so it can be forked.
