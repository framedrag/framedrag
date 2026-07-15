#!/usr/local/bin/python3
"""framedrag OPNsense lab, guest side.

Runs ON the OPNsense VM (fetched over the lab network). Three modes:

  setup <alias-url>     inject an fd_lab urltable alias + a firewall rule
                        referencing it into /conf/config.xml, then reload
                        the filter and refresh aliases so OPNsense's own
                        machinery fetches the list from framedrag
  refresh               re-run the alias refresh (phase 2, after the host
                        republished the list)
  assert <expected-url> fetch expectations JSON from the host and check
                        the live pf table: entry count and membership
                        probes via pfctl -T test

Markers on stdout (the expect driver keys off these):
  LAB-SETUP-OK / LAB-REFRESH-OK / LAB-ASSERT-PASS / LAB-FAIL <reason>
"""

import glob
import json
import os
import subprocess
import sys
import time
import urllib.request
import uuid
import xml.etree.ElementTree as ET

CONFIG = "/conf/config.xml"
ALIAS_NAME = "fd_lab"


def fail(reason):
    # Detail first, short marker last: the console driver kills the
    # session as soon as it sees LAB-FAIL, so anything after it is lost.
    print("LAB-DETAIL: " + reason)
    print("LAB-FAIL")
    sys.exit(1)


def run(cmd, **kw):
    return subprocess.run(cmd, capture_output=True, text=True, **kw)


def ensure(parent, tag):
    node = parent.find(tag)
    if node is None:
        node = ET.SubElement(parent, tag)
    return node


def inject_alias(alias_url):
    tree = ET.parse(CONFIG)
    root = tree.getroot()

    # MVC alias: OPNsense/Firewall/Alias/aliases/alias
    opn = ensure(root, "OPNsense")
    fw = ensure(opn, "Firewall")
    alias_root = fw.find("Alias")
    if alias_root is None:
        alias_root = ET.SubElement(fw, "Alias", {"version": "1.0.1"})
        ET.SubElement(ensure(alias_root, "geoip"), "url")
    aliases = ensure(alias_root, "aliases")

    for a in aliases.findall("alias"):
        if a.findtext("name") == ALIAS_NAME:
            aliases.remove(a)  # idempotent re-runs

    a = ET.SubElement(aliases, "alias", {"uuid": str(uuid.uuid4())})
    for tag, text in [
        ("enabled", "1"),
        ("name", ALIAS_NAME),
        ("type", "urltable"),
        ("proto", None),
        ("interface", None),
        ("counters", "0"),
        ("updatefreq", "0.01"),  # days; keep the periodic path plausible
        ("content", alias_url),
        ("categories", None),
        ("description", "framedrag lab list"),
    ]:
        el = ET.SubElement(a, tag)
        if text is not None:
            el.text = text

    # A legacy filter rule referencing the alias guarantees the pf table
    # is materialized. Block traffic FROM the listed prefixes inbound on
    # LAN; the lab host (192.168.1.2) is never in the list.
    filt = ensure(root, "filter")
    for r in filt.findall("rule"):
        if r.findtext("descr") == "framedrag lab":
            filt.remove(r)
    rule = ET.Element("rule")
    for tag, text in [
        ("type", "block"),
        ("interface", "lan"),
        ("ipprotocol", "inet46"),
        ("statetype", "keep state"),
        ("descr", "framedrag lab"),
    ]:
        ET.SubElement(rule, tag).text = text
    src = ET.SubElement(rule, "source")
    ET.SubElement(src, "address").text = ALIAS_NAME
    dst = ET.SubElement(rule, "destination")
    ET.SubElement(dst, "any")
    filt.insert(0, rule)

    tree.write(CONFIG)


def reload_filter():
    for cmd in [
        ["configctl", "filter", "reload"],
        ["configctl", "filter", "refresh_aliases"],
    ]:
        r = run(cmd, timeout=180)
        if r.returncode != 0:
            fail("%s rc=%d %s%s" % (" ".join(cmd), r.returncode, r.stdout, r.stderr))


def table_entries():
    r = run(["pfctl", "-t", ALIAS_NAME, "-T", "show"])
    if r.returncode != 0:
        return None
    return [l.strip() for l in r.stdout.splitlines() if l.strip()]


def wait_for_table(min_entries=1, timeout=120):
    deadline = time.time() + timeout
    while time.time() < deadline:
        entries = table_entries()
        if entries is not None and len(entries) >= min_entries:
            return entries
        time.sleep(5)
    fail("pf table %s never reached %d entries" % (ALIAS_NAME, min_entries))


def check_probes(exp):
    """Returns None on success, or a failure description."""
    entries = table_entries() or []
    if len(entries) != exp["count"]:
        return "entry count %d, want %d: %s" % (len(entries), exp["count"], entries)
    # pfctl -T test reports via exit code (0 = at least one address
    # matched) and prints its summary on stderr, not stdout.
    for probe in exp["member"]:
        r = run(["pfctl", "-t", ALIAS_NAME, "-T", "test", probe])
        if r.returncode != 0:
            return "probe %s should match (table: %s): rc=%d %s%s" % (probe, entries, r.returncode, r.stdout.strip(), r.stderr.strip())
    for probe in exp["nonmember"]:
        r = run(["pfctl", "-t", ALIAS_NAME, "-T", "test", probe])
        if r.returncode == 0:
            return "probe %s should NOT match (table: %s): rc=%d %s%s" % (probe, entries, r.returncode, r.stdout.strip(), r.stderr.strip())
    return None


def do_assert(expected_url):
    with urllib.request.urlopen(expected_url, timeout=30) as f:
        exp = json.load(f)

    wait_for_table(min_entries=1)
    # A filter reload can transiently repopulate the table from the
    # cached file; retry a few times before declaring failure.
    reason = None
    for attempt in range(6):
        reason = check_probes(exp)
        if reason is None:
            print("LAB-ASSERT-PASS count=%d" % exp["count"])
            return
        time.sleep(10)
    fail(reason)


def main():
    mode = sys.argv[1]
    if mode == "setup":
        inject_alias(sys.argv[2])
        reload_filter()
        wait_for_table()
        print("LAB-SETUP-OK")
    elif mode == "refresh":
        # updatefreq gates re-downloads by cache age; a real deployment
        # waits for the next cron tick. The lab forces an immediate
        # re-fetch by dropping the cached table files first.
        for f in glob.glob("/var/db/aliastables/%s*" % ALIAS_NAME):
            os.remove(f)
        r = run(["configctl", "filter", "refresh_aliases"], timeout=180)
        if r.returncode != 0:
            fail("refresh rc=%d %s%s" % (r.returncode, r.stdout, r.stderr))
        print("LAB-REFRESH-OK")
    elif mode == "assert":
        do_assert(sys.argv[2])
    else:
        fail("unknown mode " + mode)


if __name__ == "__main__":
    try:
        main()
    except SystemExit:
        raise
    except Exception as e:  # any surprise is a marker, not a traceback hunt
        fail("exception: %r" % (e,))
