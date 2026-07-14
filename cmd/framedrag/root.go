package main

import (
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"framedrag.dev/framedrag/internal/catalog"
	"framedrag.dev/framedrag/internal/config"
	"framedrag.dev/framedrag/internal/health"
	"framedrag.dev/framedrag/internal/pipeline"
	"framedrag.dev/framedrag/internal/state"
	"framedrag.dev/framedrag/internal/target"
)

// Default file locations. --config and --catalog override them.
const (
	defaultConfigPrimary   = "/usr/local/etc/framedrag.yaml"
	defaultConfigSecondary = "/etc/framedrag/config.yaml"
	defaultCatalogPath     = "/usr/local/share/framedrag/feeds.json"
	devCatalogPath         = "catalog/feeds.json"
	overlayName            = "feeds.local.yaml"
)

// app holds the global flags and output streams shared by all
// commands. A fresh app is built per invocation, so tests can run the
// CLI repeatedly without leaking flag state.
type app struct {
	configFlag  string
	catalogFlag string
	verbose     bool
	jsonOut     bool

	stdout io.Writer
	stderr io.Writer
}

func newRoot(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:           "framedrag",
		Short:         "Curated IP reputation feeds, dragged into the null route",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.StringVar(&a.configFlag, "config", "",
		"config file (default "+defaultConfigPrimary+", then "+defaultConfigSecondary+")")
	pf.StringVar(&a.catalogFlag, "catalog", "",
		"vendored feed catalog (default "+defaultCatalogPath+", then ./"+devCatalogPath+")")
	pf.BoolVar(&a.verbose, "verbose", false,
		"log informational detail to stderr and list every change")
	pf.BoolVar(&a.jsonOut, "json", false,
		"machine-readable JSON on stdout")

	root.AddCommand(
		newUpdateCmd(a),
		newHealthCmd(a),
		newCatalogCmd(a),
		newServeCmd(a),
		newVersionCmd(a),
	)
	return root
}

// resolveConfigPath returns the config file to use: the --config flag
// if given, else the first default location that exists.
func (a *app) resolveConfigPath() (string, error) {
	if a.configFlag != "" {
		return a.configFlag, nil
	}
	for _, p := range []string{defaultConfigPrimary, defaultConfigSecondary} {
		if fileExists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("no config file found: pass --config or create %s or %s",
		defaultConfigPrimary, defaultConfigSecondary)
}

// resolveCatalogPath returns the vendored catalog to load: the
// --catalog flag if given, else the installed default, else the
// in-repo dev copy.
func (a *app) resolveCatalogPath() (string, error) {
	if a.catalogFlag != "" {
		return a.catalogFlag, nil
	}
	for _, p := range []string{defaultCatalogPath, devCatalogPath} {
		if fileExists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("no feed catalog found: pass --catalog or install %s", defaultCatalogPath)
}

// loadCatalog loads the vendored catalog plus, when configDir is
// non-empty, the feeds.local.yaml overlay sitting next to the config.
func (a *app) loadCatalog(configDir string) (catalog.Catalog, error) {
	catPath, err := a.resolveCatalogPath()
	if err != nil {
		return catalog.Catalog{}, err
	}
	overlay := ""
	if configDir != "" {
		if p := filepath.Join(configDir, overlayName); fileExists(p) {
			overlay = p
		}
	}
	return catalog.Load(catPath, overlay)
}

// runEnv is everything the update/health pipeline needs, wired from
// config plus catalog.
type runEnv struct {
	cfg      config.Config
	cat      catalog.Catalog
	specs    []pipeline.AliasSpec
	suppress []netip.Prefix
	store    state.Store
	targets  []target.Target
	th       health.Thresholds
}

// loadEnv loads config and catalog, resolves alias feed refs, and
// builds the state store and (optionally) the targets.
func (a *app) loadEnv(withTargets bool) (*runEnv, error) {
	cfgPath, err := a.resolveConfigPath()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	cat, err := a.loadCatalog(filepath.Dir(cfgPath))
	if err != nil {
		return nil, err
	}

	specs := make([]pipeline.AliasSpec, 0, len(cfg.Aliases))
	for _, al := range cfg.Aliases {
		for _, ref := range al.Feeds {
			if len(cat.Select([]string{ref})) == 0 {
				fmt.Fprintf(a.stderr, "warning: alias %s: feed ref %q matches no enabled feeds\n", al.Name, ref)
			}
		}
		specs = append(specs, pipeline.AliasSpec{Alias: al, Feeds: cat.Select(al.Feeds)})
	}

	suppress, err := cfg.SuppressPrefixes()
	if err != nil {
		return nil, fmt.Errorf("config suppress: %w", err)
	}
	store, err := state.NewDir(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}

	env := &runEnv{
		cfg:      cfg,
		cat:      cat,
		specs:    specs,
		suppress: suppress,
		store:    store,
		th:       thresholdsFrom(cfg.Health),
	}
	if withTargets {
		env.targets, err = buildTargets(cfg)
		if err != nil {
			return nil, err
		}
	}
	return env, nil
}

// thresholdsFrom maps the config health section onto health.Thresholds.
// The rejected-line ratio has no config knob yet; the spec default
// applies.
func thresholdsFrom(h config.Health) health.Thresholds {
	th := health.DefaultThresholds()
	if h.DeltaThresholdPct > 0 {
		th.DeltaPct = h.DeltaThresholdPct
	}
	if h.StaleMaxDays > 0 {
		th.StaleMax = time.Duration(h.StaleMaxDays) * 24 * time.Hour
	}
	return th
}

// buildTargets constructs one target per config entry.
func buildTargets(cfg config.Config) ([]target.Target, error) {
	targets := make([]target.Target, 0, len(cfg.Targets))
	for i, t := range cfg.Targets {
		switch t.Type {
		case "file":
			var opts []target.Option
			if t.AllowNonLoopback {
				opts = append(opts, target.AllowNonLoopback())
			}
			tg, err := target.NewFile(t.Dir, t.Serve, opts...)
			if err != nil {
				return nil, fmt.Errorf("target %d: %w", i, err)
			}
			targets = append(targets, tg)
		default:
			return nil, fmt.Errorf("target %d: unknown type %q", i, t.Type)
		}
	}
	return targets, nil
}

// logf returns the pipeline log sink. Everything the pipeline logs is
// warning grade (suppressions, poison drops, unhealthy reasons), so it
// always reaches stderr; suppressions in particular must always be
// logged. --verbose gates only the extra listings the CLI itself adds.
func (a *app) logf() func(format string, args ...any) {
	return func(format string, args ...any) {
		fmt.Fprintf(a.stderr, format+"\n", args...)
	}
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
