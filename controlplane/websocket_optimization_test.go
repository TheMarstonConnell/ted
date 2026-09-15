package controlplane

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
	"github.com/TheMarstonConnell/ted/api"
)

func TestSummaryWSMatchesJSONWireConversion(t *testing.T) {
	full := Agent{
		ParentAgentID: "parent", ID: "agent", ProjectID: "project", Title: "title",
		Settings:       Settings{Model: "provider/model", Effort: "high"},
		ActiveSettings: &Settings{Model: "provider/active", Effort: "low"},
		Settled:        true, State: "stopping", Held: true,
		Workspace: Workspace{
			Shared: true, WorkspaceSelection: WorkspaceSelection{Mode: "worktree", BaseBranch: "main"},
			Locked: true, Status: "ready", Path: "/tmp/worktree", Branch: "branch",
			BaseCommit: "abc123", Error: "error",
		},
		ContextUsage:       agent.ContextUsage{Model: "provider/model", InputTokens: 1, OutputTokens: 2, EstimatedTokens: 3, ContextWindow: 4, Known: true, Estimated: true},
		Cursor:             12,
		LastResponseCursor: 10,
		ReadCursor:         8,
		CreatedAt:          time.Unix(1, 2).UTC(),
		UpdatedAt:          time.Unix(3, 4).UTC(),
	}
	for name, input := range map[string]Agent{"zero_optional_fields": {ID: "agent", ProjectID: "project"}, "all_fields": full} {
		t.Run(name, func(t *testing.T) {
			got, err := summaryWS(input)
			if err != nil {
				t.Fatal(err)
			}
			wire := wireAgent(input)
			wire.Messages = nil
			wire.Queue = nil
			data, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			var want api.AgentSummary
			if err = json.Unmarshal(data, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("direct conversion differs from JSON conversion:\n got: %#v\nwant: %#v", got, want)
			}
		})
	}
	if _, err := summaryWS(Agent{Cursor: math.MaxUint64}); err == nil {
		t.Fatal("uint64 cursor overflow was accepted")
	}
}

func TestValidateWSReturnsSchemaValidatedTypedCommand(t *testing.T) {
	spec, _, _, err := sharedHTTPDefinition()
	if err != nil {
		t.Fatal(err)
	}
	h := &httpAPI{spec: spec}
	command, err := h.validateWS([]byte(`{"type":"subscribe","request_id":"request","subscribe_all":true,"agent_ids":["one"],"cursors":{"one":9007199254740993}}`))
	if err != nil {
		t.Fatal(err)
	}
	if command.subscribe == nil || command.subscribe.Cursors == nil || (*command.subscribe.Cursors)["one"] != 9007199254740993 {
		t.Fatalf("integer cursor lost precision: %#v", command.subscribe)
	}
	for name, data := range map[string]string{
		"typed integer exponent":       `{"type":"subscribe","request_id":"request","cursors":{"one":1e3}}`,
		"trailing JSON":                `{"type":"subscribe","request_id":"request"}{}`,
		"non-JSON trailing whitespace": "{\"type\":\"subscribe\",\"request_id\":\"request\"}\u00a0",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.validateWS([]byte(data)); err == nil {
				t.Fatal("invalid command accepted")
			}
		})
	}
}
