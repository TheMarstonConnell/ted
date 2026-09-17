package api

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

// This runs in ordinary go test / CI, not just in a developer's generation
// workflow. The script writes only into a temporary directory.
func TestGeneratedUpToDate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", "check-generated.sh")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated API drift check failed: %v\n%s", err, output)
	}
}

func TestEmbeddedSpecificationAndProviderSchemas(t *testing.T) {
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	if err = spec.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, sample := range []string{
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call","type":"function","index":0,"function":{"name":"bash","arguments":"{}"}}],"reasoning_details":[{"type":"reasoning.encrypted","data":"opaque","signature":"signed","provider_extension":{"preserve":true}}],"source_model":"provider/model"}`,
		`{"role":"tool","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}],"tool_call_id":"call"}`,
		`{"role":"assistant","content":"answer","reasoning":"reason"}`,
	} {
		var raw any
		if err = json.Unmarshal([]byte(sample), &raw); err != nil {
			t.Fatal(err)
		}
		if err = spec.Components.Schemas["Message"].Value.VisitJSON(raw); err != nil {
			t.Fatalf("valid retained message rejected: %v", err)
		}
		var message Message
		if err = json.Unmarshal([]byte(sample), &message); err != nil {
			t.Fatal(err)
		}
		roundtrip, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(roundtrip, &raw); err != nil {
			t.Fatal(err)
		}
		if err = spec.Components.Schemas["Message"].Value.VisitJSON(raw); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkspaceContract(t *testing.T) {
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}

	workspaceRoute := spec.Paths.Value("/v1/agents/{agent_id}/workspace")
	if workspaceRoute == nil || workspaceRoute.Patch == nil || workspaceRoute.Patch.OperationID != "PatchAgentWorkspace" {
		t.Fatalf("workspace route is missing or has the wrong operation ID: %#v", workspaceRoute)
	}
	branchesRoute := spec.Paths.Value("/v1/projects/{project_id}/branches")
	if branchesRoute == nil || branchesRoute.Get == nil || branchesRoute.Get.OperationID != "ListProjectBranches" {
		t.Fatalf("branches route is missing or has the wrong operation ID: %#v", branchesRoute)
	}

	selection := spec.Components.Schemas["WorkspaceSelection"].Value
	for _, sample := range []struct {
		value any
		valid bool
	}{
		{map[string]any{"mode": "current_checkout"}, true},
		{map[string]any{"mode": "worktree", "base_branch": "origin/main"}, true},
		{map[string]any{}, false},
		{map[string]any{"mode": "shared"}, false},
		{map[string]any{"mode": "worktree", "base_branch": ""}, false},
		{map[string]any{"mode": "worktree", "extra": true}, false},
	} {
		err := selection.VisitJSON(sample.value)
		if (err == nil) != sample.valid {
			t.Errorf("WorkspaceSelection validation for %#v: got %v, valid=%v", sample.value, err, sample.valid)
		}
	}

	// Workspace metadata remains optional in resource/event schemas so older
	// persisted frontend fixtures continue to type-check. The backend supplies it.
	for _, name := range []string{"Agent", "AgentSummary", "AgentUpdate", "Project"} {
		for _, required := range spec.Components.Schemas[name].Value.Required {
			if required == "workspace" || required == "workspace_defaults" {
				t.Errorf("%s unexpectedly requires %s", name, required)
			}
		}
	}
	for _, code := range []string{"invalid_workspace", "workspace_locked", "workspace_failed", "workspace_unavailable"} {
		if err := spec.Components.Schemas["ErrorCode"].Value.VisitJSON(code); err != nil {
			t.Errorf("ErrorCode rejects %q: %v", code, err)
		}
	}
}
