package agent

// TokenUsage is usage for one completion, not a cumulative conversation total.
// Cached input and reasoning output are already included in these counts.
// JSON names match OpenRouter; the Responses adapter normalizes OpenAI names.
type TokenUsage struct {
	InputTokens  int64 `json:"prompt_tokens"`
	OutputTokens int64 `json:"completion_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

// ContextUsage is the latest request's input + output estimate. It does not
// include tool results or user input appended since that response. Missing
// usage and unknown model capacity are distinct from measured zero usage.
type ContextUsage struct {
	Model           string
	InputTokens     int64
	OutputTokens    int64
	EstimatedTokens int64
	ContextWindow   int64
	Known           bool
	Estimated       bool
}

// Percent reports an approximate context percentage, without clamping it.
func (u ContextUsage) Percent() (float64, bool) {
	if !u.Known || u.ContextWindow <= 0 {
		return 0, false
	}
	return 100 * float64(u.EstimatedTokens) / float64(u.ContextWindow), true
}

// ContextUsage returns a detached snapshot, safe to read during a turn.
func (a *Agent) ContextUsage() ContextUsage {
	a.mu.Lock()
	defer a.mu.Unlock()
	u := a.contextUsage
	u.Model = a.settings.Model
	u.ContextWindow = a.currentModel().ContextWindow
	return u
}

func (a *Agent) recordContextUsage(model string, usage *TokenUsage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	u := ContextUsage{Model: model}
	if usage != nil && usage.InputTokens >= 0 && usage.OutputTokens >= 0 {
		u.InputTokens = usage.InputTokens
		u.OutputTokens = usage.OutputTokens
		u.EstimatedTokens = usage.InputTokens + usage.OutputTokens
		u.Known = true
		u.Estimated = true
	}
	a.contextUsage = u
}
