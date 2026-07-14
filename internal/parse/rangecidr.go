package parse

import (
	"encoding/binary"
	"io"
	"math/bits"
	"net/netip"
	"strings"
)

// rangeParser handles `a.b.c.d-w.x.y.z` style ranges (IPv4 and IPv6),
// converting each to the minimal covering CIDR set. Feeds in this
// format routinely mix in plain IPs and CIDRs, so those parse too.
type rangeParser struct{}

func (rangeParser) Name() string { return "range" }

func (rangeParser) Parse(r io.Reader) ([]netip.Prefix, Stats, error) {
	var out []netip.Prefix
	var st Stats
	err := scanLines(r, &st, func(entry string) lineResult {
		if ps, ok := parseRangeEntry(entry); ok {
			out = append(out, ps...)
			return lineParsed
		}
		p, ok := parseEntry(entry)
		if !ok {
			return lineRejected
		}
		out = append(out, p)
		return lineParsed
	})
	return out, st, err
}

// parseRangeEntry parses `start-end` into the minimal covering CIDR
// set. Neither IPv4 nor IPv6 literals contain '-', so splitting on the
// first '-' is unambiguous.
func parseRangeEntry(s string) ([]netip.Prefix, bool) {
	lo, hi, found := strings.Cut(s, "-")
	if !found {
		return nil, false
	}
	start, err := netip.ParseAddr(strings.TrimSpace(lo))
	if err != nil {
		return nil, false
	}
	end, err := netip.ParseAddr(strings.TrimSpace(hi))
	if err != nil {
		return nil, false
	}
	start = start.Unmap().WithZone("")
	end = end.Unmap().WithZone("")
	if start.Is4() != end.Is4() || end.Less(start) {
		return nil, false
	}
	return rangeToPrefixes(start, end), true
}

// rangeToPrefixes converts an inclusive [start, end] range (same
// family, start <= end) to the minimal covering CIDR set: repeatedly
// emit the largest block that both starts aligned at the cursor and
// fits within the remaining range.
func rangeToPrefixes(start, end netip.Addr) []netip.Prefix {
	v4 := start.Is4()
	addrBits := 128
	if v4 {
		addrBits = 32
	}
	s, e := addrToU128(start), addrToU128(end)
	var out []netip.Prefix
	for {
		// Largest block allowed by the cursor's alignment.
		k := s.trailingZeros()
		if k > addrBits {
			k = addrBits
		}
		// Largest block that fits: 2^k - 1 <= end - cursor.
		if k2 := maxBlock(e.sub(s), addrBits); k2 < k {
			k = k2
		}
		out = append(out, netip.PrefixFrom(u128ToAddr(s, v4), addrBits-k))
		next, overflow := s.addPow2(k)
		if overflow || e.less(next) {
			return out
		}
		s = next
	}
}

// maxBlock returns the largest k such that a block of 2^k addresses
// fits in a remaining span of span+1 addresses.
func maxBlock(span uint128, addrBits int) int {
	if span.hi == ^uint64(0) && span.lo == ^uint64(0) {
		return addrBits // full 128-bit space; unreachable for IPv4
	}
	k := span.add1().bitLen() - 1
	if k > addrBits {
		k = addrBits
	}
	return k
}

// uint128 is an unsigned 128-bit integer, hi:lo. IPv4 addresses live
// in the low 32 bits with hi == 0.
type uint128 struct{ hi, lo uint64 }

func addrToU128(a netip.Addr) uint128 {
	if a.Is4() {
		b := a.As4()
		return uint128{0, uint64(binary.BigEndian.Uint32(b[:]))}
	}
	b := a.As16()
	return uint128{binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])}
}

func u128ToAddr(v uint128, v4 bool) netip.Addr {
	if v4 {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(v.lo)) // #nosec G115 -- v4 addresses occupy only the low 32 bits
		return netip.AddrFrom4(b)
	}
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], v.hi)
	binary.BigEndian.PutUint64(b[8:], v.lo)
	return netip.AddrFrom16(b)
}

func (a uint128) less(b uint128) bool {
	if a.hi != b.hi {
		return a.hi < b.hi
	}
	return a.lo < b.lo
}

// sub returns a - b; callers guarantee a >= b.
func (a uint128) sub(b uint128) uint128 {
	lo, borrow := bits.Sub64(a.lo, b.lo, 0)
	hi, _ := bits.Sub64(a.hi, b.hi, borrow)
	return uint128{hi, lo}
}

// add1 returns a + 1; callers guarantee no overflow.
func (a uint128) add1() uint128 {
	lo, carry := bits.Add64(a.lo, 1, 0)
	hi, _ := bits.Add64(a.hi, 0, carry)
	return uint128{hi, lo}
}

// addPow2 returns a + 2^k and whether the addition overflowed 128 bits.
func (a uint128) addPow2(k int) (uint128, bool) {
	if k >= 128 {
		return uint128{}, true
	}
	var b uint128
	if k < 64 {
		b.lo = 1 << k
	} else {
		b.hi = 1 << (k - 64)
	}
	lo, carry := bits.Add64(a.lo, b.lo, 0)
	hi, carry := bits.Add64(a.hi, b.hi, carry)
	return uint128{hi, lo}, carry != 0
}

func (a uint128) trailingZeros() int {
	if a.lo != 0 {
		return bits.TrailingZeros64(a.lo)
	}
	if a.hi != 0 {
		return 64 + bits.TrailingZeros64(a.hi)
	}
	return 128
}

func (a uint128) bitLen() int {
	if a.hi != 0 {
		return 64 + bits.Len64(a.hi)
	}
	return bits.Len64(a.lo)
}
