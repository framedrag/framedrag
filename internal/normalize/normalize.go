// Package normalize turns raw parsed prefixes into the deterministic,
// minimal, safe lists that targets consume: dedup, suppression of the
// user's own networks, CIDR aggregation, stable sort.
//
// Order matters: suppression runs BEFORE aggregation. Aggregating first
// could merge innocent neighbors into a block that overlaps the user's
// own network, and suppression would then drop the whole merged block,
// losing blocking coverage that never conflicted in the first place.
package normalize

import (
	"math/bits"
	"net/netip"
	"slices"
)

// Result is the outcome of Normalize.
type Result struct {
	// Prefixes is the final list: suppressed, aggregated, sorted.
	Prefixes []netip.Prefix
	// Suppressed lists every input prefix removed because it contains
	// or overlaps one of the user's own networks. The caller must log
	// each one (docs/SPEC.md section 7).
	Suppressed []netip.Prefix
}

// Normalize deduplicates, suppresses the user's own networks, and
// CIDR-aggregates into the minimal covering set, sorted.
func Normalize(prefixes, own []netip.Prefix) Result {
	kept, suppressed := Suppress(prefixes, own)
	return Result{Prefixes: Aggregate(kept), Suppressed: suppressed}
}

// Suppress removes every prefix that overlaps (contains, is contained
// by, or equals) any of the user's own networks. Input order is
// preserved in both return values.
func Suppress(prefixes, own []netip.Prefix) (kept, suppressed []netip.Prefix) {
	if len(own) == 0 {
		return prefixes, nil
	}
	kept = make([]netip.Prefix, 0, len(prefixes))
	for _, p := range prefixes {
		if overlapsAny(p, own) {
			suppressed = append(suppressed, p)
		} else {
			kept = append(kept, p)
		}
	}
	return kept, suppressed
}

func overlapsAny(p netip.Prefix, own []netip.Prefix) bool {
	for _, o := range own {
		if p.Overlaps(o) {
			return true
		}
	}
	return false
}

// Aggregate collapses the input into the minimal set of prefixes
// covering exactly the same address space: duplicates and contained
// prefixes vanish, adjacent aligned blocks merge. Output is sorted
// (IPv4 before IPv6, then by address, then by prefix length) and the
// input is not modified.
func Aggregate(prefixes []netip.Prefix) []netip.Prefix {
	var v4, v6 []ipRange
	for _, p := range prefixes {
		r := rangeOf(p.Masked())
		if p.Addr().Is4() {
			v4 = append(v4, r)
		} else {
			v6 = append(v6, r)
		}
	}
	var out []netip.Prefix
	for _, r := range mergeRanges(v4) {
		out = append(out, rangeToCIDRs(r, 32)...)
	}
	for _, r := range mergeRanges(v6) {
		out = append(out, rangeToCIDRs(r, 128)...)
	}
	return out
}

// uint128 is a big-endian 128-bit unsigned integer. IPv4 addresses use
// only the low 32 bits.
type uint128 struct{ hi, lo uint64 }

func (a uint128) cmp(b uint128) int {
	switch {
	case a.hi != b.hi:
		if a.hi < b.hi {
			return -1
		}
		return 1
	case a.lo != b.lo:
		if a.lo < b.lo {
			return -1
		}
		return 1
	}
	return 0
}

func (a uint128) add1() (uint128, bool) {
	lo := a.lo + 1
	hi := a.hi
	if lo == 0 {
		hi++
		if hi == 0 {
			return uint128{}, true // wrapped past max
		}
	}
	return uint128{hi, lo}, false
}

// sub returns a - b; caller guarantees a >= b.
func (a uint128) sub(b uint128) uint128 {
	lo := a.lo - b.lo
	hi := a.hi - b.hi
	if a.lo < b.lo {
		hi--
	}
	return uint128{hi, lo}
}

type ipRange struct{ start, end uint128 } // inclusive

func toUint128(a netip.Addr, bits int) uint128 {
	if bits == 32 {
		b := a.As4()
		return uint128{0, uint64(b[0])<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3])}
	}
	b := a.As16()
	var u uint128
	for i := 0; i < 8; i++ {
		u.hi = u.hi<<8 | uint64(b[i])
		u.lo = u.lo<<8 | uint64(b[i+8])
	}
	return u
}

func toAddr(u uint128, bits int) netip.Addr {
	if bits == 32 {
		return netip.AddrFrom4([4]byte{byte(u.lo >> 24), byte(u.lo >> 16), byte(u.lo >> 8), byte(u.lo)})
	}
	var b [16]byte
	for i := 0; i < 8; i++ {
		b[i] = byte(u.hi >> (56 - 8*i))
		b[i+8] = byte(u.lo >> (56 - 8*i))
	}
	return netip.AddrFrom16(b)
}

func rangeOf(p netip.Prefix) ipRange {
	bits := 32
	if !p.Addr().Is4() {
		bits = 128
	}
	start := toUint128(p.Addr(), bits)
	span := bits - p.Bits() // number of host bits
	end := start
	if span > 0 {
		var mask uint128
		if span > 64 {
			mask = uint128{^uint64(0) >> (128 - span), ^uint64(0)}
		} else if span == 64 {
			mask = uint128{0, ^uint64(0)}
		} else {
			mask = uint128{0, ^uint64(0) >> (64 - span)}
		}
		end = uint128{start.hi | mask.hi, start.lo | mask.lo}
	}
	return ipRange{start, end}
}

// mergeRanges sorts by start and coalesces overlapping or adjacent
// ranges into the fewest inclusive ranges.
func mergeRanges(rs []ipRange) []ipRange {
	if len(rs) == 0 {
		return nil
	}
	slices.SortFunc(rs, func(a, b ipRange) int {
		if c := a.start.cmp(b.start); c != 0 {
			return c
		}
		return a.end.cmp(b.end)
	})
	out := []ipRange{rs[0]}
	for _, r := range rs[1:] {
		cur := &out[len(out)-1]
		next, wrapped := cur.end.add1()
		// r extends cur if it overlaps (r.start <= cur.end) or is
		// exactly adjacent (r.start == cur.end + 1).
		if r.start.cmp(cur.end) <= 0 || (!wrapped && r.start.cmp(next) == 0) {
			if r.end.cmp(cur.end) > 0 {
				cur.end = r.end
			}
		} else {
			out = append(out, r)
		}
	}
	return out
}

// rangeToCIDRs emits the minimal CIDR set covering the inclusive range.
func rangeToCIDRs(r ipRange, bits int) []netip.Prefix {
	var out []netip.Prefix
	start := r.start
	for {
		// Largest block size aligned at start.
		align := trailingZeros(start, bits)
		// Largest block size fitting in the remaining span.
		span := floorLog2(r.end.sub(start))
		k := min(align, span)
		out = append(out, netip.PrefixFrom(toAddr(start, bits), bits-k))
		// Advance past this block: start += 2^k.
		var step uint128
		if k >= 64 {
			step = uint128{1 << (k - 64), 0}
		} else {
			step = uint128{0, 1 << k}
		}
		lo := start.lo + step.lo
		hi := start.hi + step.hi
		if lo < start.lo {
			hi++
			if hi == 0 && step.hi == 0 { // wrapped past max address
				break
			}
		} else if hi < start.hi {
			break
		}
		start = uint128{hi, lo}
		if start.cmp(r.end) > 0 {
			break
		}
	}
	return out
}

// trailingZeros of the address value, capped at bits (an all-zero
// address is aligned for the full width).
func trailingZeros(u uint128, bits int) int {
	tz := 0
	if u.lo != 0 {
		tz = tzs64(u.lo)
	} else if u.hi != 0 {
		tz = 64 + tzs64(u.hi)
	} else {
		tz = 128
	}
	return min(tz, bits)
}

// floorLog2 of (span + 1), i.e. the largest k with 2^k <= number of
// addresses remaining, where span = end - start.
func floorLog2(span uint128) int {
	// number of addresses = span + 1; we want floor(log2(span + 1)).
	// For span all-ones (full space) this is the full width.
	n, wrapped := span.add1()
	if wrapped {
		return 128
	}
	if n.hi != 0 {
		return 63 + bits.Len64(n.hi)
	}
	return bits.Len64(n.lo) - 1
}

func tzs64(x uint64) int { return bits.TrailingZeros64(x) }

// lastAddr returns the highest address inside p.
func lastAddr(p netip.Prefix) netip.Addr {
	bits := 32
	if !p.Addr().Is4() {
		bits = 128
	}
	return toAddr(rangeOf(p.Masked()).end, bits)
}
