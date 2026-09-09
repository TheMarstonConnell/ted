package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSessionRoundTrip(t *testing.T) {
	t.Setenv("TED_HOME", t.TempDir())
	a := NewAgent(nil, []Provider{settingsProvider()})
	a.messages = append(a.messages,
		Message{Role: "user", Content: TextContent("hello\nworld")},
		Message{Role: "assistant", sourceModel: "test/a", Content: TextContent("checking"), Reasoning: "thinking", ReasoningDetails: ReasoningDetails{json.RawMessage(`{"type":"reasoning.encrypted","data":"opaque"}`)}, ToolCalls: []ToolCall{{Id: "one", Function: FunctionCall{Name: "bash", Arguments: `{"command":"pwd"}`}}}},
		Message{Role: "tool", ToolCallId: "one", Content: TextContent("/project")},
		Message{Role: "user", Content: imageContent([]screenshotImage{{Data: []byte("png"), MIMEType: "image/png"}})},
		Message{Role: "assistant", sourceModel: "test/a", Content: TextContent("done")},
	)
	a.contextUsage = ContextUsage{Model: "test/a", Known: true, Estimated: true, InputTokens: 10, OutputTokens: 3, EstimatedTokens: 13}
	a.manifestOffset = 123
	if err := a.EnablePersistence(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.home, "threads", a.ThreadID(), "session.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", info.Mode())
	}
	b := NewAgent(nil, []Provider{settingsProvider()})
	if err := b.RestoreSession(a.ThreadID(), "", ""); err != nil {
		t.Fatal(err)
	}
	if b.Settings() != a.Settings() || b.ThreadID() != a.ThreadID() || b.WorkingDir() != a.WorkingDir() || b.ProjectRoot() != a.ProjectRoot() || b.contextUsage != a.contextUsage || b.manifestOffset != 123 {
		t.Fatal("state not restored")
	}
	// Content gains raw JSON on decode; compare wire form and private provenance.
	before, _ := json.Marshal(a.Messages())
	after, _ := json.Marshal(b.Messages())
	if string(before) != string(after) {
		t.Fatalf("messages differ:\n%s\n%s", before, after)
	}
	if b.messages[2].sourceModel != "test/a" {
		t.Fatal("lost provenance")
	}
	if len(messagesForModel(b.messages, "test/b")[2].ReasoningDetails) != 0 {
		t.Fatal("reasoning leaked to another model")
	}
	sessions, err := ListSessions()
	if err != nil || len(sessions) != 1 || sessions[0].Title != "hello world" {
		t.Fatal(sessions, err)
	}
	if id, err := LatestSessionID(); err != nil || id != a.ThreadID() {
		t.Fatal(id, err)
	}
	if err := b.EnablePersistence(); err != nil {
		t.Fatal(err)
	}
	if err := b.Turn("continue"); err != nil {
		t.Fatal(err)
	}
	c := NewAgent(nil, []Provider{settingsProvider()})
	if err := c.RestoreSession(b.ThreadID(), "", ""); err != nil {
		t.Fatal(err)
	}
	if got := c.Messages(); got[len(got)-2].Content.Text() != "continue" {
		t.Fatal("continued turn not saved")
	}
}

func TestSessionAutosaveAndConflict(t *testing.T) {
	t.Setenv("TED_HOME", t.TempDir())
	a := NewAgent(nil, []Provider{settingsProvider()})
	if err := a.EnablePersistence(); err != nil {
		t.Fatal(err)
	}
	b := NewAgent(nil, []Provider{settingsProvider()})
	if err := b.RestoreSession(a.ThreadID(), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetEffort(EffortHigh); err != nil {
		t.Fatal(err)
	}
	if err := b.EnablePersistence(); err == nil || !strings.Contains(err.Error(), "another process") {
		t.Fatal(err)
	}
	c := NewAgent(nil, []Provider{settingsProvider()})
	if err := c.RestoreSession(a.ThreadID(), "", ""); err != nil {
		t.Fatal(err)
	}
	if c.Settings().Effort != EffortHigh {
		t.Fatal(c.Settings())
	}
	if _, err := a.SetModel("test/b"); err != nil {
		t.Fatal(err)
	}
	d := NewAgent(nil, []Provider{settingsProvider()})
	if err := d.RestoreSession(a.ThreadID(), "", ""); err != nil || d.Settings().Model != "test/b" {
		t.Fatal(err, d.Settings())
	}
}

func TestFailedTurnDoesNotSave(t *testing.T) {
	t.Setenv("TED_HOME", t.TempDir())
	p := settingsProvider()
	p.complete = func(CompletionRequest) (*Response, error) { return &Response{}, nil }
	a := NewAgent(nil, []Provider{p})
	if err := a.EnablePersistence(); err != nil {
		t.Fatal(err)
	}
	before, err := readSession(a.home, a.ThreadID())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Turn("not committed"); err == nil {
		t.Fatal("expected failure")
	}
	after, err := readSession(a.home, a.ThreadID())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed turn changed snapshot")
	}
}

func TestSessionValidationAndOverride(t *testing.T) {
	t.Setenv("TED_HOME", t.TempDir())
	a := NewAgent(nil, []Provider{settingsProvider()})
	if err := a.EnablePersistence(); err != nil {
		t.Fatal(err)
	}
	b := NewAgent(nil, nil)
	if err := b.RestoreSession("../escape", "", ""); err == nil {
		t.Fatal("accepted traversal")
	}
	if err := b.RestoreSession(a.ThreadID(), "", ""); err == nil || !strings.Contains(err.Error(), "--model") {
		t.Fatal(err)
	}
	b = NewAgent(nil, []Provider{settingsProvider()})
	if err := b.RestoreSession(a.ThreadID(), "test/plain", ""); err != nil || b.Settings().Effort != "" {
		t.Fatal(err)
	}
	path := filepath.Join(a.home, "threads", a.ThreadID(), "session.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"version": 1`, `"version": 99`, 1))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.RestoreSession(a.ThreadID(), "", ""); err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListSessions(); err == nil {
		t.Fatal("corruption hidden")
	}
}

func TestSessionsRecentOrderAndProjectFilter(t *testing.T) {
	t.Setenv("TED_HOME", t.TempDir())
	a := NewAgent(nil, []Provider{settingsProvider()})
	if err := a.EnablePersistence(); err != nil {
		t.Fatal(err)
	}
	b := NewAgent(nil, []Provider{settingsProvider()})
	b.projectRoot = t.TempDir()
	if err := b.EnablePersistence(); err != nil {
		t.Fatal(err)
	}
	s, err := readSession(b.home, b.ThreadID())
	if err != nil {
		t.Fatal(err)
	}
	s.UpdatedAt = time.Now().Add(time.Hour)
	data, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(b.home, "threads", b.ThreadID(), "session.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	sessions, err := ListSessions()
	if err != nil || len(sessions) != 2 || sessions[0].ID != b.ThreadID() {
		t.Fatal(sessions, err)
	}
	if id, err := LatestSessionID(); err != nil || id != a.ThreadID() {
		t.Fatal(id, err)
	}
}

func TestSaveFailureLeavesSnapshotAndReportsCompletedTurn(t *testing.T) {
	t.Setenv("TED_HOME", t.TempDir())
	a := NewAgent(nil, []Provider{settingsProvider()})
	if err := a.EnablePersistence(); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(a.home, "threads", a.ThreadID(), "session.write-lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		t.Fatal(err)
	}
	before := a.Settings()
	if _, err := a.SetEffort(EffortHigh); err == nil || a.Settings() != before {
		t.Fatal("settings should roll back", err)
	}
	if err := a.Turn("completed in memory"); err == nil || !strings.Contains(err.Error(), "turn completed, but was not saved") {
		t.Fatal(err)
	}
	if len(a.Messages()) != 3 {
		t.Fatal("successful work rolled back")
	}
	s, err := readSession(a.home, a.ThreadID())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Messages) != 1 {
		t.Fatal("old snapshot modified")
	}
}
