package main

import (
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/TheMarstonConnell/ted/remote"
	"github.com/spf13/cobra"
)

func newSessionsCommand() *cobra.Command {
	var server string
	cmd := &cobra.Command{Use: "sessions", Short: "List server conversations, most recent first (including settled)", Args: cobra.NoArgs}
	cmd.Flags().StringVar(&server, "server", defaultServerURL, "Existing API server URL (never starts a server)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := checkServer(cmd.Context(), server); err != nil {
			return fmt.Errorf("connect to server %s: %w", server, err)
		}
		c := remote.New(server)
		sessions, err := c.Agents(cmd.Context(), "")
		if err != nil {
			return err
		}
		if len(sessions) == 0 {
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "No saved sessions on server.")
			return err
		}
		projects, err := c.Projects(cmd.Context())
		if err != nil {
			return err
		}
		roots := map[string]string{}
		for _, p := range projects {
			roots[p.ID] = p.Root
		}
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tUPDATED (UTC)\tMODEL\tPROJECT\tTITLE")
		for _, s := range sessions {
			fmt.Fprintf(w, "%s\t%s\t%q\t%q\t%q\n", s.ID, s.UpdatedAt.UTC().Format("2006-01-02 15:04"), s.Settings.Model, roots[s.ProjectID], s.Title)
		}
		return w.Flush()
	}
	return cmd
}
