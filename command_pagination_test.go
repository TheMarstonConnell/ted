package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/TheMarstonConnell/ted/browser"
	"github.com/spf13/cobra"
)

func TestListPaginationInvalidFlags(t *testing.T) {
	for _, tc := range []struct {
		command string
		flags   []string
	}{
		{"sessions", []string{"--page", "0"}},
		{"models", []string{"--page", "-1"}},
		{"providers", []string{"--page-size", "0"}},
		{"efforts", []string{"--page-size", "-1"}},
		{"status", []string{"--all", "--page", "2"}},
		{"tabs", []string{"--all", "--page-size", "10"}},
		{"console", []string{"--page", fmt.Sprint(int(^uint(0) >> 1)), "--page-size", "25"}},
		{"errors", []string{"--page", "0"}},
	} {
		command, flags := tc.command, tc.flags
		t.Run(command+strings.Join(flags, " "), func(t *testing.T) {
			load := func() (*agent.Agent, error) { t.Fatal("unexpected catalog load"); return nil, nil }
			var cmd *cobra.Command
			args := append([]string{}, flags...)
			switch command {
			case "sessions":
				cmd = newSessionsCommand()
				args = append(args, "--server", ":invalid")
			case "models":
				cmd = newModelsCommand(load)
			case "providers":
				cmd = newProvidersCommand(load)
			case "efforts":
				cmd = newEffortsCommand(load)
				args = append([]string{"a/model"}, args...)
			default:
				cmd = newBrowserCommand(func(context.Context, browser.Request) (browser.Response, error) {
					t.Fatal("unexpected browser call")
					return browser.Response{}, nil
				})
				args = append([]string{command}, args...)
			}
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(args)
			err := cmd.Execute()
			if err == nil || strings.Contains(err.Error(), "connect to server") {
				t.Fatalf("expected flag validation error, got %v", err)
			}
		})
	}
}
