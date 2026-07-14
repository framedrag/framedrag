package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"framedrag.dev/framedrag/internal/fetch"
	"framedrag.dev/framedrag/internal/pipeline"
)

func newHealthCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check every feed and print a status table",
		Long: "Health answers \"is this actually working?\": it fetches and evaluates\n" +
			"every configured feed exactly like update, but never applies targets,\n" +
			"never persists state, and never moves the health baselines.\n\n" +
			"Exits 0 when every feed is OK, 2 when any feed is SUSPECT, STALE, or\n" +
			"FAILED, and 1 on hard errors.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runHealth(cmd.Context())
		},
	}
}

func (a *app) runHealth(ctx context.Context) error {
	env, err := a.loadEnv(false)
	if err != nil {
		return err
	}
	// Always a dry run with no targets: health reports, it never mutates.
	res, err := pipeline.Run(ctx, pipeline.Options{
		Fetcher:    fetch.NewHTTP(),
		Store:      env.store,
		Thresholds: env.th,
		Suppress:   env.suppress,
		DryRun:     true,
		Logf:       a.logf(),
	}, env.specs)
	if err != nil {
		return err
	}

	if a.jsonOut {
		if err := writeJSON(a.stdout, toHealthJSON(res)); err != nil {
			return err
		}
	} else {
		fmt.Fprint(a.stdout, renderFeedTable(res.Feeds, true))
	}

	if !res.Healthy {
		return exitError{code: exitUnhealthy}
	}
	return nil
}
