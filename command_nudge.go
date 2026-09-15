package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/TheMarstonConnell/ted/remote"
	"github.com/spf13/cobra"
)

func newNudgeCommand() *cobra.Command {
	var server, key string
	cmd := &cobra.Command{
		Use:   "nudge <chat-id> <message>",
		Short: "Queue a bot notification and wake a chat without waiting for its response",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return fmt.Errorf("chat ID cannot be empty")
			}
			if strings.TrimSpace(args[1]) == "" {
				return fmt.Errorf("message cannot be empty")
			}
			if cmd.Flags().Changed("idempotency-key") && strings.TrimSpace(key) == "" {
				return fmt.Errorf("idempotency key cannot be empty")
			}
			if err := checkServer(cmd.Context(), server); err != nil {
				return fmt.Errorf("connect to server %s: %w", server, err)
			}
			m, err := remote.New(server).Nudge(cmd.Context(), args[0], args[1], os.Getenv("TED_THREAD_ID"), key)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Nudge accepted: %s\n", m.ID)
			return err
		},
	}
	cmd.Flags().StringVar(&server, "server", defaultServerURL, "Existing API server URL (never starts a server)")
	cmd.Flags().StringVar(&key, "idempotency-key", "", "Stable key for retrying the same notification without duplicate delivery")
	return cmd
}
