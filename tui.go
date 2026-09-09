package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/cursor"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
	"github.com/TheMarstonConnell/ted/agent"
	"github.com/TheMarstonConnell/ted/commands"
	"github.com/charmbracelet/x/ansi"
)

// transcriptGap is the number of blank lines rendered between the transcript
// and the input area.
const transcriptGap = 1

// separatorHeight is the number of rows the transcript separator occupies:
// the line break that ends the transcript plus the blank gap lines. It is
// counted against the terminal height so the view never exceeds the screen.
const separatorHeight = 1 + transcriptGap

// inputMinHeight and inputMaxHeight bound the number of text rows in the
// input area. It starts at the minimum and grows with its content up to the
// maximum, after which it scrolls internally. The transcript above shrinks by
// the same amount, so the input appears to expand upward.
const (
	inputMinHeight = 1
	inputMaxHeight = 8
)

// messageKind identifies who produced a transcript entry so it can be styled
// accordingly when the transcript is rendered.
type messageKind int

const (
	bannerMessage messageKind = iota
	userMessage
	agentMessage
	toolCallMessage
	commandMessage
)

// transcriptEntry is one message in the conversation. Entries hold raw text
// and are styled at render time so they re-flow when the terminal is resized.
type transcriptEntry struct {
	kind    messageKind
	content string
}

type initialPromptMsg struct{}

type model struct {
	initialPrompt string
	viewport      viewport.Model
	markdown      *glamour.TermRenderer
	// toolbarStyle draws the inverted band across the bottom of the screen,
	// inputPadStyle draws the inverted padding around the text area, and
	// inputBorder is the frame drawn around that padding. The frame is drawn
	// by frameView rather than by a lipgloss border, because lipgloss applies
	// only colors to border cells and the frame must be reversed to match
	// the band.
	toolbarStyle  lipgloss.Style
	inputPadStyle lipgloss.Style
	inputBorder   lipgloss.Border
	// statusStyle draws the line beneath the input frame that names the
	// model in use and the working directory.
	statusStyle lipgloss.Style
	// directory is the working directory at startup, shown in the status
	// line so the user can see where the agent's tools operate.
	directory      string
	gitBranch      string
	messages       []transcriptEntry
	textarea       textarea.Model
	bannerStyle    lipgloss.Style
	senderStyle    lipgloss.Style
	agentStyle     lipgloss.Style
	toolCallStyle  lipgloss.Style
	err            error
	agent          *agent.Agent
	commands       *commands.Handler
	picker         *pickerState
	busy           bool
	workingSpinner spinner.Model
	// transcriptContent caches rendered messages so spinner ticks do not re-render markdown.
	transcriptContent string
	// height is the terminal height from the most recent window size
	// message. It is kept so the transcript can be resized whenever the input
	// area changes height.
	height int
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

// newMarkdownRenderer builds the renderer used for agent messages. The
// document margin and surrounding blank lines are removed so the rendered
// text sits flush with the other transcript entries, and word wrap is fixed
// to the given width so the renderer must be rebuilt when the terminal is
// resized.
func newMarkdownRenderer(width int) (*glamour.TermRenderer, error) {
	style := styles.DarkStyleConfig
	margin := uint(0)
	style.Document.Margin = &margin
	style.Document.BlockPrefix = ""
	style.Document.BlockSuffix = ""
	// Use reverse video for code rather than the theme's red inline code
	// and syntax highlighting. Keep the existing padding and block margins.
	inverse := true
	style.Code.Color = nil
	style.Code.BackgroundColor = nil
	style.Code.Inverse = &inverse
	style.CodeBlock.Color = nil
	style.CodeBlock.BackgroundColor = nil
	style.CodeBlock.Inverse = &inverse
	style.CodeBlock.Theme = ""
	style.CodeBlock.Chroma = nil
	return glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(width),
	)
}

func initialModel(agent *agent.Agent) model {
	ta := textarea.New()
	ta.Placeholder = "Send a message..."
	ta.SetVirtualCursor(false)
	ta.Focus()

	ta.Prompt = ""

	ta.SetWidth(30)

	ta.DynamicHeight = true
	ta.MinHeight = inputMinHeight
	ta.MaxHeight = inputMaxHeight
	ta.SetHeight(inputMinHeight)

	// Reverse swaps the terminal's default foreground and background colors,
	// so the user's messages, the banner, and the input box appear inverted
	// regardless of the terminal theme. Width is applied at render time to
	// fill the line.
	inverted := lipgloss.NewStyle().Reverse(true)

	// The text is inverted to match the toolbar around it. The border is
	// drawn by the view rather than by the text area's base style, because
	// the text area applies its base style twice when it shows the
	// placeholder, which would nest one frame inside another.
	ta.SetStyles(inputStyles(ta.Styles()))

	ta.ShowLineNumbers = false

	vp := viewport.New(viewport.WithWidth(30), viewport.WithHeight(5))
	vp.SetContent(`TED`)
	vp.KeyMap = scrollKeyMap()

	ta.KeyMap.InsertNewline.SetEnabled(false)

	markdown, err := newMarkdownRenderer(vp.Width())

	directory := agent.WorkingDir()
	entries := []transcriptEntry{{kind: bannerMessage, content: "Ted Coding Agent"}}
	for _, message := range agent.Messages() {
		switch message.Role {
		case "user":
			text := message.Content.Text()
			if text == "" {
				text = "[Image attachment]"
			}
			entries = append(entries, transcriptEntry{kind: userMessage, content: text})
		case "assistant":
			if text := message.Content.Text(); text != "" {
				entries = append(entries, transcriptEntry{kind: agentMessage, content: text})
			}
			for _, call := range message.ToolCalls {
				text := call.Function.Name + " " + call.Function.Arguments
				var args struct {
					Command string `json:"command"`
				}
				if call.Function.Name == "bash" && json.Unmarshal([]byte(call.Function.Arguments), &args) == nil {
					text = fmt.Sprintf("Ran shell command - %q", args.Command)
				}
				entries = append(entries, transcriptEntry{kind: toolCallMessage, content: text})
			}
		}
	}

	return model{
		textarea:      ta,
		messages:      entries,
		viewport:      vp,
		markdown:      markdown,
		toolbarStyle:  inverted.Padding(1, 2),
		inputPadStyle: inverted.Padding(1, 1),
		inputBorder:   lipgloss.DoubleBorder(),
		statusStyle:   inverted.Faint(true),
		directory:     directory,
		bannerStyle:   inverted.Bold(true).Padding(2, 2),
		senderStyle:   inverted.Padding(1, 2),
		agentStyle:    lipgloss.NewStyle(),
		toolCallStyle: lipgloss.NewStyle().Faint(true),
		err:           err,
		agent:         agent,
		commands:      commands.New(agent),
	}
}

// inputStyles returns the text area styles with every element drawn inverted
// so the input reads as one solid block.
func inputStyles(s textarea.Styles) textarea.Styles {
	inverted := lipgloss.NewStyle().Reverse(true)
	for _, state := range []*textarea.StyleState{&s.Focused, &s.Blurred} {
		state.Base = lipgloss.NewStyle()
		state.Text = inverted
		state.CursorLine = inverted
		state.Prompt = inverted
		state.EndOfBuffer = inverted
		state.LineNumber = inverted
		state.CursorLineNumber = inverted
		state.Placeholder = inverted.Faint(true)
	}
	return s
}

// frameView surrounds content with border, rendering every border glyph
// through style so the frame carries attributes such as reverse video that
// lipgloss borders cannot. Each content line is padded to the widest line
// with styled spaces so the right edge lines up.
func frameView(content string, border lipgloss.Border, style lipgloss.Style) string {
	lines := strings.Split(content, "\n")
	width := lipgloss.Width(content)
	rows := make([]string, 0, len(lines)+2)
	rows = append(rows, style.Render(border.TopLeft+strings.Repeat(border.Top, width)+border.TopRight))
	for _, line := range lines {
		gap := style.Render(strings.Repeat(" ", max(0, width-lipgloss.Width(line))))
		rows = append(rows, style.Render(border.Left)+line+gap+style.Render(border.Right))
	}
	rows = append(rows, style.Render(border.BottomLeft+strings.Repeat(border.Bottom, width)+border.BottomRight))
	return strings.Join(rows, "\n")
}

// inputFrameWidth is the number of columns the input frame and padding add
// to the text area width.
func (m model) inputFrameWidth() int {
	return m.inputPadStyle.GetHorizontalFrameSize() + lipgloss.Width(m.inputBorder.Left) + lipgloss.Width(m.inputBorder.Right)
}

// inputView renders the text area with its padding and frame.
func (m model) inputView() string {
	return frameView(m.inputPadStyle.Render(m.textarea.View()), m.inputBorder, m.inputPadStyle.UnsetPadding())
}

// statusView renders the line beneath the input frame naming the model in
// use and the working directory. It is truncated to the frame width so a
// long path cannot widen the toolbar.
func (m model) statusView() string {
	width := lipgloss.Width(m.inputView())
	settings := m.agent.Settings()
	label := settings.Model
	if settings.Effort != "" {
		label += " · " + string(settings.Effort)
	}
	meter, percent := contextMeter(m.agent.ContextUsage())
	// Reserve space for the meter even when the model or directory is long.
	available := width - ansi.StringWidth(meter) - 2
	if available < 0 {
		return m.statusStyle.Width(width).Render(ansi.Truncate(meter, width, "…"))
	}
	directory := m.directory
	if m.gitBranch != "" {
		directory += " (" + m.gitBranch + ")"
	}
	prefix := ansi.Truncate(label+"  "+directory, available, "…")
	meterStyle := lipgloss.NewStyle()
	if percent >= 90 {
		meterStyle = meterStyle.Foreground(lipgloss.Color("1"))
	} else if percent >= 80 {
		meterStyle = meterStyle.Foreground(lipgloss.Color("3"))
	}
	return m.statusStyle.Width(width).Render(prefix + "  " + meterStyle.Render(meter))
}

func contextMeter(usage agent.ContextUsage) (string, float64) {
	// Default to full capacity until the provider reports usage.
	if !usage.Known {
		return "ctx 100% left", 0
	}
	percent, known := usage.Percent()
	if !known {
		return "ctx —", 0
	}
	return fmt.Sprintf("ctx %.0f%% left", max(0, 100-percent)), percent
}

// toolbarView renders the bottom bar: the framed text area and the status
// line beneath it on an inverted band that spans the full terminal width.
func (m model) toolbarView() string {
	return m.toolbarStyle.Width(m.viewport.Width()).Render(m.inputView() + "\n" + m.statusView())
}

// toolbarHeight is the number of rows the bottom bar occupies, including the
// band padding, the input border, the input padding, and the status line.
func (m model) toolbarHeight() int {
	return lipgloss.Height(m.toolbarView())
}

// layout gives the transcript every row the toolbar and separator do not
// use. It runs after any change that can alter the toolbar height so the
// view never exceeds the terminal.
func (m *model) layout() {
	followTail := m.viewport.AtBottom()
	m.viewport.SetHeight(max(1, m.height-m.toolbarHeight()-separatorHeight))
	if followTail {
		m.viewport.GotoBottom()
	}
}

func (m model) Init() tea.Cmd {
	if strings.TrimSpace(m.initialPrompt) != "" {
		return tea.Batch(textarea.Blink, readGitBranch(m.directory), func() tea.Msg { return initialPromptMsg{} })
	}
	return tea.Batch(textarea.Blink, readGitBranch(m.directory))
}

// styleFor returns the style used to render entries of the given kind.
func (m model) styleFor(kind messageKind) lipgloss.Style {
	switch kind {
	case bannerMessage:
		return m.bannerStyle
	case userMessage:
		return m.senderStyle
	case toolCallMessage, commandMessage:
		return m.toolCallStyle
	default:
		return m.agentStyle
	}
}

// renderEntry styles one message for the given width. Agent messages are
// rendered as markdown so headings, lists, and fenced code blocks are shown
// with terminal styling; if the markdown renderer is unavailable or fails,
// the raw text is shown instead.
func (m *model) renderEntry(entry transcriptEntry, width int) string {
	if entry.kind == toolCallMessage {
		// Keep the raw entry intact so resizing can reveal more of the command.
		// Strip terminal escapes and collapse whitespace before measuring cells,
		// not bytes, so wide Unicode characters cannot cause wrapping.
		line := strings.Join(strings.Fields(ansi.Strip(entry.content)), " ")
		limit := max(0, width*3/4)
		if limit == 0 {
			return ""
		}
		tail := "…"
		if strings.HasSuffix(line, `"`) && limit >= 2 {
			tail += `"`
		}
		return m.toolCallStyle.Render(ansi.Truncate(line, limit, tail))
	}
	if entry.kind == agentMessage && m.markdown != nil {
		if out, err := m.markdown.Render(entry.content); err == nil {
			return strings.TrimRight(out, "\n")
		}
	}
	return m.styleFor(entry.kind).Width(width).Render(entry.content)
}

// renderTranscript styles and wraps every message to the viewport width,
// separates them with a blank line, and installs the result as the viewport
// content.
func (m *model) renderTranscript() {
	width := m.viewport.Width()
	rendered := make([]string, 0, len(m.messages))
	for _, entry := range m.messages {
		rendered = append(rendered, m.renderEntry(entry, width))
	}
	m.transcriptContent = strings.Join(rendered, "\n\n")
	m.refreshTranscript()
}

// refreshTranscript adds the transient working indicator without recording it
// in the conversation, and preserves the reader's scroll position.
func (m *model) refreshTranscript() {
	followTail := m.viewport.AtBottom()
	content := m.transcriptContent
	if m.busy {
		content += "\n\n" + m.toolCallStyle.Render(m.workingSpinner.View()+" working...")
	}
	m.viewport.SetContent(content)
	if followTail {
		m.viewport.GotoBottom()
	}
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

// startTurn reserves the UI turn before the asynchronous agent command runs.
func (m model) startTurn(prompt string) (tea.Model, tea.Cmd) {
	m.appendMessage(userMessage, prompt)
	m.busy = true
	m.workingSpinner = spinner.New(spinner.WithSpinner(spinner.Line))
	m.refreshTranscript()
	m.layout()
	m.viewport.GotoBottom()
	instance := m.agent
	return m, tea.Batch(m.workingSpinner.Tick, func() tea.Msg { return turnDoneMsg{err: instance.Turn(prompt)} })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case gitBranchMsg:
		m.gitBranch = string(msg)
		return m, scheduleGitBranchRefresh()
	case gitBranchRefreshMsg:
		return m, readGitBranch(m.directory)
	case spinner.TickMsg:
		if !m.busy || msg.ID != m.workingSpinner.ID() {
			return m, nil
		}
		var cmd tea.Cmd
		m.workingSpinner, cmd = m.workingSpinner.Update(msg)
		m.refreshTranscript()
		return m, cmd
	case initialPromptMsg:
		prompt := m.initialPrompt
		m.initialPrompt = ""
		if strings.TrimSpace(prompt) == "" {
			return m, nil
		}
		return m.startTurn(prompt)
	case tea.WindowSizeMsg:
		m.height = msg.Height
		m.viewport.SetWidth(msg.Width)
		m.textarea.SetWidth(msg.Width - m.toolbarStyle.GetHorizontalFrameSize() - m.inputFrameWidth())
		m.layout()

		// Word wrap is fixed at construction, so the renderer is rebuilt for
		// the new width before the transcript re-flows.
		if renderer, err := newMarkdownRenderer(msg.Width); err == nil {
			m.markdown = renderer
		}

		if len(m.messages) > 0 {
			// Wrap content before setting it.
			m.renderTranscript()
		}
		m.viewport.GotoBottom()
	case agent.AgentResponse:
		if msg.ResponseType == "usage" {
			return m, nil // Usage changed; redraw the toolbar without transcript noise.
		} else if msg.ResponseType == "status" {
			m.appendMessage(commandMessage, msg.Content)
		} else if msg.ResponseType == "tool" {
			m.appendMessage(toolCallMessage, msg.Content)
		} else {
			m.appendMessage(agentMessage, msg.Content)
		}
		return m, nil
	case turnDoneMsg:
		m.busy = false
		m.refreshTranscript()
		if msg.err != nil {
			m.appendMessage(commandMessage, "Error: "+msg.err.Error())
		}
		m.layout()
		return m, nil
	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if m.picker != nil {
			return m.updatePicker(msg)
		}
		switch msg.String() {
		case "ctrl+c", "esc":
			fmt.Println(m.textarea.Value())
			return m, tea.Quit
		case "enter":

			userInput := m.textarea.Value()
			if strings.TrimSpace(userInput) == "" {
				return m, nil
			}
			// Reserve the turn in the UI immediately, before its tea.Cmd starts.
			fields := strings.Fields(userInput)
			if m.busy && (fields[0] == "/model" || fields[0] == "/effort") {
				m.appendMessage(commandMessage, agent.ErrBusy.Error())
				return m, nil
			}
			result, handled, err := m.commands.Handle(userInput)
			if handled {
				m.textarea.Reset()
				m.applyCommandResult(result, err)
				if err == nil && result.Exit {
					return m, tea.Quit
				}
				m.layout()
				return m, nil
			}
			if m.busy {
				m.appendMessage(commandMessage, agent.ErrBusy.Error())
				return m, nil
			}
			userInput = commands.ChatText(userInput)
			m.textarea.Reset()
			return m.startTurn(userInput)

		default:
			if m.isScrollKey(msg) {
				var cmd tea.Cmd
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}

			// Send all other keypresses to the textarea. Typing can wrap onto
			// a new row or delete one, so the transcript is resized to match.
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(msg)
			m.layout()
			return m, cmd
		}

	case tea.PasteMsg, cursor.BlinkMsg:
		previous := m.textarea.Value()
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		if m.picker != nil && previous != m.textarea.Value() {
			m.picker.index = 0
		}
		m.layout()
		return m, cmd
	}

	return m, nil
}

func (m model) View() tea.View {
	viewportView := m.viewport.View()
	if m.picker != nil {
		viewportView = m.pickerView()
	}
	v := tea.NewView(viewportView + strings.Repeat("\n", separatorHeight) + m.toolbarView())
	c := m.textarea.Cursor()
	if c != nil {
		// The cursor position is relative to the text area, so it is moved
		// past the transcript, the gap, the toolbar padding, and the input
		// border and padding.
		c.X += m.toolbarStyle.GetPaddingLeft() + lipgloss.Width(m.inputBorder.Left) + m.inputPadStyle.GetPaddingLeft()
		c.Y += lipgloss.Height(viewportView) + transcriptGap +
			m.toolbarStyle.GetPaddingTop() + 1 + m.inputPadStyle.GetPaddingTop()
	}
	v.Cursor = c
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}
