package config

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
)

func newReader(b []byte) io.Reader { return bytes.NewReader(b) }

func applyDefaults(c *Config) {
	if c.Health.DeltaThresholdPct == 0 {
		c.Health.DeltaThresholdPct = 40
	}
	if c.Health.StaleMaxDays == 0 {
		c.Health.StaleMaxDays = 14
	}
}

var (
	validActions    = map[string]bool{"deny": true, "permit": true, "match": true}
	validDirections = map[string]bool{"in": true, "out": true, "both": true}
)

func validate(c Config) error {
	if c.StateDir == "" {
		return fmt.Errorf("state_dir is required")
	}
	for _, s := range c.Suppress {
		if _, err := parseSuppress(s); err != nil {
			return err
		}
	}
	seen := make(map[string]bool, len(c.Aliases))
	for _, a := range c.Aliases {
		if !strings.HasPrefix(a.Name, "fd_") {
			return fmt.Errorf("alias %q: names must carry the fd_ prefix", a.Name)
		}
		if seen[a.Name] {
			return fmt.Errorf("alias %q: duplicate name", a.Name)
		}
		seen[a.Name] = true
		if !validActions[a.Action] {
			return fmt.Errorf("alias %q: action %q must be one of deny, permit, match", a.Name, a.Action)
		}
		if !validDirections[a.Direction] {
			return fmt.Errorf("alias %q: direction %q must be one of in, out, both", a.Name, a.Direction)
		}
	}
	for i, t := range c.Targets {
		if err := validateTarget(i, t); err != nil {
			return err
		}
	}
	return nil
}

func validateTarget(i int, t Target) error {
	switch t.Type {
	case "file":
		if t.Dir == "" {
			return fmt.Errorf("target %d (file): dir is required", i)
		}
	// Later target types (e.g. "opnsense", docs/SPEC.md section 8 v2)
	// slot in as further cases here.
	default:
		return fmt.Errorf("target %d: unknown type %q (supported: file)", i, t.Type)
	}
	if t.Serve != "" && !t.AllowNonLoopback {
		if err := checkLoopback(t.Serve); err != nil {
			return fmt.Errorf("target %d (%s): %w", i, t.Type, err)
		}
	}
	return nil
}

// checkLoopback enforces docs/SPEC.md section 9: the local HTTP server
// must never accidentally re-serve feeds publicly.
func checkLoopback(serve string) error {
	host, _, err := net.SplitHostPort(serve)
	if err != nil {
		return fmt.Errorf("serve %q: %v", serve, err)
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("serve %q: host must be an IP literal", serve)
	}
	if !a.IsLoopback() {
		return fmt.Errorf("serve %q: bind address must be loopback (127.0.0.0/8 or ::1); set allow_non_loopback: true to override", serve)
	}
	return nil
}
