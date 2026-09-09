package main

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/TheMarstonConnell/ted/commands"
	"github.com/charmbracelet/x/ansi"
)

type turnDoneMsg struct{ err error }
type submitDoneMsg struct{ err error }
type remoteStateMsg bool

type pickerState struct {
	selection commands.Selection
	index     int
}

func (m *model) applyCommandResult(result commands.Result, err error) {
	if err != nil {
		m.appendMessage(commandMessage, "Error: "+err.Error())
		return
	}
	if result.Message != "" {
		m.appendMessage(commandMessage, result.Message)
	}
	if result.Selection != nil {
		m.picker = &pickerState{selection: *result.Selection}
		for i, choice := range result.Selection.Choices {
			if choice.ID == result.Selection.Current {
				m.picker.index = i
				break
			}
		}
		m.textarea.Placeholder = "Filter choices..."
	}
}

func (m model) pickerChoices() []commands.Choice {
	query := strings.ToLower(strings.TrimSpace(m.textarea.Value()))
	var choices []commands.Choice
	for _, choice := range m.picker.selection.Choices {
		if strings.Contains(strings.ToLower(choice.Label+" "+choice.ID), query) {
			choices = append(choices, choice)
		}
	}
	return choices
}

func (m *model) closePicker() {
	m.picker = nil
	m.textarea.Reset()
	m.textarea.Placeholder = "Send a message..."
	m.layout()
}

func (m model) updatePicker(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	choices := m.pickerChoices()
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.closePicker()
		return m, nil
	case "up", "ctrl+p":
		m.picker.index = max(0, m.picker.index-1)
	case "down", "ctrl+n":
		m.picker.index = min(max(0, len(choices)-1), m.picker.index+1)
	case "enter":
		if len(choices) == 0 {
			return m, nil
		}
		index := min(m.picker.index, len(choices)-1)
		result, err := m.commands.Execute(m.picker.selection.Command, choices[index].ID)
		m.closePicker()
		m.applyCommandResult(result, err)
		m.layout()
	default:
		previous := m.textarea.Value()
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		if previous != m.textarea.Value() {
			m.picker.index = 0
		}
		m.layout()
		return m, cmd
	}
	return m, nil
}

func (m model) pickerView() string {
	choices := m.pickerChoices()
	height := m.viewport.Height()
	rows := []string{m.picker.selection.Title, "↑/↓ choose · Enter confirm · Esc cancel · type to filter"}
	if len(choices) == 0 {
		rows = append(rows, "No matching choices")
	}
	visible := max(1, height-2)
	start := max(0, m.picker.index-visible+1)
	for i := start; i < min(len(choices), start+visible); i++ {
		prefix := "  "
		if i == m.picker.index {
			prefix = "> "
		}
		label := prefix + choices[i].Label
		if choices[i].ID == m.picker.selection.Current {
			label += " ✓"
		}
		rows = append(rows, label)
	}
	for i := range rows {
		rows[i] = ansi.Truncate(rows[i], max(1, m.viewport.Width()), "…")
	}
	return lipgloss.NewStyle().Width(m.viewport.Width()).MaxWidth(m.viewport.Width()).Height(height).MaxHeight(height).Render(strings.Join(rows, "\n"))
}
