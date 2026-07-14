package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"framedrag.dev/framedrag/internal/fetch"
	"framedrag.dev/framedrag/internal/pipeline"
)

func newUpdateCmd(a *app) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Fetch, parse, normalize, and apply feeds to targets",
		Long: "Update runs one full cycle: fetch every configured feed, evaluate its\n" +
			"health, normalize per alias, and apply the result to each target.\n" +
			"With --dry-run everything runs except Apply and nothing is persisted.\n\n" +
			"Exits 0 when every feed is OK, 2 when any feed is SUSPECT, STALE, or\n" +
			"FAILED, and 1 on hard errors.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runUpdate(cmd.Context(), dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"print what would change; apply nothing, persist nothing")
	return cmd
}

func (a *app) runUpdate(ctx context.Context, dryRun bool) error {
	env, err := a.loadEnv(true)
	if err != nil {
		return err
	}
	res, err := pipeline.Run(ctx, pipeline.Options{
		Fetcher:    fetch.NewHTTP(),
		Store:      env.store,
		Targets:    env.targets,
		Thresholds: env.th,
		Suppress:   env.suppress,
		DryRun:     dryRun,
		Logf:       a.logf(),
	}, env.specs)
	if err != nil {
		return err
	}

	if a.jsonOut {
		if err := writeJSON(a.stdout, toRunJSON(res)); err != nil {
			return err
		}
	} else {
		fmt.Fprint(a.stdout, renderFeedTable(res.Feeds, a.verbose))
		fmt.Fprint(a.stdout, renderReports(res.Reports, a.verbose))
		if !dryRun {
			for _, t := range env.cfg.Targets {
				if t.Type == "file" && t.Serve != "" {
					fmt.Fprintf(a.stdout, "lists written to %s; run 'framedrag serve' to serve them on %s for URL table aliases\n",
						t.Dir, t.Serve)
				}
			}
		}
	}

	if !res.Healthy {
		return exitError{code: exitUnhealthy}
	}
	return nil
}
