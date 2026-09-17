package controlplane

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/TheMarstonConnell/ted/agent"
)

func cloneDiskState(state diskState) diskState {
	cloned := diskState{Version: state.Version}
	cloned.Projects = maps.Clone(state.Projects)
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
	cloned.Receipts = maps.Clone(state.Receipts)
	return cloned
}

func cloneAgent(value Agent) Agent {
	cloned := value
	if value.ActiveSettings != nil {
		settings := *value.ActiveSettings
		cloned.ActiveSettings = &settings
	}
	cloned.Queue = cloneQueue(value.Queue)
	cloned.Messages = cloneMessages(value.Messages)
	// Preserve the former JSON snapshot omission.
	cloned.Events = nil
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
	content, err := value.Content.CloneJSON()
	if err != nil {
		panic(err)
	}
	// Private runtime provenance is excluded; SourceModel is durable.
	cloned := agent.Message{
		SourceModel:   value.SourceModel,
		Kind:          value.Kind,
		SenderAgentID: value.SenderAgentID,
		Role:          value.Role,
		Content:       content,
		Reasoning:     value.Reasoning,
		ToolCallId:    value.ToolCallId,
	}
	// Preserve omitempty normalization.
	if len(value.ReasoningDetails) != 0 {
		cloned.ReasoningDetails = make(agent.ReasoningDetails, len(value.ReasoningDetails))
		for i, raw := range value.ReasoningDetails {
			cloned.ReasoningDetails[i] = cloneRawJSON(raw)
		}
	}
	if len(value.ToolCalls) != 0 {
		cloned.ToolCalls = slices.Clone(value.ToolCalls)
	}
	return cloned
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

func cloneQueuedMessage(value QueuedMessage) QueuedMessage {
	value.Attachments = slices.Clone(value.Attachments)
	return value
}
func cloneQueue(values []QueuedMessage) []QueuedMessage {
	cloned := slices.Clone(values)
	for i := range cloned {
		cloned[i] = cloneQueuedMessage(cloned[i])
	}
	return cloned
}
