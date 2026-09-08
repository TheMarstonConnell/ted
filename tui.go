package main

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/cursor"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// transcriptGap is the number of blank lines rendered between the transcript
// and the input area.
const transcriptGap = 1

// separatorHeight is the number of rows the transcript separator occupies:
// the line break that ends the transcript plus the blank gap lines. It is
// counted against the terminal height so the view never exceeds the screen.
const separatorHeight = 1 + transcriptGap

// messageKind identifies who produced a transcript entry so it can be styled
// accordingly when the transcript is rendered.
type messageKind int

const (
	bannerMessage messageKind = iota
	userMessage
	agentMessage
	toolCallMessage
)

// transcriptEntry is one message in the conversation. Entries hold raw text
// and are styled at render time so they re-flow when the terminal is resized.
type transcriptEntry struct {
	kind    messageKind
	content string
}

type model struct {
	viewport      viewport.Model
	messages      []transcriptEntry
	textarea      textarea.Model
	bannerStyle   lipgloss.Style
	senderStyle   lipgloss.Style
	agentStyle    lipgloss.Style
	toolCallStyle lipgloss.Style
	err           error
	agent         *Agent
}

// scrollKeyMap returns the transcript scrolling bindings. The text area holds
// the focus and receives every other keypress, so these are limited to keys
// the text area has no meaningful use for.
func scrollKeyMap() viewport.KeyMap {
	km := viewport.DefaultKeyMap()
	km.PageUp = key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up"))
	km.PageDown = key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down"))
	km.HalfPageUp = key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "half page up"))
	km.HalfPageDown = key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "half page down"))
	km.Up = key.NewBinding(key.WithKeys("ctrl+up"), key.WithHelp("ctrl+↑", "scroll up"))
	km.Down = key.NewBinding(key.WithKeys("ctrl+down"), key.WithHelp("ctrl+↓", "scroll down"))
	km.Left.SetEnabled(false)
	km.Right.SetEnabled(false)
	return km
}

func initialModel(agent *Agent) model {
	ta := textarea.New()
	ta.Placeholder = "Send a message..."
	ta.SetVirtualCursor(false)
	ta.Focus()

	ta.Prompt = "┃ "
	ta.CharLimit = 280

	ta.SetWidth(30)
	ta.SetHeight(3)

	// Remove cursor line styling
	s := ta.Styles()
	s.Focused.CursorLine = lipgloss.NewStyle()
	ta.SetStyles(s)

	ta.ShowLineNumbers = false

	vp := viewport.New(viewport.WithWidth(30), viewport.WithHeight(5))
	vp.SetContent(`TED`)
	vp.KeyMap = scrollKeyMap()

	ta.KeyMap.InsertNewline.SetEnabled(false)

	// Reverse swaps the terminal's default foreground and background colors,
	// so the user's messages and the banner appear inverted regardless of the
	// terminal theme. Width is applied at render time to fill the line.
	inverted := lipgloss.NewStyle().Reverse(true)

	return model{
		textarea: ta,
		messages: []transcriptEntry{
			{kind: bannerMessage, content: "TED coding agent"},
		},
		viewport:      vp,
		bannerStyle:   inverted.Bold(true).Padding(1, 2),
		senderStyle:   inverted.Padding(0, 1),
		agentStyle:    lipgloss.NewStyle(),
		toolCallStyle: lipgloss.NewStyle().Faint(true),
		err:           nil,
		agent:         agent,
	}
}

func (m model) Init() tea.Cmd {
	return textarea.Blink
}

// styleFor returns the style used to render entries of the given kind.
func (m model) styleFor(kind messageKind) lipgloss.Style {
	switch kind {
	case bannerMessage:
		return m.bannerStyle
	case userMessage:
		return m.senderStyle
	case toolCallMessage:
		return m.toolCallStyle
	default:
		return m.agentStyle
	}
}

// renderTranscript styles and wraps every message to the viewport width,
// separates them with a blank line, and installs the result as the viewport
// content.
func (m *model) renderTranscript() {
	width := m.viewport.Width()
	rendered := make([]string, 0, len(m.messages))
	for _, entry := range m.messages {
		rendered = append(rendered, m.styleFor(entry.kind).Width(width).Render(entry.content))
	}
	m.viewport.SetContent(strings.Join(rendered, "\n\n"))
}

// appendMessage adds one message to the transcript. The view follows the
// newest message only when the reader is already at the bottom, so a message
// arriving mid-scroll does not yank the reader away from what they are
// reading.
func (m *model) appendMessage(kind messageKind, content string) {
	followTail := m.viewport.AtBottom()
	m.messages = append(m.messages, transcriptEntry{kind: kind, content: content})
	m.renderTranscript()
	if followTail {
		m.viewport.GotoBottom()
	}
}

// isScrollKey reports whether a keypress belongs to the transcript rather than
// the input area.
func (m model) isScrollKey(msg tea.KeyPressMsg) bool {
	km := m.viewport.KeyMap
	return key.Matches(msg, km.PageUp, km.PageDown, km.HalfPageUp, km.HalfPageDown, km.Up, km.Down)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.viewport.SetWidth(msg.Width)
		m.textarea.SetWidth(msg.Width)
		m.viewport.SetHeight(msg.Height - m.textarea.Height() - separatorHeight)

		if len(m.messages) > 0 {
			// Wrap content before setting it.
			m.renderTranscript()
		}
		m.viewport.GotoBottom()
	case AgentResponse:
		if msg.ResponseType == "tool" {
			m.appendMessage(toolCallMessage, msg.Content)
		} else {
			m.appendMessage(agentMessage, msg.Content)
		}
		m.textarea.Reset()
		return m, nil
	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			fmt.Println(m.textarea.Value())
			return m, tea.Quit
		case "enter":

			userInput := m.textarea.Value()

			if userInput == "exit" {
				fmt.Println(m.textarea.Value())
				return m, tea.Quit
			}

			m.appendMessage(userMessage, userInput)
			m.textarea.Reset()
			m.viewport.GotoBottom()
			go func() {
				err := m.agent.Turn(userInput)
				if err != nil {
					m.err = err
				}
			}()

			return m, nil
		default:
			if m.isScrollKey(msg) {
				var cmd tea.Cmd
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}

			// Send all other keypresses to the textarea.
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(msg)
			return m, cmd
		}

	case cursor.BlinkMsg:
		// Textarea should also process cursor blinks.
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m model) View() tea.View {
	viewportView := m.viewport.View()
	v := tea.NewView(viewportView + strings.Repeat("\n", separatorHeight) + m.textarea.View())
	c := m.textarea.Cursor()
	if c != nil {
		c.Y += lipgloss.Height(viewportView) + transcriptGap
	}
	v.Cursor = c
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
