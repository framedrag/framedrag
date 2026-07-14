package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the framedrag version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.jsonOut {
				return writeJSON(a.stdout, struct {
					Version string `json:"version"`
					Commit  string `json:"commit"`
					Date    string `json:"date"`
				}{version, commit, date})
			}
			fmt.Fprintf(a.stdout, "framedrag %s (commit %s, built %s)\n", version, commit, date)
			return nil
		},
	}
}
