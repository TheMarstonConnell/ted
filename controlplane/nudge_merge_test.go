package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/TheMarstonConnell/ted/agent"
)

func TestBotNudgesMergeIntoOnePendingTurn(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	if _, err := s.Submit(a.ID, "parent work", ""); err != nil {
		t.Fatal(err)
	}
	awaitCall(t, p)
	first, err := s.SubmitMessage(a.ID, SubmitMessageRequest{Kind: "bot", SenderAgentID: "child", Text: "Blocker: waiting for API"}, "blocker")
	if err != nil {
		t.Fatal(err)
	}
	secondReq := SubmitMessageRequest{Kind: "bot", SenderAgentID: "child", Text: "Complete: API fixed; all tests pass"}
	merged, err := s.SubmitMessage(a.ID, secondReq, "complete")
	if err != nil {
		t.Fatal(err)
	}
	want := first.Text + "\n\n" + secondReq.Text
	if merged.ID != first.ID || merged.Text != want || !merged.CreatedAt.Equal(first.CreatedAt) || merged.Status != "pending" {
		t.Fatalf("merged record: %+v", merged)
	}
	for key, req := range map[string]SubmitMessageRequest{
		"blocker":  {Kind: "bot", SenderAgentID: "child", Text: first.Text},
		"complete": secondReq,
	} {
		retry, err := s.SubmitMessage(a.ID, req, key)
		if err != nil || retry != merged {
			t.Fatalf("retry %s: %+v %v", key, retry, err)
		}
	}
	_, err = s.SubmitMessage(a.ID, SubmitMessageRequest{Kind: "bot", SenderAgentID: "child", Text: "different"}, "complete")
	assertStatus(t, err, 409)
	queue, err := s.QueuedMessages(a.ID)
	if err != nil || len(queue) != 2 {
		t.Fatalf("queue: %+v %v", queue, err)
	}
	events, err := s.Events(a.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var botEvents []Event
	for _, e := range events {
		if e.Type == "message.queued" || e.Type == "message.updated" {
			var m QueuedMessage
			if err := json.Unmarshal(e.Data, &m); err != nil {
				t.Fatal(err)
			}
			if m.ID == first.ID {
				botEvents = append(botEvents, e)
			}
		}
	}
	if len(botEvents) != 2 || botEvents[0].Type != "message.queued" || botEvents[1].Type != "message.updated" {
		t.Fatalf("bot events: %+v", botEvents)
	}
	var original, updated QueuedMessage
	_ = json.Unmarshal(botEvents[0].Data, &original)
	_ = json.Unmarshal(botEvents[1].Data, &updated)
	if original.Text != first.Text || updated != merged {
		t.Fatalf("event snapshots mutated or incomplete: %+v %+v", original, updated)
	}
	p.results <- nil
	call := awaitCall(t, p)
	input := call.Messages[len(call.Messages)-1].Content.Text()
	if !strings.Contains(input, "Blocker: waiting for API") || !strings.Contains(input, secondReq.Text) {
		t.Fatalf("model did not receive both reports: %s", input)
	}
	p.results <- nil
	done := awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" })
	noCall(t, p)
	var botInputs []agent.Message
	for _, m := range done.Messages {
		if m.Kind == "bot" {
			botInputs = append(botInputs, m)
		}
	}
	if len(botInputs) != 1 || botInputs[0].Content.Text() != want || botInputs[0].SenderAgentID != "child" {
		t.Fatalf("merged conversation: %+v", botInputs)
	}
}

func TestBotMergeBoundaries(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	submit := func(kind, sender, text string) QueuedMessage {
		t.Helper()
		m, err := s.SubmitMessage(a.ID, SubmitMessageRequest{Kind: kind, SenderAgentID: sender, Text: text}, "")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	running := submit("bot", "child", "running")
	awaitCall(t, p)
	first := submit("bot", "child", "first pending")
	if first.ID == running.ID {
		t.Fatal("merged into active turn")
	}
	other := submit("bot", "other", "other sender")
	merged := submit("bot", "child", "second pending")
	if merged.ID != first.ID {
		t.Fatal("intervening bot prevented merging")
	}
	human := submit("user", "", "human follow-up")
	afterHuman := submit("bot", "child", "after human")
	if afterHuman.ID == first.ID {
		t.Fatal("new report moved ahead of human input")
	}
	anonymous1 := submit("bot", "", "anonymous one")
	anonymous2 := submit("bot", "", "anonymous two")
	if anonymous1.ID == anonymous2.ID {
		t.Fatal("unrelated anonymous reports merged")
	}
	blank1 := submit("bot", " ", "blank sender one")
	blank2 := submit("bot", " ", "blank sender two")
	if blank1.ID == blank2.ID {
		t.Fatal("blank provenance treated as identified sender")
	}
	if err := s.DeletePending(a.ID, afterHuman.ID); err != nil {
		t.Fatal(err)
	}
	afterCancel := submit("bot", "child", "after cancellation")
	if afterCancel.ID == afterHuman.ID || afterCancel.ID == first.ID {
		t.Fatal("cancelled entry or human barrier ignored")
	}
	queue, err := s.QueuedMessages(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queue[1].ID != first.ID || queue[2].ID != other.ID || queue[3].ID != human.ID || queue[1].Text != "first pending\n\nsecond pending" || queue[0].Text != "running" {
		t.Fatalf("queue order/content: %+v", queue)
	}
}

func TestBotMergeConcurrentNudges(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	if _, err := s.Submit(a.ID, "work", ""); err != nil {
		t.Fatal(err)
	}
	awaitCall(t, p)
	const count = 37
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			req := SubmitMessageRequest{Kind: "bot", SenderAgentID: "child", Text: fmt.Sprintf("report-%02d", i)}
			for range 2 {
				if _, err := s.SubmitMessage(a.ID, req, req.Text); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	queue, err := s.QueuedMessages(a.ID)
	if err != nil || len(queue) != 2 {
		t.Fatalf("queue: %+v %v", queue, err)
	}
	parts := strings.Split(queue[1].Text, "\n\n")
	seen := map[string]bool{}
	for _, part := range parts {
		if seen[part] {
			t.Fatalf("duplicate report %q", part)
		}
		seen[part] = true
	}
	if len(seen) != count {
		t.Fatalf("lost reports: %d", len(seen))
	}
	for i := range count {
		if !seen[fmt.Sprintf("report-%02d", i)] {
			t.Fatalf("missing report %d", i)
		}
	}
}

func TestBotMergePersistsAndRetriesAcrossRestart(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	if _, err := s.Submit(a.ID, "work", ""); err != nil {
		t.Fatal(err)
	}
	awaitCall(t, p)
	firstReq := SubmitMessageRequest{Kind: "bot", SenderAgentID: "child", Text: "first"}
	secondReq := SubmitMessageRequest{Kind: "bot", SenderAgentID: "child", Text: "second"}
	first, err := s.SubmitMessage(a.ID, firstReq, "first")
	if err != nil {
		t.Fatal(err)
	}
	merged, err := s.SubmitMessage(a.ID, secondReq, "second")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewService(s.dir, nil, []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close(context.Background())
	for key, req := range map[string]SubmitMessageRequest{"first": firstReq, "second": secondReq} {
		retry, err := recovered.SubmitMessage(a.ID, req, key)
		if err != nil || retry != merged || retry.ID != first.ID {
			t.Fatalf("persisted receipt: %+v %v", retry, err)
		}
	}
	got, err := recovered.GetAgent(a.ID)
	if err != nil || !got.Held || len(got.Queue) != 2 || got.Queue[1].Text != "first\n\nsecond" {
		t.Fatalf("restored agent: %+v %v", got, err)
	}
	noCall(t, p)
	if _, err := recovered.Continue(a.ID); err != nil {
		t.Fatal(err)
	}
	awaitCall(t, p)
	p.results <- nil
	awaitAgent(t, recovered, a.ID, func(a Agent) bool { return a.State == "idle" })
	fresh, err := recovered.SubmitMessage(a.ID, firstReq, "new-completion")
	if err != nil || fresh.ID == first.ID {
		t.Fatalf("merged into completed record: %+v %v", fresh, err)
	}
	awaitCall(t, p)
}

func TestBotMergeOverflowDoesNotMutateOrReserveReceipt(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	if _, err := s.Submit(a.ID, "work", ""); err != nil {
		t.Fatal(err)
	}
	awaitCall(t, p)
	first, err := s.SubmitMessage(a.ID, SubmitMessageRequest{Kind: "bot", SenderAgentID: "child", Text: strings.Repeat("界", (1<<20)-3)}, "large")
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.SubmitMessage(a.ID, SubmitMessageRequest{Kind: "bot", SenderAgentID: "child", Text: "界界"}, "overflow")
	assertStatus(t, err, 413)
	after, err := s.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("overflow changed queue, events, or metadata")
	}
	accepted, err := s.SubmitMessage(a.ID, SubmitMessageRequest{Kind: "bot", SenderAgentID: "child", Text: "界"}, "overflow")
	if err != nil || accepted.ID != first.ID || accepted.Text != first.Text+"\n\n界" {
		t.Fatalf("Unicode boundary/rejected receipt: id=%s err=%v", accepted.ID, err)
	}
}

func TestBotMergeStorageFailureRollsBack(t *testing.T) {
	for _, table := range []string{"queue_messages", "events", "receipts"} {
		t.Run(table, func(t *testing.T) {
			s, p, _, a := serviceFixture(t)
			if _, err := s.Submit(a.ID, "work", ""); err != nil {
				t.Fatal(err)
			}
			awaitCall(t, p)
			if _, err := s.SubmitMessage(a.ID, SubmitMessageRequest{Kind: "bot", SenderAgentID: "child", Text: "original"}, "first"); err != nil {
				t.Fatal(err)
			}
			// Stop workers before fault injection, leaving the pending report held.
			if err := s.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			recovered, err := NewService(s.dir, nil, []agent.Provider{p})
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close(context.Background())
			before, err := recovered.store.readState()
			if err != nil {
				t.Fatal(err)
			}
			action := "INSERT"
			if table == "queue_messages" {
				action = "UPDATE"
			}
			if _, err := recovered.store.db.Exec("CREATE TRIGGER reject_merge AFTER " + action + " ON " + table + " BEGIN SELECT RAISE(ABORT, 'merge failure'); END"); err != nil {
				t.Fatal(err)
			}
			result, err := recovered.SubmitMessage(a.ID, SubmitMessageRequest{Kind: "bot", SenderAgentID: "child", Text: "not committed"}, "failed")
			assertStatus(t, err, 503)
			if result.ID != "" {
				t.Fatal("failed merge acknowledged")
			}
			recovered.mu.RLock()
			requireDiskState(t, before, recovered.state)
			recovered.mu.RUnlock()
			disk, err := recovered.store.readState()
			if err != nil {
				t.Fatal(err)
			}
			requireDiskState(t, before, disk)
			noCall(t, p)
			if _, err := recovered.store.db.Exec("DROP TRIGGER reject_merge"); err != nil {
				t.Fatal(err)
			}
			if err := recovered.Close(context.Background()); err == nil {
				t.Fatal("storage failure hidden")
			}
			reopened, err := NewService(s.dir, nil, []agent.Provider{p})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close(context.Background())
			accepted, err := reopened.SubmitMessage(a.ID, SubmitMessageRequest{Kind: "bot", SenderAgentID: "child", Text: "retry after repair"}, "failed")
			if err != nil || accepted.Text != "original\n\nretry after repair" {
				t.Fatalf("failed merge leaked text or receipt: %+v %v", accepted, err)
			}
			awaitCall(t, p)
		})
	}
}
