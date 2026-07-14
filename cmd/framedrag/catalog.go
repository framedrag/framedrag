package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"framedrag.dev/framedrag/internal/fetch"
)

func newCatalogCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Inspect and sync the vendored feed catalog",
	}
	cmd.AddCommand(newCatalogListCmd(a), newCatalogSyncCmd(a))
	return cmd
}

func newCatalogListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show available feeds and tiers",
		Long: "List prints every catalog feed grouped by tier, with cadence and\n" +
			"disabled or requires-key markers. The feeds.local.yaml overlay next\n" +
			"to the config file is applied when present; no config is required.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCatalogList()
		},
	}
}

func (a *app) runCatalogList() error {
	// The overlay lives next to the config, but list must also work
	// with no config at all (e.g. before first setup).
	configDir := ""
	if p, err := a.resolveConfigPath(); err == nil {
		configDir = filepath.Dir(p)
	}
	cat, err := a.loadCatalog(configDir)
	if err != nil {
		return err
	}
	if a.jsonOut {
		return writeJSON(a.stdout, toCatalogJSON(cat.Feeds))
	}
	fmt.Fprint(a.stdout, renderCatalogList(cat.Feeds))
	return nil
}

func newCatalogSyncCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "sync",
		Short: "Diff the vendored catalog against pfBlockerNG upstream",
		Long: "Sync fetches the upstream pfBlockerNG catalog and diffs it against\n" +
			"the vendored copy. The local overlay never participates in the diff.\n\n" +
			"Exits 0 when the vendored copy matches upstream, 3 when a diff was\n" +
			"found, and 1 on error.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCatalogSync(cmd.Context())
		},
	}
}

func (a *app) runCatalogSync(ctx context.Context) error {
	// No overlay: sync diffs the vendored copy against upstream only.
	cat, err := a.loadCatalog("")
	if err != nil {
		return err
	}
	diff, err := cat.Sync(ctx, fetch.NewHTTP())
	if err != nil {
		return err
	}
	if a.jsonOut {
		b, err := diff.JSON()
		if err != nil {
			return err
		}
		fmt.Fprintln(a.stdout, string(b))
	} else {
		fmt.Fprintln(a.stdout, diff.String())
	}
	if !diff.Empty() {
		return exitError{code: exitDiff}
	}
	return nil
}
