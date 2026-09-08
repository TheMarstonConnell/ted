package main

import (
	"github.com/TheMarstonConnell/ted/agent"
	"testing"
)

func TestRetryNoticeIsLocalStatusNotAssistantMessage(t *testing.T) {
	m := initialModel(agent.NewAgent(nil, nil))
	updated, _ := m.Update(agent.AgentResponse{ResponseType: "status", Content: "Model connection interrupted; retrying (1/3)"})
	got := updated.(model)
	if len(got.messages) == 0 {
		t.Fatal("missing retry notice")
	}
	last := got.messages[len(got.messages)-1]
	if last.kind != commandMessage {
		t.Fatalf("retry notice kind=%v", last.kind)
	}
}
