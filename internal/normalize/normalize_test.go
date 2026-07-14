package normalize

import (
	"math/rand"
	"net/netip"
	"slices"
	"testing"
)

func pfx(t *testing.T, ss ...string) []netip.Prefix {
	t.Helper()
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestAggregate(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, nil},
		{"single", []string{"10.0.0.0/8"}, []string{"10.0.0.0/8"}},
		{"dedup exact", []string{"1.2.3.0/24", "1.2.3.0/24"}, []string{"1.2.3.0/24"}},
		{"adjacent siblings merge", []string{"1.2.3.0/25", "1.2.3.128/25"}, []string{"1.2.3.0/24"}},
		{"contained removed", []string{"10.0.0.0/8", "10.1.0.0/16"}, []string{"10.0.0.0/8"}},
		{"chain of four quarters", []string{"1.2.3.0/26", "1.2.3.64/26", "1.2.3.128/26", "1.2.3.192/26"}, []string{"1.2.3.0/24"}},
		{"non-siblings stay apart", []string{"1.2.3.128/25", "1.2.4.0/25"}, []string{"1.2.3.128/25", "1.2.4.0/25"}},
		{"adjacent but misaligned", []string{"1.2.3.128/25", "1.2.4.0/24"}, []string{"1.2.3.128/25", "1.2.4.0/24"}},
		{"host pair merges to /31", []string{"1.2.3.4/32", "1.2.3.5/32"}, []string{"1.2.3.4/31"}},
		{"overlap across sizes", []string{"1.2.2.0/23", "1.2.3.0/24"}, []string{"1.2.2.0/23"}},
		{"v6 siblings merge", []string{"2001:db8::/33", "2001:db8:8000::/33"}, []string{"2001:db8::/32"}},
		{"v4 and v6 never merge", []string{"1.2.3.0/24", "2001:db8::/32"}, []string{"1.2.3.0/24", "2001:db8::/32"}},
		{"unmasked input canonicalized", []string{"1.2.3.77/24"}, []string{"1.2.3.0/24"}},
		{"full v4 table", []string{"0.0.0.0/1", "128.0.0.0/1"}, []string{"0.0.0.0/0"}},
		{"range remnant merges upward", []string{"1.2.3.0/24", "1.2.2.0/24", "1.2.0.0/23"}, []string{"1.2.0.0/22"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Aggregate(pfx(t, tc.in...))
			want := pfx(t, tc.want...)
			if !slices.Equal(got, want) {
				t.Fatalf("Aggregate(%v)\n got %v\nwant %v", tc.in, got, want)
			}
		})
	}
}

func TestAggregateDoesNotMutateInput(t *testing.T) {
	in := pfx(t, "10.1.0.0/16", "10.0.0.0/8")
	orig := slices.Clone(in)
	Aggregate(in)
	if !slices.Equal(in, orig) {
		t.Fatalf("input mutated: %v", in)
	}
}

func TestSuppress(t *testing.T) {
	own := pfx(t, "192.168.0.0/16", "2001:db8:aa::/48")
	in := pfx(t,
		"8.8.8.0/24",         // unrelated, kept
		"192.168.1.0/24",     // inside own, dropped
		"192.0.0.0/8",        // contains own, dropped
		"192.169.0.0/16",     // adjacent to own, kept
		"2001:db8:aa::1/128", // inside own v6, dropped
		"2001:db8::/32",      // contains own v6, dropped
	)
	kept, suppressed := Suppress(in, own)
	wantKept := pfx(t, "8.8.8.0/24", "192.169.0.0/16")
	wantSup := pfx(t, "192.168.1.0/24", "192.0.0.0/8", "2001:db8:aa::1/128", "2001:db8::/32")
	if !slices.Equal(kept, wantKept) {
		t.Fatalf("kept = %v, want %v", kept, wantKept)
	}
	if !slices.Equal(suppressed, wantSup) {
		t.Fatalf("suppressed = %v, want %v", suppressed, wantSup)
	}
}

func TestSuppressEmptyOwn(t *testing.T) {
	in := pfx(t, "1.2.3.0/24")
	kept, suppressed := Suppress(in, nil)
	if !slices.Equal(kept, in) || len(suppressed) != 0 {
		t.Fatalf("kept=%v suppressed=%v", kept, suppressed)
	}
}

// Suppression must run before aggregation: merging 1.2.3.0/25 and
// 1.2.3.128/25 into a /24 first would make the whole /24 overlap the
// user's 1.2.3.0/26 and needlessly drop coverage of 1.2.3.128/25.
func TestNormalizeSuppressesBeforeAggregating(t *testing.T) {
	res := Normalize(pfx(t, "1.2.3.0/25", "1.2.3.128/25"), pfx(t, "1.2.3.0/26"))
	want := pfx(t, "1.2.3.128/25")
	if !slices.Equal(res.Prefixes, want) {
		t.Fatalf("Prefixes = %v, want %v", res.Prefixes, want)
	}
	if len(res.Suppressed) != 1 || res.Suppressed[0] != netip.MustParsePrefix("1.2.3.0/25") {
		t.Fatalf("Suppressed = %v", res.Suppressed)
	}
}

func TestNormalizeDeterministicAcrossOrder(t *testing.T) {
	in := pfx(t,
		"5.6.7.0/24", "1.2.3.0/25", "1.2.3.128/25", "10.0.0.0/8",
		"10.20.0.0/16", "2001:db8::/34", "2001:db8:4000::/34",
	)
	want := Normalize(slices.Clone(in), nil).Prefixes
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 20; i++ {
		shuffled := slices.Clone(in)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		got := Normalize(shuffled, nil).Prefixes
		if !slices.Equal(got, want) {
			t.Fatalf("order dependence: got %v want %v", got, want)
		}
	}
}

func TestNormalizeSortsV4BeforeV6ByAddress(t *testing.T) {
	res := Normalize(pfx(t, "2001:db8::/32", "9.9.9.9/32", "1.0.0.0/24"), nil)
	want := pfx(t, "1.0.0.0/24", "9.9.9.9/32", "2001:db8::/32")
	if !slices.Equal(res.Prefixes, want) {
		t.Fatalf("got %v want %v", res.Prefixes, want)
	}
}

// covers reports whether addr a is covered by any prefix in set.
func covers(set []netip.Prefix, a netip.Addr) bool {
	for _, p := range set {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Property (docs/SPEC.md section 12): the aggregated set covers exactly
// the same address space as the input. Never drops, never adds.
func TestAggregateCoverageProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for iter := 0; iter < 200; iter++ {
		var in []netip.Prefix
		n := 1 + rng.Intn(40)
		for i := 0; i < n; i++ {
			if rng.Intn(4) == 0 { // some v6
				var b [16]byte
				rng.Read(b[:])
				b[0], b[1] = 0x20, 0x01
				bits := 16 + rng.Intn(113)
				in = append(in, netip.PrefixFrom(netip.AddrFrom16(b), bits).Masked())
			} else {
				var b [4]byte
				rng.Read(b[:])
				bits := 8 + rng.Intn(25)
				in = append(in, netip.PrefixFrom(netip.AddrFrom4(b), bits).Masked())
			}
		}
		out := Aggregate(in)

		// Minimality smoke check: never more prefixes than the input.
		if len(out) > len(in) {
			t.Fatalf("iter %d: aggregation grew the set: %d -> %d", iter, len(in), len(out))
		}

		// Probe boundary addresses of every input and output prefix:
		// first, last, and neighbors just outside. Coverage must agree.
		var probes []netip.Addr
		for _, p := range append(slices.Clone(in), out...) {
			first := p.Masked().Addr()
			last := lastAddr(p)
			probes = append(probes, first, last)
			if prev := first.Prev(); prev.IsValid() {
				probes = append(probes, prev)
			}
			if next := last.Next(); next.IsValid() {
				probes = append(probes, next)
			}
		}
		for _, a := range probes {
			if covers(in, a) != covers(out, a) {
				t.Fatalf("iter %d: coverage mismatch at %v\n in: %v\nout: %v", iter, a, in, out)
			}
		}
	}
}
