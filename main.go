package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "ted",
		Short:         "ted is an agent harness",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newTUICommand(), newModelsCommand(loadCatalogAgent), newEffortsCommand(loadCatalogAgent), newProvidersCommand(loadCatalogAgent))
	return root
}
