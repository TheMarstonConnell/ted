package api

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/browser"
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

func TestBrowserLiveCommandContract(t *testing.T) {
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	schema := spec.Components.Schemas["BrowserLiveCommand"].Value

	valid := []string{
		`{"type":"watch"}`,
		`{"type":"watch","tab_id":""}`,
		`{"type":"new"}`,
		`{"type":"new","url":"https://example.com"}`,
		`{"type":"navigate","url":"about:blank"}`,
		`{"type":"navigate","tab_id":"","url":"data:text/plain,hello"}`,
		`{"type":"back","tab_id":"tab"}`,
		`{"type":"forward","tab_id":"tab"}`,
		`{"type":"reload","tab_id":"tab"}`,
		`{"type":"close","tab_id":"tab"}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseMoved","x":0,"y":0}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseWheel","x":100000,"y":100000,"button":"none","buttons":7,"click_count":3,"delta_x":-100000,"delta_y":100000,"modifiers":15}`,
		`{"type":"mouse","tab_id":"tab","event":"mousePressed","x":1.5,"y":2.5,"button":"left"}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseReleased","x":1,"y":2,"button":"middle"}`,
		`{"type":"key","tab_id":"tab","event":"keyDown","key":"a"}`,
		`{"type":"key","tab_id":"tab","event":"keyUp","code":"KeyA"}`,
		`{"type":"key","tab_id":"tab","event":"keyDown","key":"a","code":"KeyA","text":"a","key_code":65,"modifiers":15}`,
		`{"type":"key","tab_id":"tab","event":"keyDown","key":"a","code":""}`,
		`{"type":"text","tab_id":"tab","text":"hello"}`,
		`{"type":"release","tab_id":"tab"}`,
		fmt.Sprintf(`{"type":"watch","tab_id":%q}`, strings.Repeat("😀", 256)),
		fmt.Sprintf(`{"type":"key","tab_id":"tab","event":"keyDown","key":%q}`, strings.Repeat("é", 128)),
		fmt.Sprintf(`{"type":"key","tab_id":"tab","event":"keyUp","code":%q}`, strings.Repeat("界", 128)),
		fmt.Sprintf(`{"type":"text","tab_id":"tab","text":%q}`, strings.Repeat("é", 16384)),
		fmt.Sprintf(`{"type":"new","url":%q}`, "data:,"+strings.Repeat("😀", 8192-6)),
	}
	invalid := []string{
		`{}`,
		`{"type":"unknown"}`,
		`{"type":"watch","url":"https://example.com"}`,
		`{"type":"new","tab_id":"tab"}`,
		`{"type":"navigate"}`,
		`{"type":"navigate","url":""}`,
		`{"type":"back"}`,
		`{"type":"forward","tab_id":""}`,
		`{"type":"reload","tab_id":"tab","url":"https://example.com"}`,
		`{"type":"close","tab_id":null}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseMoved","y":1}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseMoved","x":1}`,
		`{"type":"mouse","tab_id":"tab","x":1,"y":1}`,
		`{"type":"mouse","event":"mouseMoved","x":1,"y":1}`,
		`{"type":"mouse","tab_id":"tab","event":"mousePressed","x":1,"y":1}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseReleased","x":1,"y":1,"button":"none"}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseMoved","x":1,"y":1,"button":"primary"}`,
		`{"type":"mouse","tab_id":"tab","event":"keyDown","x":1,"y":1}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseMoved","x":-1,"y":1}`,
		`{"type":"mouse","tab_id":"tab","event":"mouseMoved","x":1,"y":1,"buttons":1.5}`,
		`{"type":"key","tab_id":"tab","event":"keyDown"}`,
		`{"type":"key","tab_id":"tab","event":"keyDown","key":""}`,
		`{"type":"key","tab_id":"tab","event":"keyDown","code":""}`,
		`{"type":"key","tab_id":"tab","event":"keyDown","key":"","code":"KeyA"}`,
		`{"type":"key","tab_id":"tab","event":"keypress","key":"a"}`,
		`{"type":"key","event":"keyDown","key":"a"}`,
		`{"type":"key","tab_id":"tab","event":"keyDown","key":"a","button":"left"}`,
		`{"type":"text","tab_id":"tab"}`,
		`{"type":"text","tab_id":"tab","text":""}`,
		`{"type":"text","text":"hello"}`,
		`{"type":"release"}`,
		`{"type":"release","tab_id":"tab","modifiers":0}`,
		fmt.Sprintf(`{"type":"back","tab_id":%q}`, strings.Repeat("😀", 257)),
		fmt.Sprintf(`{"type":"key","tab_id":"tab","event":"keyDown","key":%q}`, strings.Repeat("é", 129)),
		fmt.Sprintf(`{"type":"key","tab_id":"tab","event":"keyUp","code":%q}`, strings.Repeat("界", 129)),
		fmt.Sprintf(`{"type":"text","tab_id":"tab","text":%q}`, strings.Repeat("é", 16385)),
		fmt.Sprintf(`{"type":"new","url":%q}`, "data:,"+strings.Repeat("😀", 8192-6+1)),
	}

	check := func(raw string, wantValid bool) {
		t.Helper()
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatalf("bad test JSON %q: %v", raw, err)
		}
		schemaErr := schema.VisitJSON(value)
		_, parserErr := browser.ParseLiveCommand([]byte(raw))
		if got := schemaErr == nil; got != wantValid {
			t.Errorf("schema validity for %s = %v, want %v (error: %v)", raw, got, wantValid, schemaErr)
		}
		if got := parserErr == nil; got != wantValid {
			t.Errorf("parser validity for %s = %v, want %v (error: %v)", raw, got, wantValid, parserErr)
		}
	}
	for _, raw := range valid {
		check(raw, true)
	}
	for _, raw := range invalid {
		check(raw, false)
	}
}

func TestBrowserLiveEventContract(t *testing.T) {
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	schema := spec.Components.Schemas["BrowserLiveEvent"].Value

	emitted := []browser.LiveEvent{
		{Type: "state"},
		{Type: "state", Tabs: []browser.LiveTab{}, Selected: "agent-tab", Pinned: "viewer-tab", TabID: "viewer-tab"},
		{Type: "state", Tabs: []browser.LiveTab{{ID: "tab", URL: "https://example.com", Title: "Example"}}},
		{Type: "frame", TabID: "tab", Data: "/9j/2Q==", Width: 1280, Height: 720},
		{Type: "activity", TabID: "tab", Kind: "move"},
		{Type: "activity", TabID: "tab", Kind: "click", X: 12.5, Y: 20},
		{Type: "error", Code: "tab_not_found", Message: "Tab was closed"},
	}
	invalid := []string{
		`{}`,
		`{"type":"unknown"}`,
		`{"type":"state","code":"invalid"}`,
		`{"type":"state","tabs":[{"id":"tab","url":"https://example.com"}]}`,
		`{"type":"frame"}`,
		`{"type":"frame","tab_id":"tab","data":"/9j/2Q==","width":1280}`,
		`{"type":"frame","tab_id":"tab","data":"/9j/2Q==","width":1280,"height":720,"selected":"tab"}`,
		`{"type":"activity","kind":"move"}`,
		`{"type":"activity","tab_id":"tab"}`,
		`{"type":"activity","tab_id":"tab","kind":"type","x":1,"y":2}`,
		`{"type":"error","code":"invalid"}`,
		`{"type":"error","message":"invalid command"}`,
		`{"type":"error","code":"invalid","message":"invalid command","tab_id":"tab"}`,
	}

	check := func(raw string, wantValid bool) {
		t.Helper()
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatalf("bad test JSON %q: %v", raw, err)
		}
		err := schema.VisitJSON(value)
		if got := err == nil; got != wantValid {
			t.Errorf("event schema validity for %s = %v, want %v (error: %v)", raw, got, wantValid, err)
		}
	}
	for _, event := range emitted {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		check(string(raw), true)
	}
	for _, raw := range invalid {
		check(raw, false)
	}
}
