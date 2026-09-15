package main

import (
	"fmt"
	"slices"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

type catalogAgentLoader func() (*agent.Agent, error)

func loadCatalogAgent() (*agent.Agent, error) {
	providers, err := configuredProviders(zap.NewNop())
	if err != nil {
		return nil, err
	}
	return agent.NewAgent(nil, providers), nil
}

func newModelsCommand(load catalogAgentLoader) *cobra.Command {
	var provider string
	var pagination *listPagination
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List available model IDs from configured providers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := pagination.validate(); err != nil {
				return err
			}
			a, err := load()
			if err != nil {
				return err
			}
			if provider != "" && !slices.Contains(a.ListProviders(), provider) {
				return fmt.Errorf("unknown or unconfigured provider %q", provider)
			}
			var models []string
			for _, model := range a.ListModels() {
				if provider != "" && model.Provider != provider {
					continue
				}
				models = append(models, model.ID)
			}
			start, end := pagination.bounds(len(models))
			for _, model := range models[start:end] {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), model); err != nil {
					return err
				}
			}
			return pagination.report(cmd, args, len(models), "models")
		},
	}
	pagination = addListPagination(cmd)
	cmd.Flags().StringVar(&provider, "provider", "", "List models only from this configured provider")
	return cmd
}

func newProvidersCommand(load catalogAgentLoader) *cobra.Command {
	var pagination *listPagination
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "List configured provider names",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := pagination.validate(); err != nil {
				return err
			}
			a, err := load()
			if err != nil {
				return err
			}
			providers := a.ListProviders()
			start, end := pagination.bounds(len(providers))
			for _, name := range providers[start:end] {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), name); err != nil {
					return err
				}
			}
			return pagination.report(cmd, args, len(providers), "providers")
		},
	}
	pagination = addListPagination(cmd)
	return cmd
}

func newEffortsCommand(load catalogAgentLoader) *cobra.Command {
	var pagination *listPagination
	cmd := &cobra.Command{
		Use:   "efforts <provider/model-id>",
		Short: "List supported reasoning efforts for a model",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := pagination.validate(); err != nil {
				return err
			}
			a, err := load()
			if err != nil {
				return err
			}
			if _, err := a.SetModel(args[0]); err != nil {
				return err
			}
			efforts := a.ListEfforts()
			if len(efforts) == 0 && pagination.page == 1 {
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "Model %q does not expose configurable effort.\n", args[0])
				return err
			}
			start, end := pagination.bounds(len(efforts))
			for _, effort := range efforts[start:end] {
				if _, err := fmt.Fprintln(cmd.OutOrStdout(), effort); err != nil {
					return err
				}
			}
			return pagination.report(cmd, args, len(efforts), "efforts")
		},
	}
	pagination = addListPagination(cmd)
	return cmd
}
