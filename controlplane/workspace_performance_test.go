package controlplane

import (
	"testing"
	"time"
)

func TestUpdateWorkspaceNormalizationDoesNotBlockReadsAndCannotBeatFirstSubmit(t *testing.T) {
	s, _, project, a := serviceFixture(t)

	normalizationStarted := make(chan struct{})
	releaseNormalization := make(chan struct{})
	updateDone := make(chan error, 1)
	go func() {
		_, err := s.updateWorkspace(a.ID, WorkspaceSelection{Mode: "worktree", BaseBranch: "origin/main"}, func(root string, selection WorkspaceSelection) (WorkspaceSelection, error) {
			if root != project.Root {
				t.Errorf("normalization root = %q, want %q", root, project.Root)
			}
			close(normalizationStarted)
			<-releaseNormalization
			return selection, nil
		})
		updateDone <- err
	}()

	select {
	case <-normalizationStarted:
	case <-time.After(time.Second):
		t.Fatal("workspace normalization did not start")
	}

	// A read using the same service mutex must finish while normalization is
	// deliberately stopped. This regresses the former write-lock convoy.
	readDone := make(chan error, 1)
	go func() {
		_, err := s.GetProject(project.ID)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err != nil {
			close(releaseNormalization)
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(releaseNormalization)
		<-updateDone
		t.Fatal("unrelated read blocked behind workspace normalization")
	}

	// The first valid submit is the permanent workspace lock. It must be able
	// to win while the older patch is still validating outside the mutex.
	submitDone := make(chan error, 1)
	go func() {
		_, err := s.Submit(a.ID, "first message", "")
		submitDone <- err
	}()
	select {
	case err := <-submitDone:
		if err != nil {
			close(releaseNormalization)
			<-updateDone
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(releaseNormalization)
		<-updateDone
		t.Fatal("first submit blocked behind workspace normalization")
	}

	close(releaseNormalization)
	select {
	case err := <-updateDone:
		requireProblem(t, err, 409, "workspace_locked")
	case <-time.After(time.Second):
		t.Fatal("workspace update did not finish")
	}

	got, err := s.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Workspace.Locked || got.Workspace.Mode != "current_checkout" {
		t.Fatalf("late workspace update overwrote first-submit lock: %+v", got.Workspace)
	}
}
