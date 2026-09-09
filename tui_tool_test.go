package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/charmbracelet/x/ansi"
)

func TestToolCommandSingleLine(t *testing.T) {
	m := model{}
	for _, command := range []string{"echo hello", strings.Repeat("long command ", 50), strings.Repeat("界🙂", 60), "echo one\necho two\t\r\x1b[31m"} {
		entry := transcriptEntry{kind: toolCallMessage, content: fmt.Sprintf("Ran shell command - %q", command)}
		for _, width := range []int{0, 1, 2, 3, 8, 40, 80, 160} {
			out := ansi.Strip(m.renderEntry(entry, width))
			if strings.ContainsAny(out, "\n\r\t\x1b") || ansi.StringWidth(out) > width*3/4 {
				t.Fatalf("width %d: not a single line within budget: %q", width, out)
			}
		}
	}
}

func TestToolCommandResizesWithoutLosingContent(t *testing.T) {
	m := model{}
	original := `Ran shell command - "echo hello world and some more text"`
	entry := transcriptEntry{kind: toolCallMessage, content: original}
	narrow := ansi.Strip(m.renderEntry(entry, 40))
	if !strings.HasSuffix(narrow, `…"`) {
		t.Fatalf("expected truncated quoted command: %q", narrow)
	}
	wide := ansi.Strip(m.renderEntry(entry, 120))
	if wide != original || entry.content != original {
		t.Fatalf("full command not preserved: %q", wide)
	}
}

func TestToolResultsHidden(t *testing.T) {
	m := tuiTestModel()
	next, _ := m.Update(agent.AgentResponse{ResponseType: "tool", Content: `Ran shell command - "echo hello"`})
	m = next.(model)
	count, transcript := len(m.messages), m.transcriptContent
	for _, full := range []string{"", "full output\nlast line"} {
		next, cmd := m.Update(agent.AgentResponse{ResponseType: "tool_result", Content: "hello", FullToolOutput: full})
		m = next.(model)
		if cmd != nil || len(m.messages) != count || m.transcriptContent != transcript {
			t.Fatal("tool result changed the visible transcript")
		}
	}
	if m.messages[count-1].kind != toolCallMessage {
		t.Fatal("command missing")
	}
}
