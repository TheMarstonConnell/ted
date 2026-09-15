package controlplane

import (
	"encoding/json"

	"github.com/TheMarstonConnell/ted/agent"
)

// New snapshot types require an explicit deep-copy implementation.
type cloneable interface {
	diskState | Agent | []agent.Message | []Event
}

// Snapshots preserve wire fields and detach all mutable backing storage.
func cloneSnapshot[T cloneable](value T) T {
	switch value := any(value).(type) {
	case diskState:
		return any(cloneDiskState(value)).(T)
	case Agent:
		return any(cloneAgent(value)).(T)
	case []agent.Message:
		return any(cloneMessages(value)).(T)
	case []Event:
		return any(cloneEvents(value)).(T)
	default:
		panic("unreachable clone type")
	}
}

func cloneDiskState(state diskState) diskState {
	cloned := diskState{Version: state.Version}
	if state.Projects != nil {
		cloned.Projects = make(map[string]Project, len(state.Projects))
		for id, project := range state.Projects {
			cloned.Projects[id] = project
		}
	}
	if state.Agents != nil {
		cloned.Agents = make(map[string]*storedAgent, len(state.Agents))
		for id, stored := range state.Agents {
			if stored == nil {
				cloned.Agents[id] = nil
				continue
			}
			cloned.Agents[id] = &storedAgent{
				Agent:          cloneAgent(stored.Agent),
				Events:         cloneEvents(stored.Events),
				ManifestOffset: stored.ManifestOffset,
			}
		}
	}
	if state.Receipts != nil {
		cloned.Receipts = make(map[string]receipt, len(state.Receipts))
		for key, value := range state.Receipts {
			cloned.Receipts[key] = value
		}
	}
	return cloned
}

func cloneAgent(value Agent) Agent {
	cloned := value
	if value.ActiveSettings != nil {
		settings := *value.ActiveSettings
		cloned.ActiveSettings = &settings
	}
	cloned.Queue = cloneQueuedMessages(value.Queue)
	cloned.Messages = cloneMessages(value.Messages)
	// Preserve the former JSON snapshot omission.
	cloned.Events = nil
	return cloned
}

func cloneQueuedMessages(values []QueuedMessage) []QueuedMessage {
	if values == nil {
		return nil
	}
	cloned := make([]QueuedMessage, len(values))
	copy(cloned, values)
	return cloned
}

func cloneMessages(values []agent.Message) []agent.Message {
	if values == nil {
		return nil
	}
	cloned := make([]agent.Message, len(values))
	for i, value := range values {
		cloned[i] = cloneMessage(value)
	}
	return cloned
}

func cloneMessage(value agent.Message) agent.Message {
	// Private runtime provenance is excluded; SourceModel is durable.
	cloned := agent.Message{
		SourceModel: value.SourceModel,
		Role:        value.Role,
		Content:     cloneContent(value.Content),
		Reasoning:   value.Reasoning,
		ToolCallId:  value.ToolCallId,
	}
	// Preserve omitempty normalization.
	if len(value.ReasoningDetails) != 0 {
		cloned.ReasoningDetails = make(agent.ReasoningDetails, len(value.ReasoningDetails))
		for i, raw := range value.ReasoningDetails {
			cloned.ReasoningDetails[i] = cloneRawJSON(raw)
		}
	}
	if len(value.ToolCalls) != 0 {
		cloned.ToolCalls = make([]agent.ToolCall, len(value.ToolCalls))
		copy(cloned.ToolCalls, value.ToolCalls)
	}
	return cloned
}

func cloneContent(value agent.Content) agent.Content {
	snapshot, err := value.CloneJSON()
	if err != nil {
		panic(err)
	}
	return snapshot
}

func cloneEvents(values []Event) []Event {
	if values == nil {
		return nil
	}
	cloned := make([]Event, len(values))
	for i, value := range values {
		cloned[i] = value
		cloned[i].Data = cloneRawJSON(value.Data)
	}
	return cloned
}

func cloneRawJSON(value json.RawMessage) json.RawMessage {
	// Preserve raw JSON validation, canonicalization, and nil-to-null behavior.
	cloned, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return json.RawMessage(cloned)
}
