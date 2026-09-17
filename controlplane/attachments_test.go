package controlplane

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"reflect"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
)

func testAttachment(t *testing.T) Attachment {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	return Attachment{Name: "screenshot.png", URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())}
}

func assertAttachmentContent(t *testing.T, content agent.Content, text string, attachment Attachment) {
	t.Helper()
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err = json.Unmarshal(raw, &parts); err != nil {
		t.Fatalf("not multimodal: %s", raw)
	}
	images := 0
	for _, part := range parts {
		if part.Type == "image_url" && part.ImageURL.URL == attachment.URL {
			images++
		}
	}
	if images != 1 || content.Text() != text {
		t.Fatalf("missing image or text: %s", raw)
	}
}

func TestAttachmentPersistenceCloningIdempotencyAndDelivery(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	if _, err := s.Submit(a.ID, "blocking", ""); err != nil {
		t.Fatal(err)
	}
	awaitCall(t, p)
	attachment := testAttachment(t)
	req := SubmitMessageRequest{Text: "inspect this", Attachments: []Attachment{attachment}}
	queued, err := s.SubmitMessage(a.ID, req, "image-key")
	if err != nil {
		t.Fatal(err)
	}
	req.Attachments[0].Name = "caller mutation"
	queued.Attachments[0].URL = "return mutation"
	req.Attachments[0] = attachment
	same, err := s.SubmitMessage(a.ID, req, "image-key")
	if err != nil || same.ID != queued.ID || same.Attachments[0] != attachment {
		t.Fatalf("clone/dedupe: %+v %v", same, err)
	}
	same.Attachments[0].Name = "retry mutation"
	queue, err := s.QueuedMessages(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	queue[1].Attachments[0].Name = "queue mutation"
	snapshot, err := s.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Queue[1].Attachments[0].Name = "snapshot mutation"
	different := attachment
	var differentPNG bytes.Buffer
	if err := png.Encode(&differentPNG, image.NewRGBA(image.Rect(0, 0, 3, 4))); err != nil {
		t.Fatal(err)
	}
	different.URL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(differentPNG.Bytes())
	for _, conflict := range []SubmitMessageRequest{
		{Text: req.Text},
		{Text: req.Text, Attachments: []Attachment{different}},
		{Text: req.Text, Attachments: []Attachment{{Name: "renamed", URL: attachment.URL}}},
		{Text: req.Text, Attachments: []Attachment{attachment, attachment}},
	} {
		_, err = s.SubmitMessage(a.ID, conflict, "image-key")
		assertStatus(t, err, 409)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewService(s.dir, nil, []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close(context.Background())
	snapshot, err = recovered.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Queue[1].Attachments, req.Attachments) {
		t.Fatal("durable queue lost attachments")
	}
	events, err := recovered.Events(a.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		var m QueuedMessage
		if event.Type == "message.queued" && json.Unmarshal(event.Data, &m) == nil && m.ID == queued.ID {
			found = reflect.DeepEqual(m.Attachments, req.Attachments)
		}
	}
	if !found {
		t.Fatal("replayed event lost attachments")
	}
	same, err = recovered.SubmitMessage(a.ID, req, "image-key")
	if err != nil || same.ID != queued.ID {
		t.Fatalf("durable receipt: %v", err)
	}
	if _, err = recovered.Continue(a.ID); err != nil {
		t.Fatal(err)
	}
	call := awaitCall(t, p)
	assertAttachmentContent(t, call.Messages[len(call.Messages)-1].Content, req.Text, attachment)
	p.results <- nil
	done := awaitAgent(t, recovered, a.ID, func(a Agent) bool { return a.State == "idle" })
	assertAttachmentContent(t, done.Messages[len(done.Messages)-2].Content, req.Text, attachment)
	if err = recovered.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewService(s.dir, nil, []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(context.Background())
	done, err = restarted.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertAttachmentContent(t, done.Messages[len(done.Messages)-2].Content, req.Text, attachment)
}

func TestAttachmentHTTPAndWS(t *testing.T) {
	calls := make(chan agent.CompletionRequest, 10)
	f := newHTTPFixture(t, &httpTestProvider{complete: func(r agent.CompletionRequest) (*agent.Response, error) {
		calls <- r
		return &agent.Response{Choices: []agent.Choice{{FinishReason: "stop", Message: agent.Message{Role: "assistant", Content: agent.TextContent("done")}}}}, nil
	}})
	project, err := f.s.CreateProject(CreateProjectRequest{Name: "images", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	a, err := f.s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	attachment := testAttachment(t)
	req := SubmitMessageRequest{Attachments: []Attachment{attachment}}
	body, _ := json.Marshal(req)
	path := "/v1/agents/" + a.ID + "/messages"
	f.request(http.MethodPost, path, string(body), "http-image", 202)
	call := <-calls
	assertAttachmentContent(t, call.Messages[len(call.Messages)-1].Content, "", attachment)
	awaitAgent(t, f.s, a.ID, func(a Agent) bool { return a.State == "idle" })
	c := dialHTTPWS(t, f.server)
	sendHTTPWS(t, c, map[string]any{"type": "submit", "request_id": "image", "agent_id": a.ID, "idempotency_key": "ws-image", "text": "", "attachments": req.Attachments})
	if frame := readHTTPWS(t, c); frame.Type != "ack" {
		t.Fatalf("WS submit: %+v", frame)
	}
	call = <-calls
	assertAttachmentContent(t, call.Messages[len(call.Messages)-1].Content, "", attachment)
	awaitAgent(t, f.s, a.ID, func(a Agent) bool { return a.State == "idle" })
	for _, bad := range []string{
		`{"text":"","attachments":null}`,
		`{"text":"","attachments":[]}`,
		`{"text":"","attachments":[{"name":"bad","url":"https://example.com/a.png"}]}`,
		`{"text":"","attachments":[{"url":"` + attachment.URL + `"}]}`,
		`{"text":"bot","kind":"bot","attachments":[{"name":"screen","url":"` + attachment.URL + `"}]}`,
		`{"text":"x","attachments":[{"name":"bad","url":"data:image/png;base64,aGVsbG8="}]}`,
	} {
		f.request(http.MethodPost, path, bad, "", 400)
		var command map[string]any
		_ = json.Unmarshal([]byte(bad), &command)
		command["type"] = "submit"
		command["request_id"] = "bad"
		command["agent_id"] = a.ID
		command["idempotency_key"] = "bad"
		sendHTTPWS(t, c, command)
		if frame := readHTTPWS(t, c); frame.Type != "error" {
			t.Fatalf("accepted invalid WS submit: %+v", frame)
		}
	}
	var largeImage bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&largeImage, image.NewRGBA(image.Rect(0, 0, 768, 768))); err != nil {
		t.Fatal(err)
	}
	largeAttachment := Attachment{Name: "large.png", URL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(largeImage.Bytes())}
	largeBody, _ := json.Marshal(SubmitMessageRequest{Attachments: []Attachment{largeAttachment}})
	large := string(largeBody)
	if len(large) <= maxHTTPBody {
		t.Fatal("fixture does not exercise submit-specific limit")
	}
	f.request(http.MethodPost, path, large, "large-http", 202)
	f.request(http.MethodPost, "/v1/projects", large, "", 413)
	sendHTTPWS(t, c, map[string]any{"type": "submit", "request_id": "large", "agent_id": a.ID, "idempotency_key": "large-ws", "text": "", "attachments": []Attachment{largeAttachment}})
	if frame := readHTTPWS(t, c); frame.Type != "ack" {
		t.Fatalf("large WS submit: %+v", frame)
	}
}
