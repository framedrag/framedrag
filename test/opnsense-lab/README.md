# OPNsense integration lab

Boots a real OPNsense firewall (official nano image, amd64, emulated
under QEMU TCG, so it runs on Apple Silicon too) and proves the M1
integration story end to end, with zero framedrag code installed on
the firewall:

```
fixture feed
  -> framedrag update        fetch, parse, health, dedup, CIDR-aggregate
  -> file target + serve     lists on loopback HTTP
  -> OPNsense URL Table alias
       OPNsense's own alias machinery (configctl filter
       refresh_aliases) fetches the list over the QEMU user-network
       host alias, so framedrag serve never leaves loopback
  -> pf table
       asserted by entry count and per-address membership
       (pfctl -t fd_lab -T test), IPv4 and IPv6, including that
       adjacent /25s and /33s arrive as their aggregated /24 and /32
```

Phase 2 then changes the feed, re-runs `framedrag update` on the host
(triggered by the guest via a small control server), forces an alias
re-fetch, and asserts that the pf table converged on the new content:
added prefixes present, removed prefixes gone.

## Run it

```sh
test/opnsense-lab/run.sh              # fresh disk every time
test/opnsense-lab/run.sh --reuse-disk # faster iteration
```

Requirements: `brew install qemu`, plus expect and python3 (both ship
with macOS). First run downloads the OPNsense nano image (~500 MB,
checksum-verified) into `.cache/`. Runtime is roughly 10 to 40
minutes; nearly all of it is the emulated first boot. The script exits
0 only when the console driver prints `LAB-ALL-PASS`.

Debugging: `work/console.log` holds the full serial transcript,
`work/qemu.log` the emulator output, `work/serve.log` and
`work/ctrl.log` the host services. While a VM is up you can poke at it
directly:

```sh
expect test/opnsense-lab/debug-probe.expect work/serial.sock \
    "pfctl -t fd_lab -T show"
```

The web UI is forwarded to https://127.0.0.1:18443 (root / opnsense).

## Design notes

- The guest reaches the host's loopback at 192.168.1.2 (the QEMU slirp
  host alias). That is what lets framedrag serve keep its
  loopback-only default, exactly as in production, while a separate
  machine still consumes the lists.
- OPNsense's nano image default-assigns em0 as LAN at 192.168.1.1;
  the console driver verifies this from the login banner and only runs
  the interface-assignment dialogue if the layout differs.
- The alias is injected into /conf/config.xml by guest-setup.py along
  with a firewall rule referencing it (a table is only guaranteed to
  materialize in pf when something references it), then loaded through
  the standard configctl paths. No OPNsense API keys are needed, which
  keeps the lab fully non-interactive.
- Real deployments re-fetch URL tables on the alias updatefreq / cron
  cadence. Phase 2 forces an immediate re-fetch by dropping the cached
  /var/db/aliastables files first; the production equivalent is simply
  waiting for the next tick.
- Everything is hermetic: fixture feeds use documentation prefixes
  (198.51.100/24, 203.0.113/24, 192.0.2.x, 2001:db8::/32) plus
  benchmark space (198.18.0.0/15) for the phase-2 addition. Only the
  OPNsense image download touches the internet.

Learned the hard way, kept for posterity: root's shell on OPNsense is
csh, `pfctl -T test` prints its verdict to stderr and speaks through
its exit code, and QEMU's stdio serial mux dislikes living under
expect's pty, which is why the serial console is a unix socket that
the driver reaches over `nc -U`.
