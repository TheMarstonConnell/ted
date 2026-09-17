package controlplane

import (
	"testing"
)

func TestUserSubmissionKindPreservesLegacyReceipts(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	first, err := s.Submit(a.ID, "ordinary input", "legacy-key")
	if err != nil {
		t.Fatal(err)
	}
	awaitCall(t, p)
	// This fingerprint is the persisted receipt format before bot submissions.
	s.mu.Lock()
	r := s.state.Receipts["message:"+a.ID+":legacy-key"]
	if r.Fingerprint != fingerprint("ordinary input") {
		t.Error("user receipt format changed")
	}
	s.mu.Unlock()
	same, err := s.SubmitMessage(a.ID, SubmitMessageRequest{Text: "ordinary input", Kind: "user", Attachments: []Attachment{}}, "legacy-key")
	if err != nil || same.ID != first.ID {
		t.Fatalf("explicit user kind must reuse old receipt: %+v %v", same, err)
	}
	second, err := s.SubmitMessage(a.ID, SubmitMessageRequest{Text: "explicit user", Kind: "user"}, "explicit-key")
	if err != nil {
		t.Fatal(err)
	}
	if second.Kind != "" {
		t.Fatalf("explicit user kind was not normalized: %+v", second)
	}
	same, err = s.Submit(a.ID, "explicit user", "explicit-key")
	if err != nil || same.ID != second.ID {
		t.Fatalf("omitted user kind must reuse explicit receipt: %+v %v", same, err)
	}
	_, err = s.SubmitMessage(a.ID, SubmitMessageRequest{Text: "ordinary input", Kind: "bot"}, "legacy-key")
	assertStatus(t, err, 409)
}
