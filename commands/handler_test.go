package commands

import (
	"github.com/TheMarstonConnell/ted/agent"
	"reflect"
	"testing"
)

func TestCommands(t *testing.T) {
	a := agent.NewAgent(nil, []agent.Provider{agent.NewCodexProvider(nil), agent.NewOpenRouterProvider("")})
	h := New(a)
	before := a.Messages()
	result, handled, err := h.Handle(" /model ")
	if err != nil || !handled || result.Selection == nil || len(result.Selection.Choices) != 8 {
		t.Fatal(result, handled, err)
	}
	selection := result.Selection
	if selection.Current != a.Settings().Model {
		t.Fatal(selection)
	}
	result, err = h.Execute(selection.Command, selection.Choices[1].ID)
	if err != nil || result.Change == nil || a.Settings().Model != selection.Choices[1].ID {
		t.Fatal(result, err)
	}
	result, _, err = h.Handle("/effort")
	if err != nil || result.Selection.Current != "medium" {
		t.Fatal(result, err)
	}
	if _, _, err := h.Handle("/effort high"); err != nil || a.Settings().Effort != agent.EffortHigh {
		t.Fatal(err)
	}
	for _, text := range []string{"/unknown", "/", "/model x y", "/effort bogus", "/model nonexistent", "/help extra"} {
		if _, handled, err := h.Handle(text); !handled || err == nil {
			t.Fatalf("%q: %v %v", text, handled, err)
		}
	}
	for _, text := range []string{"hello", "effort", "//model", ""} {
		if _, handled, err := h.Handle(text); handled || err != nil {
			t.Fatalf("%q: %v %v", text, handled, err)
		}
	}
	if ChatText("  //model") != "  /model" {
		t.Fatal("escape failed")
	}
	if _, _, err := h.Handle("/model openrouter/meta/muse-spark-1.3-contributor"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Handle("/effort"); err == nil {
		t.Fatal("expected unsupported explanation")
	}
	result, _, err = h.Handle("/help")
	if err != nil || result.Message == "" || len(h.Specs()) != 4 {
		t.Fatal(result, err)
	}
	if !reflect.DeepEqual(before, a.Messages()) {
		t.Fatal("commands polluted history")
	}
}

type remoteSettingsAgent struct {
	*agent.Agent
	operations []string
}

func (*remoteSettingsAgent) Ready() bool              { return false }
func (*remoteSettingsAgent) AcceptsQueuedInput() bool { return true }
func (a *remoteSettingsAgent) Stop() error            { a.operations = append(a.operations, "stop"); return nil }
func (a *remoteSettingsAgent) Unsettle() error {
	a.operations = append(a.operations, "unsettle")
	return nil
}
func (a *remoteSettingsAgent) Settle() error {
	a.operations = append(a.operations, "settle")
	return nil
}
func (a *remoteSettingsAgent) Continue() error {
	a.operations = append(a.operations, "continue")
	return nil
}

func TestRemoteLifecycleAndBusySettingsPickers(t *testing.T) {
	a := &remoteSettingsAgent{Agent: agent.NewAgent(nil, []agent.Provider{agent.NewCodexProvider(nil)})}
	h := New(a)
	for _, name := range []string{"model", "effort"} {
		result, err := h.Execute(name)
		if err != nil || result.Selection == nil {
			t.Fatal("busy API settings picker blocked", result, err)
		}
	}
	for _, name := range []string{"stop", "settle", "unsettle", "continue"} {
		result, handled, err := h.Handle("/" + name)
		if err != nil || !handled || result.Message == "" {
			t.Fatal(result, handled, err)
		}
		if _, err := h.Execute(name, "unexpected"); err == nil {
			t.Fatal("accepted lifecycle argument")
		}
	}
	if !reflect.DeepEqual(a.operations, []string{"stop", "settle", "unsettle", "continue"}) {
		t.Fatal(a.operations)
	}
	if _, _, err := h.Handle("/pause"); err == nil {
		t.Fatal("pause must not exist")
	}
}
