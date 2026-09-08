package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

func main() {

	logger, err := newLogger()
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not build logger: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = logger.Sync() }()

	if err := godotenv.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "could not load .env file: %v\n", err)
		os.Exit(1)
	}

	openRouterKey := os.Getenv("OPENROUTER_API_KEY")
	if openRouterKey == "" {
		logger.Fatal("no openrouter key found", zap.String("variable", "OPENROUTER_API_KEY"))
	}

	agent := NewAgent(logger, openRouterKey)

	p := tea.NewProgram(initialModel(agent))

	agent.SetOutput(func(res AgentResponse) {
		p.Send(res)
	})

	if _, err := p.Run(); err != nil {
		logger.Fatal("could not run tea program", zap.Error(err))
	}

}
