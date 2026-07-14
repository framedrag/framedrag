// Package config loads the single YAML configuration file described in
// docs/SPEC.md section 10.
package config

import (
	"fmt"
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
