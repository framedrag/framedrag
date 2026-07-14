// framedrag: curated IP reputation feeds, dragged into the null route.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is set by the linker at release time.
var version = "dev"

func main() {
	root := &cobra.Command{
		Use:           "framedrag",
		Short:         "Curated IP reputation feeds, dragged into the null route",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "", "path to config file")
	root.PersistentFlags().Bool("verbose", false, "verbose output")
	root.PersistentFlags().Bool("json", false, "machine-readable output")

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the framedrag version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("framedrag", version)
		},
	})

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "framedrag:", err)
		os.Exit(1)
	}
}
