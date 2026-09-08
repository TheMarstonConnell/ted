package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"go.uber.org/zap"
)

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "could not load .env file: %v\n", err)
		os.Exit(1)
	}

	logger, err := newLogger()
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not build logger: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = logger.Sync() }()

	openRouterKey := os.Getenv("OPENROUTER_API_KEY")
	if openRouterKey == "" {
		logger.Fatal("no openrouter key found", zap.String("variable", "OPENROUTER_API_KEY"))
	}

	agent := NewAgent(logger, openRouterKey, func(s string, t string) {
		if t == "tool" {
			fmt.Println(s)
		} else {
			fmt.Printf("> %s\n", s) // output
		}
	})

	logger.Debug("starting session", zap.String("model", agent.Model))

	input := bufio.NewScanner(os.Stdin)

	for {
		fmt.Print("< ")
		if !input.Scan() {
			break
		}

		userInput := strings.TrimSpace(input.Text())
		if userInput == "" {
			continue
		}

		if userInput == "exit" {
			fmt.Println("exiting...")
			os.Exit(0)
			return
		}

		logger.Debug("received user input", zap.Int("length", len(userInput)))

		err := agent.Turn(userInput)
		if err != nil {
			logger.Fatal("couldn't complete turn", zap.Error(err))
		}
	}

	if err := input.Err(); err != nil {
		logger.Fatal("could not read input", zap.Error(err))
	}
}
