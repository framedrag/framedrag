package config

import (
	"bytes"
	"fmt"
	"io"
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

func validate(c Config) error {
	if c.StateDir == "" {
		return fmt.Errorf("state_dir is required")
	}
	for _, a := range c.Aliases {
		if !strings.HasPrefix(a.Name, "fd_") {
			return fmt.Errorf("alias %q: names must carry the fd_ prefix", a.Name)
		}
	}
	return nil
}
