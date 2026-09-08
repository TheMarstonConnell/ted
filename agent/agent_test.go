package agent

import (
	"strings"
	"testing"

	"go.uber.org/zap"
)

func testAgent() *Agent {
	return &Agent{logger: zap.NewNop()}
}

// TestEveryToolCallIsAnswered covers the rule the API enforces: a tool call
// the model asks for must come back with a result carrying the same
// identifier, whatever went wrong while carrying it out.
func TestEveryToolCallIsAnswered(t *testing.T) {
	agent := testAgent()

	calls := []ToolCall{
		{Id: "call_1", Function: FunctionCall{Name: "bash", Arguments: `{"command":"echo one"}`}},
		{Id: "call_2", Function: FunctionCall{Name: "write_file", Arguments: `{"path":"/tmp/x"}`}},
		{Id: "call_3", Function: FunctionCall{Name: "bash", Arguments: `{"command":`}},
		{Id: "call_4", Function: FunctionCall{Name: "bash", Arguments: `{"command":"exit 3"}`}},
	}

	messages := []Message{{Role: "assistant", ToolCalls: calls}}
	for _, toolCall := range calls {
		messages = append(messages, Message{
			Role:       "tool",
			ToolCallId: toolCall.Id,
			Content:    TextContent(agent.runToolCall(toolCall)),
		})
	}

	answered := make(map[string]string)
	for _, message := range messages {
		if message.Role == "tool" {
			answered[message.ToolCallId] = message.Content.Text()
		}
	}

	for _, toolCall := range calls {
		result, ok := answered[toolCall.Id]
		if !ok {
			t.Errorf("tool call %s was never answered", toolCall.Id)
			continue
		}
		if strings.TrimSpace(result) == "" {
			t.Errorf("tool call %s was answered with empty text", toolCall.Id)
		}
	}
}

func TestUnavailableToolIsReportedToTheModel(t *testing.T) {
	result := testAgent().runToolCall(ToolCall{
		Id:       "call_1",
		Function: FunctionCall{Name: "write_file", Arguments: `{"path":"/tmp/x"}`},
	})

	if !strings.Contains(result, "write_file") || !strings.Contains(result, "bash") {
		t.Errorf("result should name the missing tool and the available one, got %q", result)
	}
}

func TestMalformedArgumentsAreReportedToTheModel(t *testing.T) {
	result := testAgent().runToolCall(ToolCall{
		Id:       "call_1",
		Function: FunctionCall{Name: "bash", Arguments: `{"command":`},
	})

	if !strings.HasPrefix(result, "error:") {
		t.Errorf("result should report the parse failure, got %q", result)
	}
}

func TestCommandOutputIsReturned(t *testing.T) {
	result := testAgent().runToolCall(ToolCall{
		Id:       "call_1",
		Function: FunctionCall{Name: "bash", Arguments: `{"command":"echo hello"}`},
	})

	if strings.TrimSpace(result) != "hello" {
		t.Errorf("result = %q, want %q", result, "hello")
	}
}

func TestFailingCommandReportsOutputAndStatus(t *testing.T) {
	result := testAgent().runToolCall(ToolCall{
		Id:       "call_1",
		Function: FunctionCall{Name: "bash", Arguments: `{"command":"echo oops >&2; exit 3"}`},
	})

	if !strings.Contains(result, "oops") {
		t.Errorf("result should carry the command output, got %q", result)
	}
	if !strings.Contains(result, "exit status 3") {
		t.Errorf("result should carry the exit status, got %q", result)
	}
}

func TestToolNotificationIncludesQuotedCommand(t *testing.T) {
	a := testAgent()
	var replies []AgentResponse
	a.SetOutput(func(reply AgentResponse) { replies = append(replies, reply) })
	result := a.runToolCall(ToolCall{Function: FunctionCall{
		Name: "bash", Arguments: `{"command":"echo one\necho two"}`,
	}})
	if strings.TrimSpace(result) != "one\ntwo" {
		t.Fatalf("command execution changed: %q", result)
	}
	if len(replies) != 1 || replies[0].ResponseType != "tool" || replies[0].Content != `Ran shell command - "echo one\necho two"` {
		t.Fatalf("unexpected tool notification: %+v", replies)
	}
}
