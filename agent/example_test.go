package agent_test

import (
	"fmt"

	"github.com/TheMarstonConnell/ted/agent"
	"go.uber.org/zap"
)

// Applications can implement Provider without importing terminal packages.
type exampleProvider struct{}

func (exampleProvider) Name() string { return "example" }
func (exampleProvider) ListModels() []agent.ModelInfo {
	return []agent.ModelInfo{{ID: "demo", Efforts: []agent.Effort{agent.EffortLow, agent.EffortHigh}, DefaultEffort: agent.EffortLow}}
}
func (exampleProvider) Complete(_ *zap.Logger, req agent.CompletionRequest) (*agent.Response, error) {
	return &agent.Response{Choices: []agent.Choice{{FinishReason: "stop", Message: agent.Message{Role: "assistant", Content: agent.TextContent("Hello!")}}}}, nil
}

func ExampleNewAgent() {
	a := agent.NewAgent(nil, []agent.Provider{exampleProvider{}})
	if _, err := a.SetModel("example/demo"); err != nil {
		panic(err)
	}
	if _, err := a.SetEffort(agent.EffortHigh); err != nil {
		panic(err)
	}
	if err := a.Turn("Hi"); err != nil {
		panic(err)
	}
	messages := a.Messages()
	fmt.Println(a.Settings().Effort)
	fmt.Println(messages[len(messages)-1].Content.Text())
	// Output:
	// high
	// Hello!
}
