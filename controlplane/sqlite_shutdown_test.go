package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
)

func TestSQLiteCloseTimeoutRetainsDatabaseAndLeaseUntilWorkerExits(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var finish sync.Once
	var calls atomic.Int32
	provider := &httpTestProvider{complete: func(req agent.CompletionRequest) (*agent.Response, error) {
		calls.Add(1)
		close(started)
		<-req.RequestContext().Done()
		<-release
		return nil, req.RequestContext().Err()
	}}
	s, err := NewService(t.TempDir(), nil, []agent.Provider{provider})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		finish.Do(func() { close(release) })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	p, err := s.CreateProject(CreateProjectRequest{Name: "shutdown", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAgent(CreateAgentRequest{ProjectID: p.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Submit(a.ID, "running", "running"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	pending, err := s.Submit(a.ID, "retained", "pending")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = s.Close(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close: %v", err)
	}
	if err = s.store.db.Ping(); err != nil {
		t.Fatalf("live worker lost database: %v", err)
	}
	if other, err := NewService(s.dir, nil, nil); err == nil {
		other.Close(context.Background())
		t.Fatal("timed-out close released ownership while worker active")
	}
	finish.Do(func() { close(release) })
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewService(s.dir, nil, []agent.Provider{provider})
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close(context.Background())
	snapshot, err := recovered.GetAgent(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Held || snapshot.State != "idle" || snapshot.Queue[0].Status != "interrupted" || snapshot.Queue[1].ID != pending.ID || snapshot.Queue[1].Status != "pending" {
		t.Fatalf("bad recovery: %+v", snapshot)
	}
	if calls.Load() != 1 {
		t.Fatal("shutdown advanced or replayed queue")
	}
}

func TestSQLiteIndexedReadsStayCoherentWithCommits(t *testing.T) {
	s, _, p, a := serviceFixture(t)
	var readers sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		readers.Go(func() {
			for range 30 {
				rows, total, err := s.listAgents(true, p.ID, 1, 10)
				if err != nil {
					failures <- err
					return
				}
				if total != 1 || len(rows) != 1 || rows[0].ID != a.ID || rows[0].ReadCursor > rows[0].Cursor {
					failures <- fmt.Errorf("incoherent indexed snapshot: %+v, total=%d", rows, total)
					return
				}
			}
		})
	}
	for range 15 {
		snapshot, err := s.GetAgent(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.ReadAgent(a.ID, snapshot.Cursor); err != nil {
			t.Fatal(err)
		}
	}
	readers.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	persisted := readClosedSQLiteState(t, s.dir)
	if got := persisted.Agents[a.ID].Agent; got.ReadCursor != got.Cursor-1 {
		t.Fatalf("last read acknowledgement not durable: %+v", got)
	}
}
