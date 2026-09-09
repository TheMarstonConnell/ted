package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

func makeBashTool() Tool {
	t := Tool{
		ToolType: "function",
		Function: Function{
			Description: "Calls bash. Commands time out after two minutes; partial output is returned on timeout.",
			Name:        "bash",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The bash command to execute.",
					},
				},
				"required": []string{"command"},
			},
		},
	}
	return t
}

// Agent is an instance-local conversation. One turn may run at a time.
// Settings and output registration are safe to access concurrently.
type Agent struct {
	mu              sync.Mutex
	persistent      bool
	createdAt       time.Time
	sessionRevision uint64
	contextUsage    ContextUsage
	settings        Settings
	messages        []Message
	busy            bool
	providers       []Provider
	logger          *zap.Logger
	respond         func(AgentResponse)

	threadID       string
	projectRoot    string
	workingDir     string
	home           string
	artifactMu     sync.Mutex
	manifestOffset int64
}

func (a *Agent) emit(response AgentResponse) {
	a.mu.Lock()
	output := a.respond
	a.mu.Unlock()
	if output != nil {
		output(response)
	}
}

// runToolCall carries out one tool call and reports the text to hand back to
// the model. A failure is reported as result text rather than as an error:
// the model reads the message and can correct itself on the next request,
// whereas abandoning the call would leave it unanswered and make every later
// request invalid.
func (a *Agent) runToolCall(toolCall ToolCall) string {
	result, _ := a.runToolCallWithScreenshots(toolCall)
	return result
}

func (a *Agent) runToolCallWithScreenshots(toolCall ToolCall) (string, []screenshotImage) {
	result, screenshots, _ := a.runToolCallContext(context.Background(), toolCall, false)
	return result, screenshots
}

func (a *Agent) runToolCallContext(ctx context.Context, toolCall ToolCall, retainFull bool) (string, []screenshotImage, string) {
	if err := ctx.Err(); err != nil {
		result := fmt.Sprintf("error: tool call cancelled before execution: %v", err)
		return result, nil, result
	}
	a.logger.Debug("running tool call",
		zap.String("tool_call_id", toolCall.Id),
		zap.String("function", toolCall.Function.Name),
		zap.String("arguments", toolCall.Function.Arguments),
	)

	if toolCall.Function.Name != "bash" {
		a.logger.Debug("tool call named an unavailable tool",
			zap.String("tool_call_id", toolCall.Id),
			zap.String("function", toolCall.Function.Name),
		)
		result := fmt.Sprintf("error: there is no tool named %q; bash is the only tool available", toolCall.Function.Name)
		return result, nil, result
	}

	var bashArgs map[string]string
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &bashArgs); err != nil {
		a.logger.Debug("could not parse tool call arguments",
			zap.String("tool_call_id", toolCall.Id),
			zap.Error(err),
		)
		result := fmt.Sprintf("error: could not parse the arguments as JSON: %s", err)
		return result, nil, result
	}

	command := bashArgs["command"]
	a.emit(
		AgentResponse{
			Content:      fmt.Sprintf("Ran shell command - %q", command),
			ResponseType: "tool",
			ToolCallID:   toolCall.Id,
			ToolName:     toolCall.Function.Name},
	)

	result, full := runBashInContext(ctx, command, DefaultToolTimeout, a.workingDir, toolEnvironment(a.threadID, a.projectRoot, a.home), retainFull)
	screenshots := a.newScreenshots(maxScreenshotsPerTurn)

	a.logger.Info("tool output captured", zap.String("tool_call_id", toolCall.Id), zap.Int("output_bytes", len(result)), zap.Int("screenshot_count", len(screenshots)))
	a.logger.Debug("tool call finished",
		zap.String("tool_call_id", toolCall.Id),
		zap.String("command", command),
		zap.String("result", result),
	)

	return result, screenshots, full
}

// Turn sends the user input to the model and runs the tool calls it asks for
// until it produces a final reply.
//
// Ordinary failed turns roll back. Cancelled turns retain a valid partial
// conversation, including cancelled results for every unexecuted tool call.
func (a *Agent) Turn(userInput string) error {
	return a.TurnContext(context.Background(), userInput)
}

// TurnContext runs a cancellable turn. It does not return until active tools
// have stopped. Output callbacks are synchronous and should return promptly.
func (a *Agent) TurnContext(ctx context.Context, userInput string) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return ErrBusy
	}
	settings := a.settings
	var provider Provider
	for _, p := range a.providers {
		if p.Name() == settings.Provider {
			provider = p
			break
		}
	}
	if provider == nil {
		a.mu.Unlock()
		return errors.New("no model available")
	}
	a.busy = true
	previousUsage := a.contextUsage
	messages := cloneMessages(a.messages)
	a.mu.Unlock()
	completed := false
	defer func() {
		a.mu.Lock()
		if completed || ctx.Err() != nil {
			a.messages = messages
			if saveErr := a.saveSessionLocked(); saveErr != nil {
				err = errors.Join(err, fmt.Errorf("turn completed, but was not saved: %w", saveErr))
			}
		} else {
			a.contextUsage = previousUsage
		}
		a.busy = false
		a.mu.Unlock()
	}()
	messages = append(messages, Message{Role: "user", Content: TextContent(userInput)})

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, completionErr := completeWithRetry(a.logger, provider, CompletionRequest{Context: ctx, Model: settings.Model, Effort: settings.Effort, Messages: messagesForModel(messages, settings.Model)}, func(attempt int, delay time.Duration) {
			a.emit(AgentResponse{ResponseType: "status", Content: fmt.Sprintf("Model connection interrupted; retrying (%d/%d) in %s…", attempt, completionAttempts-1, delay.Round(100*time.Millisecond))})
		})
		if completionErr != nil {
			a.logger.Error("completion failed", zap.String("provider", settings.Provider), zap.String("model", settings.Model), zap.Error(completionErr))
			return fmt.Errorf("completion failed %w", completionErr)
		}

		if res == nil || len(res.Choices) == 0 {
			return fmt.Errorf("completion returned no choices")
		}

		a.recordContextUsage(settings.Model, res.Usage)
		a.emit(AgentResponse{ResponseType: "usage"})

		choice := res.Choices[0]

		a.logger.Debug("parsed completion choice",
			zap.String("response_id", res.Id),
			zap.String("provider", res.Provider),
			zap.String("finish_reason", choice.FinishReason),
			zap.Int("tool_call_count", len(choice.Message.ToolCalls)),
			zap.Int("reasoning_detail_count", len(choice.Message.ReasoningDetails)),
		)

		msg := choice.Message
		msg.sourceModel = settings.Model
		messages = append(messages, msg)

		finishReason := choice.FinishReason
		if finishReason == "tool_calls" || len(msg.ToolCalls) > 0 {
			// A turn that reports tool calls but carries none would be sent
			// back unchanged on the next pass and loop without end.
			if len(msg.ToolCalls) == 0 {
				return fmt.Errorf("completion reported tool calls but sent none")
			}

			// Completed assistant text can accompany tool calls. Expose it just
			// like a final reply rather than silently hiding the block.
			if text := msg.Content.Text(); text != "" {
				a.emit(AgentResponse{ResponseType: "agent", Content: text})
			}

			// Each pass appends exactly one result and never leaves the
			// loop early, so no tool call can go unanswered.
			var screenshots []screenshotImage
			for i := 0; i < len(msg.ToolCalls); i++ {
				toolCall := msg.ToolCalls[i]
				result, found, full := a.runToolCallContext(ctx, toolCall, true)
				a.emit(AgentResponse{ResponseType: "tool_result", Content: result, FullToolOutput: full, ToolCallID: toolCall.Id, ToolName: toolCall.Function.Name})

				messages = append(messages, Message{
					Role:       "tool",
					ToolCallId: toolCall.Id,
					Content:    TextContent(result),
				})
				remaining := maxScreenshotsPerTurn - len(screenshots)
				if remaining > 0 {
					if len(found) > remaining {
						found = found[:remaining]
					}
					screenshots = append(screenshots, found...)
				}
			}
			// Function outputs must all immediately follow the assistant's
			// calls. Add screenshots only after every call is answered, as a
			// genuine multimodal user message rather than tool-result text.
			if len(screenshots) > 0 {
				messages = append(messages, Message{Role: "user", Content: imageContent(screenshots)})
			}

		} else {
			a.logger.Debug("assistant turn complete", zap.String("finish_reason", finishReason))
			a.emit(
				AgentResponse{
					Content:      msg.Content.Text(),
					ResponseType: "agent"},
			)
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	completed = true
	return nil
}

// Ready reports whether another turn or settings change can start.
func (a *Agent) Ready() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.busy
}

// SetOutput installs an optional synchronous event callback. Callbacks should
// return promptly; they may inspect settings and committed history.
func (a *Agent) SetOutput(respond func(AgentResponse)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.respond = respond
}

// AgentResponse is an output event. ResponseType "usage" has no content and
// signals that ContextUsage has changed during the current turn.
type AgentResponse struct {
	Content      string
	ResponseType string
	// Tool result metadata is populated on "tool_result" events. Content is
	// capped for provider context; FullToolOutput retains all stdout/stderr.
	ToolCallID     string
	ToolName       string
	FullToolOutput string
}

// NewAgent does not require an output callback. A nil logger discards diagnostics.
// Providers are tried in supplied order. TED_HOME selects session and browser storage;
// inherited thread and project variables are deliberately ignored.
func NewAgent(logger *zap.Logger, providers []Provider) *Agent {
	return newAgentIn(logger, providers, resolveWorkingDir())
}

func newAgentIn(logger *zap.Logger, providers []Provider, dir string) *Agent {
	if logger == nil {
		logger = zap.NewNop()
	}
	a := &Agent{
		logger:      logger,
		providers:   append([]Provider(nil), providers...),
		messages:    []Message{{Role: "system", Content: TextContent(SYSTEM_PROMPT)}},
		threadID:    newThreadID(),
		projectRoot: resolveProjectRoot(dir),
		workingDir:  dir,
		home:        tedHome(),
	}
	if models := a.ListModels(); len(models) > 0 {
		_, _ = a.SetModel(models[0].ID)
	}
	return a
}

// ThreadID returns the stable browser/tool identity for this agent. Every
// NewAgent call generates a fresh ID, including in nested ted processes.
func (a *Agent) ThreadID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.threadID
}

// ProjectRoot returns the canonical git root used as the browser profile key.
// Outside a git worktree it is the canonical construction-time working
// directory. Bash itself preserves the directory in which the agent started.
func (a *Agent) ProjectRoot() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.projectRoot
}

// Messages returns a detached snapshot of committed conversation history.
// An active turn becomes visible after success or cancellation.
func (a *Agent) Messages() []Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := cloneMessages(a.messages)
	for i := range result {
		result[i].SourceModel = result[i].sourceModel
	}
	return result
}

func cloneMessages(messages []Message) []Message {
	result := append([]Message(nil), messages...)
	for i := range result {
		result[i].Content.raw = append(json.RawMessage(nil), messages[i].Content.raw...)
		result[i].ToolCalls = append([]ToolCall(nil), messages[i].ToolCalls...)
		result[i].ReasoningDetails = nil
		for _, raw := range messages[i].ReasoningDetails {
			result[i].ReasoningDetails = append(result[i].ReasoningDetails, append(json.RawMessage(nil), raw...))
		}
	}
	return result
}

// Opaque reasoning belongs to the model that produced it. Keep it in stored
// history, but do not send it to a different model/provider. Text and tool
// exchanges remain available when switching models.
func messagesForModel(messages []Message, model string) []Message {
	result := cloneMessages(messages)
	for i := range result {
		source := result[i].sourceModel
		if source == "" {
			source = result[i].SourceModel
		}
		result[i].SourceModel = ""
		if source != model {
			result[i].ReasoningDetails = nil
			result[i].Reasoning = ""
		}
	}
	return result
}
