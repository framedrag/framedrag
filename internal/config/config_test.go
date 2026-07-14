package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// specExample is the example config from docs/SPEC.md section 10.
const specExample = `
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
`

func load(t *testing.T, yaml string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "framedrag.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLoadSpecExample(t *testing.T) {
	c, err := load(t, specExample)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		StateDir: "/var/lib/framedrag",
		Suppress: []string{"192.168.0.0/16", "10.0.0.0/8"},
		Health:   Health{DeltaThresholdPct: 40, StaleMaxDays: 14},
		Aliases: []Alias{
			{Name: "fd_pri1", Action: "deny", Direction: "both", Feeds: []string{"PRI1"}},
			{Name: "fd_pri2", Action: "deny", Direction: "in", Feeds: []string{"PRI2"}},
		},
		Targets: []Target{
			{Type: "file", Dir: "/var/lib/framedrag/lists", Serve: "127.0.0.1:8080"},
		},
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("Load mismatch:\n got: %+v\nwant: %+v", c, want)
	}
}

func TestLoadTable(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string // substring of error, "" means success expected
	}{
		{
			name:    "minimal valid",
			yaml:    "state_dir: /var/lib/framedrag\n",
			wantErr: "",
		},
		{
			name:    "missing state_dir",
			yaml:    "suppress: [10.0.0.0/8]\n",
			wantErr: "state_dir is required",
		},
		{
			name:    "unknown key rejected",
			yaml:    "state_dir: /s\nsupress: [10.0.0.0/8]\n",
			wantErr: "field supress not found",
		},
		{
			name:    "unknown nested key rejected",
			yaml:    "state_dir: /s\nhealth:\n  webook: x\n",
			wantErr: "field webook not found",
		},
		{
			name:    "suppress bare IPs accepted",
			yaml:    "state_dir: /s\nsuppress: [192.168.1.1, \"2001:db8::1\"]\n",
			wantErr: "",
		},
		{
			name:    "suppress garbage rejected",
			yaml:    "state_dir: /s\nsuppress: [not-an-ip]\n",
			wantErr: `suppress entry "not-an-ip"`,
		},
		{
			name:    "suppress bad prefix length rejected",
			yaml:    "state_dir: /s\nsuppress: [10.0.0.0/33]\n",
			wantErr: `suppress entry "10.0.0.0/33"`,
		},
		{
			name:    "alias without fd_ prefix",
			yaml:    "state_dir: /s\naliases:\n  - name: pri1\n    action: deny\n    direction: in\n",
			wantErr: "fd_ prefix",
		},
		{
			name: "alias duplicate name",
			yaml: "state_dir: /s\naliases:\n" +
				"  - {name: fd_a, action: deny, direction: in}\n" +
				"  - {name: fd_a, action: deny, direction: out}\n",
			wantErr: "duplicate name",
		},
		{
			name:    "alias bad action",
			yaml:    "state_dir: /s\naliases:\n  - {name: fd_a, action: block, direction: in}\n",
			wantErr: `action "block"`,
		},
		{
			name:    "alias bad direction",
			yaml:    "state_dir: /s\naliases:\n  - {name: fd_a, action: deny, direction: inbound}\n",
			wantErr: `direction "inbound"`,
		},
		{
			name:    "alias permit match directions ok",
			yaml:    "state_dir: /s\naliases:\n  - {name: fd_a, action: permit, direction: out}\n  - {name: fd_b, action: match, direction: both}\n",
			wantErr: "",
		},
		{
			name:    "target unknown type",
			yaml:    "state_dir: /s\ntargets:\n  - type: opnsense\n",
			wantErr: `unknown type "opnsense"`,
		},
		{
			name:    "file target requires dir",
			yaml:    "state_dir: /s\ntargets:\n  - type: file\n",
			wantErr: "dir is required",
		},
		{
			name:    "file target without serve ok",
			yaml:    "state_dir: /s\ntargets:\n  - {type: file, dir: /lists}\n",
			wantErr: "",
		},
		{
			name:    "serve loopback IPv4 ok",
			yaml:    "state_dir: /s\ntargets:\n  - {type: file, dir: /lists, serve: \"127.0.0.2:8080\"}\n",
			wantErr: "",
		},
		{
			name:    "serve loopback IPv6 ok",
			yaml:    "state_dir: /s\ntargets:\n  - {type: file, dir: /lists, serve: \"[::1]:8080\"}\n",
			wantErr: "",
		},
		{
			name:    "serve non-loopback rejected",
			yaml:    "state_dir: /s\ntargets:\n  - {type: file, dir: /lists, serve: \"0.0.0.0:8080\"}\n",
			wantErr: "must be loopback",
		},
		{
			name:    "serve LAN address rejected",
			yaml:    "state_dir: /s\ntargets:\n  - {type: file, dir: /lists, serve: \"192.168.1.5:8080\"}\n",
			wantErr: "must be loopback",
		},
		{
			name:    "serve hostname rejected",
			yaml:    "state_dir: /s\ntargets:\n  - {type: file, dir: /lists, serve: \"localhost:8080\"}\n",
			wantErr: "IP literal",
		},
		{
			name:    "serve without port rejected",
			yaml:    "state_dir: /s\ntargets:\n  - {type: file, dir: /lists, serve: \"127.0.0.1\"}\n",
			wantErr: "serve",
		},
		{
			name:    "serve non-loopback allowed when explicit",
			yaml:    "state_dir: /s\ntargets:\n  - {type: file, dir: /lists, serve: \"0.0.0.0:8080\", allow_non_loopback: true}\n",
			wantErr: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.yaml)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Load: unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Load: want error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load: want error containing %q, got %q", tt.wantErr, err)
			}
		})
	}
}

func TestDefaultsApplied(t *testing.T) {
	c, err := load(t, "state_dir: /s\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Health.DeltaThresholdPct != 40 {
		t.Errorf("DeltaThresholdPct = %d, want default 40", c.Health.DeltaThresholdPct)
	}
	if c.Health.StaleMaxDays != 14 {
		t.Errorf("StaleMaxDays = %d, want default 14", c.Health.StaleMaxDays)
	}
}

func TestDefaultsDoNotOverride(t *testing.T) {
	c, err := load(t, "state_dir: /s\nhealth:\n  delta_threshold_pct: 55\n  stale_max_days: 3\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Health.DeltaThresholdPct != 55 || c.Health.StaleMaxDays != 3 {
		t.Errorf("defaults overrode explicit values: %+v", c.Health)
	}
}

func TestSuppressPrefixes(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		want    []string // expected canonical prefixes
		wantErr bool
	}{
		{
			name:    "prefixes pass through",
			entries: []string{"192.168.0.0/16", "10.0.0.0/8"},
			want:    []string{"192.168.0.0/16", "10.0.0.0/8"},
		},
		{
			name:    "bare IPv4 becomes /32",
			entries: []string{"203.0.113.9"},
			want:    []string{"203.0.113.9/32"},
		},
		{
			name:    "bare IPv6 becomes /128",
			entries: []string{"2001:db8::1"},
			want:    []string{"2001:db8::1/128"},
		},
		{
			name:    "host bits masked",
			entries: []string{"10.1.2.3/8"},
			want:    []string{"10.0.0.0/8"},
		},
		{
			name:    "IPv6 prefix",
			entries: []string{"2001:db8::/32"},
			want:    []string{"2001:db8::/32"},
		},
		{
			name:    "garbage errors",
			entries: []string{"10.0.0.0/8", "nope"},
			wantErr: true,
		},
		{
			name:    "zoned address errors",
			entries: []string{"fe80::1%en0"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{Suppress: tt.entries}
			got, err := c.SuppressPrefixes()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("SuppressPrefixes: want error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("SuppressPrefixes: %v", err)
			}
			want := make([]netip.Prefix, len(tt.want))
			for i, s := range tt.want {
				want[i] = netip.MustParsePrefix(s)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("SuppressPrefixes = %v, want %v", got, want)
			}
		})
	}
}
