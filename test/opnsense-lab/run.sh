#!/bin/sh
# framedrag OPNsense integration lab.
#
# Boots a real OPNsense (nano image, amd64, emulated under QEMU TCG) and
# proves the M1 integration story end to end with zero framedrag code on
# the firewall:
#
#   fixture feed -> framedrag update (fetch/parse/health/aggregate)
#     -> file target + loopback serve
#     -> OPNsense URL Table alias (its own machinery fetches the list
#        through the slirp host alias; framedrag serve stays loopback)
#     -> pf table, asserted by entry count and pfctl -T test membership
#   then phase 2: fixture changes, framedrag republishes, OPNsense
#   refreshes the alias, and the pf table converges.
#
# Usage: test/opnsense-lab/run.sh [--reuse-disk]
# Requires: qemu (brew install qemu), expect, python3, ~2 GB disk.
# Runtime: roughly 10-40 minutes; almost all of it is emulated boot.
# Exit 0 only on LAB-ALL-PASS.

set -eu

LAB="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$LAB/../.." && pwd)"
WORK="$LAB/work"
CACHE="$LAB/.cache"
VERSION="26.1"
IMG_BZ2="$CACHE/OPNsense-$VERSION-nano-amd64.img.bz2"
DISK="$WORK/opnsense.img"

LAB_HTTP_PORT=18081   # lab assets: fixtures, guest-setup.py, expected-*.json
LAB_SERVE_PORT=18080  # framedrag serve (the URL table source)
LAB_CTRL_PORT=18082   # phase-2 trigger
export LAB_HTTP_PORT LAB_SERVE_PORT LAB_CTRL_PORT

REUSE_DISK=0
[ "${1:-}" = "--reuse-disk" ] && REUSE_DISK=1

for tool in qemu-system-x86_64 expect python3 bunzip2; do
    command -v "$tool" >/dev/null || { echo "missing: $tool (qemu: brew install qemu)"; exit 1; }
done

# ---- image -------------------------------------------------------------
mkdir -p "$WORK" "$CACHE"
if [ ! -f "$IMG_BZ2" ]; then
    echo "==> downloading OPNsense $VERSION nano image"
    for m in mirror.ams1.nl.leaseweb.net/opnsense mirrors.nycbug.org/pub/opnsense mirror.dns-root.de/opnsense; do
        if curl -fL -o "$IMG_BZ2.part" "https://$m/releases/$VERSION/OPNsense-$VERSION-nano-amd64.img.bz2" &&
           curl -fsL -o "$CACHE/checksums.sha256" "https://$m/releases/$VERSION/OPNsense-$VERSION-checksums-amd64.sha256"; then
            mv "$IMG_BZ2.part" "$IMG_BZ2"; break
        fi
    done
    [ -f "$IMG_BZ2" ] || { echo "image download failed"; exit 1; }
    want=$(awk '/nano-amd64.img.bz2/ {print $NF}' "$CACHE/checksums.sha256")
    got=$(shasum -a 256 "$IMG_BZ2" | awk '{print $1}')
    [ "$want" = "$got" ] || { echo "checksum mismatch: want $want got $got"; exit 1; }
fi
if [ "$REUSE_DISK" = 0 ] || [ ! -f "$DISK" ]; then
    echo "==> preparing fresh disk"
    bunzip2 -kc "$IMG_BZ2" > "$DISK"
fi

# ---- framedrag ---------------------------------------------------------
echo "==> building framedrag"
(cd "$REPO" && make build >/dev/null)
FRAMEDRAG="$REPO/bin/framedrag"

ASSETS="$WORK/assets"   # served to the guest on LAB_HTTP_PORT
LISTS="$WORK/lists"     # framedrag file target + serve dir
STATE="$WORK/state"
rm -rf "$ASSETS" "$LISTS" "$STATE"
mkdir -p "$ASSETS" "$LISTS" "$STATE"
cp "$LAB/guest-setup.py" "$ASSETS/"

# Fixture feeds. Documentation prefixes only: nothing here overlaps the
# suppress list, RFC1918, or the lab network. The /25 pairs and /33 pair
# exist to prove CIDR aggregation is visible from the firewall.
cat > "$ASSETS/feed-v1.txt" <<'EOF'
# framedrag lab fixture v1
198.51.100.0/24
203.0.113.0/25
203.0.113.128/25
192.0.2.7
2001:db8::/33
2001:db8:8000::/33
EOF
cat > "$ASSETS/feed-v2.txt" <<'EOF'
# framedrag lab fixture v2: 192.0.2.7 removed, 198.18.0.0/15 added
198.51.100.0/24
203.0.113.0/25
203.0.113.128/25
198.18.0.0/15
2001:db8::/33
2001:db8:8000::/33
EOF
cp "$ASSETS/feed-v1.txt" "$ASSETS/feed.txt"

# Expectations the guest asserts against the live pf table. The /25s and
# /33s must appear aggregated; membership probes hit inside and outside.
cat > "$ASSETS/expected-v1.json" <<'EOF'
{
  "count": 4,
  "member": ["192.0.2.7", "198.51.100.77", "203.0.113.5", "203.0.113.200", "2001:db8::1", "2001:db8:9999::1"],
  "nonmember": ["8.8.8.8", "203.0.114.1", "2001:db9::1"]
}
EOF
cat > "$ASSETS/expected-v2.json" <<'EOF'
{
  "count": 4,
  "member": ["198.18.1.1", "198.19.255.254", "198.51.100.77", "203.0.113.200", "2001:db8:9999::1"],
  "nonmember": ["192.0.2.7", "8.8.8.8"]
}
EOF

cat > "$WORK/config.yaml" <<EOF
state_dir: $STATE
suppress:
  - 192.168.0.0/16
  - 10.0.0.0/8
health:
  delta_threshold_pct: 40
  stale_max_days: 14
aliases:
  - name: fd_lab
    action: deny
    direction: in
    feeds: [lab_feed]
targets:
  - type: file
    dir: $LISTS
    serve: 127.0.0.1:$LAB_SERVE_PORT
EOF
cat > "$WORK/feeds.local.yaml" <<EOF
feeds:
  - name: lab_feed
    url: http://127.0.0.1:$LAB_HTTP_PORT/feed.txt
    description: lab fixture feed
    category: LAB
    format: plain
    cadence_hours: 1
EOF

# ---- host services -----------------------------------------------------
cleanup() {
    kill "${HTTP_PID:-0}" "${SERVE_PID:-0}" "${CTRL_PID:-0}" "${QEMU_PID:-0}" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

echo "==> starting lab asset server on 127.0.0.1:$LAB_HTTP_PORT"
(cd "$ASSETS" && python3 -m http.server "$LAB_HTTP_PORT" --bind 127.0.0.1 >/dev/null 2>&1) &
HTTP_PID=$!

run_update() {
    "$FRAMEDRAG" update --config "$WORK/config.yaml" --catalog "$REPO/catalog/feeds.json"
}

echo "==> framedrag update (fixture v1)"
sleep 1
run_update

echo "==> starting framedrag serve on 127.0.0.1:$LAB_SERVE_PORT"
"$FRAMEDRAG" serve --config "$WORK/config.yaml" --catalog "$REPO/catalog/feeds.json" >"$WORK/serve.log" 2>&1 &
SERVE_PID=$!
sleep 1
curl -fsS "http://127.0.0.1:$LAB_SERVE_PORT/fd_lab.txt" >/dev/null || { echo "framedrag serve not answering"; exit 1; }

echo "==> starting phase-2 control server on 127.0.0.1:$LAB_CTRL_PORT"
python3 "$LAB/ctrl-server.py" "$LAB_CTRL_PORT" \
    "cp '$ASSETS/feed-v2.txt' '$ASSETS/feed.txt' && '$FRAMEDRAG' update --config '$WORK/config.yaml' --catalog '$REPO/catalog/feeds.json'" \
    2>"$WORK/ctrl.log" &
CTRL_PID=$!

# ---- the firewall ------------------------------------------------------
SERIAL_SOCK="$WORK/serial.sock"
rm -f "$SERIAL_SOCK"
echo "==> booting OPNsense $VERSION under QEMU (emulated: this is the slow part)"
qemu-system-x86_64 \
    -M q35 -accel tcg,thread=multi -cpu qemu64 -smp 2 -m 2048 \
    -drive "file=$DISK,format=raw,if=virtio" \
    -device e1000,netdev=lan \
    -netdev "user,id=lan,net=192.168.1.0/24,host=192.168.1.2,hostfwd=tcp:127.0.0.1:18443-192.168.1.1:443" \
    -device e1000,netdev=wan -netdev user,id=wan \
    -display none -monitor none \
    -serial "unix:$SERIAL_SOCK,server=on,wait=off" \
    >"$WORK/qemu.log" 2>&1 &
QEMU_PID=$!

i=0
while [ ! -S "$SERIAL_SOCK" ]; do
    kill -0 "$QEMU_PID" 2>/dev/null || { echo "qemu died at startup (see $WORK/qemu.log)"; exit 1; }
    i=$((i + 1)); [ "$i" -gt 30 ] && { echo "serial socket never appeared"; exit 1; }
    sleep 1
done

set +e
expect "$LAB/console.expect" "$SERIAL_SOCK" 2>&1 | tee "$WORK/console.log"
set -e
kill "$QEMU_PID" 2>/dev/null || true
grep -q "LAB-ALL-PASS" "$WORK/console.log" || { echo "FAILED: no LAB-ALL-PASS (see $WORK/console.log)"; exit 1; }
echo "==> LAB PASSED"
exit 0
