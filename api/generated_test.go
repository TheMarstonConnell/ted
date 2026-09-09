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
