// Package config loads the single YAML configuration file described in
// docs/SPEC.md section 10.
package config

import (
	"fmt"
	"net/netip"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the whole framedrag configuration.
type Config struct {
	StateDir string   `yaml:"state_dir"`
	Suppress []string `yaml:"suppress"`
	Health   Health   `yaml:"health"`
	Aliases  []Alias  `yaml:"aliases"`
	Targets  []Target `yaml:"targets"`
}

// Health holds global health-check thresholds.
type Health struct {
	DeltaThresholdPct int    `yaml:"delta_threshold_pct"`
	StaleMaxDays      int    `yaml:"stale_max_days"`
	Webhook           string `yaml:"webhook"`
}

// Alias maps catalog feeds or tiers onto one firewall alias.
type Alias struct {
	Name      string   `yaml:"name"`
	Action    string   `yaml:"action"`
	Direction string   `yaml:"direction"`
	Feeds     []string `yaml:"feeds"`
}

// Target configures one output backend.
type Target struct {
	Type  string `yaml:"type"`
	Dir   string `yaml:"dir,omitempty"`
	Serve string `yaml:"serve,omitempty"`
	// AllowNonLoopback permits a serve address outside 127.0.0.0/8 and
	// ::1. It must be set explicitly: the localhost server exists to
	// feed the local firewall, never to re-serve feeds publicly
	// (docs/SPEC.md section 9).
	AllowNonLoopback bool `yaml:"allow_non_loopback,omitempty"`
}

// Load reads and validates the config file at path.
func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	dec := yaml.NewDecoder(newReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("parse %s: %w", path, err)
	}
	applyDefaults(&c)
	return c, validate(c)
}

// SuppressPrefixes parses the suppress list into prefixes. Entries may
// be CIDR prefixes or bare IPs; bare IPs become single-address
// prefixes (/32 for IPv4, /128 for IPv6). Prefixes are masked so the
// result is canonical.
func (c Config) SuppressPrefixes() ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(c.Suppress))
	for _, s := range c.Suppress {
		p, err := parseSuppress(s)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func parseSuppress(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("suppress entry %q: not a CIDR prefix or IP address", s)
	}
	if a.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("suppress entry %q: zoned addresses are not supported", s)
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}
