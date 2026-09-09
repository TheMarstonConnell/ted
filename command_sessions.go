package main

import (
	"fmt"
	"github.com/TheMarstonConnell/ted/agent"
	"github.com/spf13/cobra"
	"text/tabwriter"
)

func newSessionsCommand() *cobra.Command {
	return &cobra.Command{
		Use: "sessions", Short: "List saved conversations, most recent first", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessions, err := agent.ListSessions()
			if err != nil {
				return err
			}
			if len(sessions) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No saved sessions.")
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tUPDATED (UTC)\tMODEL\tPROJECT\tTITLE")
			for _, s := range sessions {
				fmt.Fprintf(w, "%s\t%s\t%q\t%q\t%q\n", s.ID, s.UpdatedAt.UTC().Format("2006-01-02 15:04"), s.Settings.Model, s.ProjectRoot, s.Title)
			}
			return w.Flush()
		},
	}
}
