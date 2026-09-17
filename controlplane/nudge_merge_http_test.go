package controlplane

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestHTTPAndWebSocketMergeBotNudges(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	server := httptest.NewServer(NewHandler(s))
	defer server.Close()
	f := &httpFixture{s: s, server: server, t: t}
	base := "/v1/agents/" + a.ID + "/messages"
	f.request("POST", base, `{"text":"work"}`, "work", 202)
	awaitCall(t, p)
	firstBody := `{"kind":"bot","sender_agent_id":"child","text":"First report"}`
	first := decodeHTTP[QueuedMessage](t, f.request("POST", base, firstBody, "first", 202))
	c := dialHTTPWS(t, server)
	command := map[string]any{"type": "submit", "request_id": "merge", "agent_id": a.ID, "idempotency_key": "second", "text": "Second report", "kind": "bot", "sender_agent_id": "child"}
	sendHTTPWS(t, c, command)
	ack := readHTTPWS(t, c)
	if ack.Type != "ack" || ack.MessageID != first.ID || ack.Status != "pending" {
		t.Fatalf("merge ack: %+v", ack)
	}
	secondBody := `{"kind":"bot","sender_agent_id":"child","text":"Second report"}`
	for key, body := range map[string]string{"first": firstBody, "second": secondBody} {
		retry := decodeHTTP[QueuedMessage](t, f.request("POST", base, body, key, 202))
		if retry.ID != first.ID || retry.Text != "First report\n\nSecond report" {
			t.Fatalf("retry: %+v", retry)
		}
	}
	command["request_id"] = "retry"
	sendHTTPWS(t, c, command)
	if retry := readHTTPWS(t, c); retry.Type != "ack" || retry.MessageID != first.ID {
		t.Fatalf("WS retry: %+v", retry)
	}
	queue := decodeHTTP[[]QueuedMessage](t, f.request("GET", base, "", "", 200))
	if len(queue) != 2 || queue[1].Text != "First report\n\nSecond report" {
		t.Fatalf("HTTP queue: %+v", queue)
	}
	current, err := s.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	subscribeHTTPWS(t, c, false, []string{a.ID}, map[string]uint64{})
	frames := drainHTTPWS(t, c, map[string]uint64{}, map[string]uint64{a.ID: current.Cursor})
	var updates []QueuedMessage
	for _, frame := range frames {
		if frame.Type == "event" && frame.Event.Type == "message.updated" {
			var message QueuedMessage
			if err := json.Unmarshal(frame.Event.Data, &message); err != nil {
				t.Fatal(err)
			}
			updates = append(updates, message)
		}
	}
	if len(updates) != 1 || !reflect.DeepEqual(updates[0], queue[1]) {
		t.Fatalf("merge replay: %+v", updates)
	}
	f.request("DELETE", base+"/"+first.ID, "", "", 204)
	cancelled := decodeHTTP[QueuedMessage](t, f.request("POST", base, secondBody, "second", 202))
	if cancelled.ID != first.ID || cancelled.Status != "cancelled" || cancelled.Text != queue[1].Text {
		t.Fatalf("retry resurrected cancelled reports: %+v", cancelled)
	}
}
