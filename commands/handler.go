// Package commands adapts slash commands onto the agent's typed API. It has
// no terminal dependencies; selection requests and results are plain data.
package commands

import (
	"fmt"
	"strings"

	"github.com/TheMarstonConnell/ted/agent"
)

type Spec struct {
	Name        string
	Description string
	Usage       string
}

type Choice struct {
	ID    string
	Label string
}

type Selection struct {
	Command string
	Title   string
	Current string
	Choices []Choice
}

type Result struct {
	Exit      bool // Requests that the client exit.
	Message   string
	Selection *Selection
	Change    *agent.SettingsChange
}

type entry struct {
	spec Spec
	run  func([]string) (Result, error)
}

// Agent is the settings surface shared by local agents and API clients.
type Agent interface {
	Settings() agent.Settings
	SetModel(string) (agent.SettingsChange, error)
	SetEffort(agent.Effort) (agent.SettingsChange, error)
	ListModels() []agent.ModelInfo
	ListEfforts() []agent.Effort
	Ready() bool
}

// Lifecycle is implemented by server-backed agents. No pause operation exists:
// continue (or a new message) releases held input only after explicit restoration.
type Lifecycle interface {
	Stop() error
	Settle() error
	Unsettle() error
	Continue() error
}

type Handler struct {
	agent   Agent
	entries []entry
}

func New(a Agent) *Handler {
	h := &Handler{agent: a}
	h.entries = []entry{
		{Spec{"model", "Choose a model", "/model [provider/model-id]"}, h.model},
		{Spec{"effort", "Choose reasoning effort", "/effort [value]"}, h.effort},
		{Spec{"help", "List commands", "/help"}, h.help},
		{Spec{"exit", "Exit the application", "/exit"}, func(_ []string) (Result, error) {
			return Result{Exit: true}, nil
		}},
	}
	if lifecycle, ok := a.(Lifecycle); ok {
		for _, operation := range []struct {
			name, description string
			run               func() error
		}{
			{"stop", "Stop the current turn", lifecycle.Stop},
			{"settle", "Settle this agent", lifecycle.Settle},
			{"unsettle", "Restore visibility without starting work", lifecycle.Unsettle},
			{"continue", "Continue pending input", lifecycle.Continue},
		} {
			h.entries = append(h.entries, entry{Spec{operation.name, operation.description, "/" + operation.name}, func(args []string) (Result, error) {
				if len(args) != 0 {
					return Result{}, fmt.Errorf("usage: /%s", operation.name)
				}
				if err := operation.run(); err != nil {
					return Result{}, err
				}
				return Result{Message: operation.name + " requested"}, nil
			}})
		}
	}
	return h
}

func (h *Handler) Specs() []Spec {
	specs := make([]Spec, 0, len(h.entries))
	for _, e := range h.entries {
		specs = append(specs, e.spec)
	}
	return specs
}

// Handle returns handled=false for ordinary chat. Unknown slash commands
// are handled errors, never chat. Callers may use // to escape a literal /.
func (h *Handler) Handle(input string) (result Result, handled bool, err error) {
	text := strings.TrimSpace(input)
	if !strings.HasPrefix(text, "/") || strings.HasPrefix(text, "//") {
		return Result{}, false, nil
	}
	fields := strings.Fields(strings.TrimPrefix(text, "/"))
	if len(fields) == 0 {
		return Result{}, true, fmt.Errorf("missing command; use /help")
	}
	result, err = h.Execute(fields[0], fields[1:]...)
	return result, true, err
}

// ChatText removes one slash from escaped input without otherwise changing it.
func ChatText(input string) string {
	trimmed := strings.TrimLeft(input, " \t\r\n")
	if strings.HasPrefix(trimmed, "//") {
		prefix := len(input) - len(trimmed)
		return input[:prefix] + trimmed[1:]
	}
	return input
}

// Execute is also the picker confirmation path: submit the command and the
// stable choice ID, not a UI label or a synthesized command string.
func (h *Handler) Execute(name string, args ...string) (Result, error) {
	for _, e := range h.entries {
		if e.spec.Name == name {
			if len(args) > 1 || ((name == "help" || name == "exit") && len(args) != 0) {
				return Result{}, fmt.Errorf("usage: %s", e.spec.Usage)
			}
			return e.run(args)
		}
	}
	return Result{}, fmt.Errorf("unknown command /%s; use /help", name)
}

func (h *Handler) model(args []string) (Result, error) {
	if len(args) != 0 {
		change, err := h.agent.SetModel(args[0])
		if err != nil {
			return Result{}, err
		}
		message := "Model set to " + change.After.Model
		if change.EffortAdjusted {
			effort := string(change.After.Effort)
			if effort == "" {
				effort = "not configurable"
			}
			message += "; effort adjusted to " + effort
		}
		return Result{Message: message, Change: &change}, nil
	}
	if !h.agent.Ready() && !supportsQueue(h.agent) {
		return Result{}, agent.ErrBusy
	}
	selection := &Selection{Command: "model", Title: "Choose a model", Current: h.agent.Settings().Model}
	for _, model := range h.agent.ListModels() {
		selection.Choices = append(selection.Choices, Choice{ID: model.ID, Label: model.Name + "  (" + model.Provider + ")"})
	}
	if len(selection.Choices) == 0 {
		return Result{}, fmt.Errorf("no models available")
	}
	return Result{Selection: selection}, nil
}

func (h *Handler) effort(args []string) (Result, error) {
	if len(args) != 0 {
		change, err := h.agent.SetEffort(agent.Effort(args[0]))
		if err != nil {
			return Result{}, err
		}
		return Result{Message: "Effort set to " + string(change.After.Effort), Change: &change}, nil
	}
	if !h.agent.Ready() && !supportsQueue(h.agent) {
		return Result{}, agent.ErrBusy
	}
	settings := h.agent.Settings()
	selection := &Selection{Command: "effort", Title: "Choose effort for " + settings.Model, Current: string(settings.Effort)}
	for _, effort := range h.agent.ListEfforts() {
		selection.Choices = append(selection.Choices, Choice{ID: string(effort), Label: string(effort)})
	}
	if len(selection.Choices) == 0 {
		return Result{}, fmt.Errorf("model %q does not expose configurable effort", settings.Model)
	}
	return Result{Selection: selection}, nil
}

func (h *Handler) help(_ []string) (Result, error) {
	var lines []string
	for _, spec := range h.Specs() {
		lines = append(lines, spec.Usage+" — "+spec.Description)
	}
	lines = append(lines, "Use // to send a message beginning with a literal /.")
	return Result{Message: strings.Join(lines, "\n")}, nil
}

func supportsQueue(a Agent) bool {
	q, ok := a.(interface{ AcceptsQueuedInput() bool })
	return ok && q.AcceptsQueuedInput()
}
