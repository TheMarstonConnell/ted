package agent

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestContentClone(t *testing.T) {
	for _, raw := range []string{`null`, `"hello"`, `[ { "type": "text", "text": "hello" }, { "type": "image_url", "image_url": { "url": "data:image/png;base64,AA==" } } ]`} {
		var original Content
		if err := json.Unmarshal([]byte(raw), &original); err != nil {
			t.Fatal(err)
		}
		cloned := original.Clone()
		if cloned.Text() != original.Text() {
			t.Fatal("clone changed readable text")
		}
		want, _ := original.MarshalJSON()
		got, _ := cloned.MarshalJSON()
		if !bytes.Equal(got, want) {
			t.Fatal("clone changed opaque content")
		}
		got[0] = '!'
		if want[0] == '!' {
			t.Fatal("clone aliases content bytes")
		}
	}
	original := TextContent("plain text")
	cloned := original.Clone()
	if !reflect.DeepEqual(cloned, original) {
		t.Fatal("clone changed plain text representation")
	}
	if err := json.Unmarshal([]byte(`"replacement"`), &cloned); err != nil {
		t.Fatal(err)
	}
	if original.Text() != "plain text" {
		t.Fatal("decode of clone changed original")
	}
}

func TestContentCloneJSONMatchesDurableRepresentation(t *testing.T) {
	values := []Content{TextContent("plain"), TextContent("\xff\xff")}
	var rich Content
	if err := json.Unmarshal([]byte(` [ { "type":"text", "text":"<rich>" } ] `), &rich); err != nil {
		t.Fatal(err)
	}
	values = append(values, rich)
	for _, original := range values {
		got, err := original.CloneJSON()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var want Content
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.raw, want.raw) || got.Text() != want.Text() {
			t.Fatalf("snapshot differs: got %#v want %#v", got, want)
		}
		got.raw[0] = '!'
		if original.raw != nil && original.raw[0] == '!' {
			t.Fatal("JSON clone aliases original")
		}
	}
}
