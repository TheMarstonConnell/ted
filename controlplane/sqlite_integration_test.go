package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
)

// Closed-store fixtures take the same directory lock as a service. Never edit a
// live service's SQL behind its runtime mirror (except explicit fault triggers).
func withClosedSQLiteStore(t testing.TB, dir string, f func(*sqliteStore, diskState)) {
	t.Helper()
	unlock, err := lockStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	store, state, err := openSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.close(); err != nil {
			t.Error(err)
		}
	}()
	f(store, state)
}

func readClosedSQLiteState(t testing.TB, dir string) diskState {
	t.Helper()
	var state diskState
	withClosedSQLiteStore(t, dir, func(_ *sqliteStore, got diskState) { state = got })
	return state
}

func editClosedSQLiteState(t testing.TB, dir string, edit func(diskState)) {
	t.Helper()
	withClosedSQLiteStore(t, dir, func(store *sqliteStore, state diskState) {
		edit(state)
		replaceSQLiteState(t, store, state)
	})
}

func requireDiskState(t testing.TB, want, got diskState) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		a, _ := json.Marshal(want)
		b, _ := json.Marshal(got)
		t.Fatalf("durable state mismatch\nwant %s\n got %s", a, b)
	}
}

func TestSQLiteIdleAndActiveCloseReopen(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprintf("active=%t", active), func(t *testing.T) {
			s, p, _, a := serviceFixture(t)
			if active {
				if _, err := s.Submit(a.ID, "interrupted work", "running-key"); err != nil {
					t.Fatal(err)
				}
				awaitCall(t, p)
				if _, err := s.Submit(a.ID, "pending work", "pending-key"); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(context.Background()); err != nil {
				t.Fatal("second close:", err)
			}
			// This is an independent SQL connection, not the closed Service mirror.
			disk := readClosedSQLiteState(t, s.dir)
			record := disk.Agents[a.ID]
			if record.Agent.State != "idle" {
				t.Fatalf("close left state %q", record.Agent.State)
			}
			if active && (!record.Agent.Held || record.Agent.Queue[0].Status != "interrupted" || record.Agent.Queue[1].Status != "pending") {
				t.Fatalf("close did not persist interrupted/pending queue: %+v", record.Agent)
			}
			reopened, err := NewService(s.dir, nil, []agent.Provider{p})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close(context.Background())
			noCall(t, p)
			got, err := reopened.store.readState()
			if err != nil {
				t.Fatal(err)
			}
			requireDiskState(t, disk, got)
			if active {
				retry, err := reopened.Submit(a.ID, "pending work", "pending-key")
				if err != nil || retry.ID != record.Agent.Queue[1].ID {
					t.Fatalf("durable receipt: %+v %v", retry, err)
				}
				noCall(t, p)
			}
		})
	}
}

// Compare independent SQL indexes with the fully reconstructed disk records,
// both after incremental mutations and after every connection has been closed.
func checkSQLiteIndexes(t *testing.T, store *sqliteStore) {
	t.Helper()
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	projects := []string{"", "missing-project"}
	for id, project := range state.Projects {
		projects = append(projects, id)
		got, found, err := store.projectByRoot(project.Root)
		if err != nil || !found || got != id {
			t.Fatalf("root index %q: %q %t %v", project.Root, got, found, err)
		}
	}
	if _, found, err := store.projectByRoot("/does-not-exist-in-this-fixture"); err != nil || found {
		t.Fatalf("unknown root: %t %v", found, err)
	}
	for id, record := range state.Agents {
		pending := -1
		for i, message := range record.Agent.Queue {
			got, found, err := store.queuePosition(id, message.ID)
			if err != nil || !found || got != i {
				t.Fatalf("queue index %s/%s: %d %t %v, want %d", id, message.ID, got, found, err, i)
			}
			if pending < 0 && message.Status == "pending" {
				pending = i
			}
		}
		got, found, err := store.firstPending(id)
		if err != nil || found != (pending >= 0) || (found && got != pending) {
			t.Fatalf("pending index %s: %d %t %v, want %d", id, got, found, err, pending)
		}
		if _, found, err := store.queuePosition(id, "missing-message"); err != nil || found {
			t.Fatalf("unknown message: %t %v", found, err)
		}
	}
	for _, includeSettled := range []bool{false, true} {
		for _, projectID := range projects {
			for _, recent := range []bool{false, true} {
				want := []string{}
				for id, record := range state.Agents {
					if (!includeSettled && record.Agent.Settled) || (projectID != "" && record.Agent.ProjectID != projectID) {
						continue
					}
					want = append(want, id)
				}
				sort.Slice(want, func(i, j int) bool {
					a, b := state.Agents[want[i]].Agent, state.Agents[want[j]].Agent
					if recent {
						if !a.UpdatedAt.Equal(b.UpdatedAt) {
							return a.UpdatedAt.After(b.UpdatedAt)
						}
					} else if !a.CreatedAt.Equal(b.CreatedAt) {
						return a.CreatedAt.Before(b.CreatedAt)
					}
					return a.ID < b.ID
				})
				for _, offset := range []int64{0, 1, 100} {
					limit := int64(-1)
					expected := want
					if recent {
						limit = 2
						start := min(int(offset), len(want))
						expected = want[start:min(start+int(limit), len(want))]
					}
					ids, total, err := store.agentIDs(includeSettled, projectID, recent, offset, limit)
					if err != nil || total != len(want) || len(ids) != len(expected) {
						t.Fatalf("agent index settled=%t project=%q recent=%t offset=%d: %v total=%d err=%v, want %v total=%d", includeSettled, projectID, recent, offset, ids, total, err, expected, len(want))
					}
					for i := range ids {
						if ids[i] != expected[i] {
							t.Fatalf("index order %v, want %v", ids, expected)
						}
					}
				}
			}
		}
	}
}

func TestSQLiteIndexLookupConsistencyAfterMutationAndReopen(t *testing.T) {
	s, p, project, a := serviceFixture(t)
	other, err := s.CreateProject(CreateProjectRequest{Name: "other", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		projectID := project.ID
		if i%2 == 0 {
			projectID = other.ID
		}
		created, err := s.CreateAgent(CreateAgentRequest{ProjectID: projectID}, fmt.Sprint("create-", i))
		if err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			if _, err := s.SetSettled(created.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.Submit(a.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	awaitCall(t, p)
	deleted, err := s.Submit(a.ID, "cancel this", "cancel-key")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := s.Submit(a.ID, "keep pending", "pending-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePending(a.ID, deleted.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePending(a.ID, deleted.ID); err != nil {
		t.Fatal("idempotent deletion:", err)
	}
	if _, err := s.SetSettled(a.ID, true); err != nil {
		t.Fatal(err)
	}
	awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" })
	same, err := s.GetAgent(a.ID)
	if err != nil || same.Queue[1].Status != "cancelled" || same.Queue[2].ID != pending.ID || same.Queue[2].Status != "pending" {
		t.Fatalf("queue changes: %+v %v", same, err)
	}
	checkSQLiteIndexes(t, s.store)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	withClosedSQLiteStore(t, s.dir, func(store *sqliteStore, _ diskState) { checkSQLiteIndexes(t, store) })
	// Exact ordering must not depend on textual RFC3339 ordering, UnixNano's
	// limited range, or nondeterministic map iteration for timestamp ties.
	editClosedSQLiteState(t, s.dir, func(state diskState) {
		ids := []string{}
		for id := range state.Agents {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		stamps := []time.Time{
			time.Date(1600, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2400, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2025, 1, 1, 0, 0, 0, 100000000, time.UTC),
		}
		for i, id := range ids {
			state.Agents[id].Agent.CreatedAt = stamps[i%len(stamps)]
			state.Agents[id].Agent.UpdatedAt = stamps[i%len(stamps)]
		}
	})
	withClosedSQLiteStore(t, s.dir, func(store *sqliteStore, _ diskState) { checkSQLiteIndexes(t, store) })
}

// The child deliberately bypasses all defers, Service.Close and sql.DB.Close.
// Only this test's explicitly supplied temporary directory can be used.
func TestSQLiteAbruptExitWALRecovery(t *testing.T) {
	if os.Getenv("TED_SQLITE_CRASH_CHILD") == "1" {
		dir, root := os.Getenv("TED_SQLITE_CRASH_DIR"), os.Getenv("TED_SQLITE_CRASH_ROOT")
		if dir == "" || root == "" {
			t.Fatal("missing isolated child paths")
		}
		p := &controlledProvider{calls: make(chan agent.CompletionRequest, 20), results: make(chan error, 20)}
		s, err := NewService(dir, nil, []agent.Provider{p})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.store.db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
			t.Fatal(err)
		}
		project, err := s.CreateProject(CreateProjectRequest{Name: "crash", Root: root})
		if err != nil {
			t.Fatal(err)
		}
		a, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID}, "create-key")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Submit(a.ID, "never execute twice", "running-key"); err != nil {
			t.Fatal(err)
		}
		awaitCall(t, p)
		if _, err = s.Submit(a.ID, "durable pending message", "pending-key"); err != nil {
			t.Fatal(err)
		}
		state, err := s.store.readState()
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "acknowledged.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		os.Exit(73)
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("directory lock does not automatically release on process exit on this platform")
	}
	dir, root := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSQLiteAbruptExitWALRecovery$", "-test.count=1")
	cmd.Env = append(os.Environ(), "TED_SQLITE_CRASH_CHILD=1", "TED_SQLITE_CRASH_DIR="+dir, "TED_SQLITE_CRASH_ROOT="+root)
	output, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 73 {
		t.Fatalf("child did not reach abrupt exit: %v\n%s", err, output)
	}
	info, err := os.Stat(filepath.Join(dir, "state.sqlite-wal"))
	if err != nil || info.Size() <= 32 {
		t.Fatalf("child did not leave populated WAL: %v %v", info, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "acknowledged.json"))
	if err != nil {
		t.Fatal(err)
	}
	var acknowledged diskState
	if err = json.Unmarshal(data, &acknowledged); err != nil {
		t.Fatal(err)
	}
	// Inspect prior to NewService's interrupted-turn repair. Every acknowledged
	// event, transcript, queue entry and receipt must survive the unclean exit.
	requireDiskState(t, acknowledged, readClosedSQLiteState(t, dir))
	p := &controlledProvider{calls: make(chan agent.CompletionRequest, 20), results: make(chan error, 20)}
	s, err := NewService(dir, nil, []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	var id string
	for id = range acknowledged.Agents {
		break
	}
	got, err := s.GetAgent(id)
	if err != nil || !got.Held || got.State != "idle" || len(got.Queue) != 2 || got.Queue[0].Status != "interrupted" || got.Queue[1].Status != "pending" {
		t.Fatalf("crash repair: %+v %v", got, err)
	}
	noCall(t, p)
	retry, err := s.Submit(id, "durable pending message", "pending-key")
	if err != nil || retry.ID != got.Queue[1].ID {
		t.Fatalf("WAL receipt lost: %+v %v", retry, err)
	}
	noCall(t, p)
	repaired, err := s.store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	requireDiskState(t, repaired, readClosedSQLiteState(t, dir))
}

func TestSQLiteSubmitTransactionFailureRollsBackQueueEventsAndReceipt(t *testing.T) {
	// AFTER triggers guarantee the failing row has already been written inside
	// the transaction; events and receipts also fail after earlier table writes.
	for _, table := range []string{"queue_messages", "events", "receipts"} {
		t.Run(table, func(t *testing.T) {
			s, p, project, a := serviceFixture(t)
			if _, err := s.CreateAgent(CreateAgentRequest{ProjectID: project.ID, Title: "unrelated"}, "unrelated-receipt"); err != nil {
				t.Fatal(err)
			}
			before, err := s.store.readState()
			if err != nil {
				t.Fatal(err)
			}
			predicate := ""
			if table == "events" {
				predicate = " WHEN NEW.type = 'agent.updated'"
			}
			trigger := "CREATE TRIGGER reject_submit AFTER INSERT ON " + table + predicate + " BEGIN SELECT RAISE(ABORT, 'injected durable submit failure'); END"
			if _, err = s.store.db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			queued, err := s.Submit(a.ID, "must not be acknowledged or run", "failed-receipt")
			assertStatus(t, err, 503)
			if queued.ID != "" {
				t.Fatalf("failure returned acknowledged queue entry: %+v", queued)
			}
			noCall(t, p)
			func() {
				s.mu.RLock()
				defer s.mu.RUnlock()
				requireDiskState(t, before, s.state)
			}()
			after, err := s.store.readState()
			if err != nil {
				t.Fatal(err)
			}
			requireDiskState(t, before, after)
			// Remove only the test trigger, not any application rows. Failure is still
			// sticky: a formerly failing service must not resume accepting writes.
			if _, err = s.store.db.Exec("DROP TRIGGER reject_submit"); err != nil {
				t.Fatal(err)
			}
			_, err = s.Submit(a.ID, "still rejected", "failed-receipt")
			assertStatus(t, err, 503)
			if err = s.Close(context.Background()); err == nil {
				t.Fatal("close hid storage failure")
			}
			requireDiskState(t, before, readClosedSQLiteState(t, s.dir))
			reopened, err := NewService(s.dir, nil, []agent.Provider{p})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close(context.Background())
			noCall(t, p)
			accepted, err := reopened.Submit(a.ID, "a different payload with the same uncommitted key", "failed-receipt")
			if err != nil || accepted.ID == "" {
				t.Fatalf("failed transaction left a receipt: %+v %v", accepted, err)
			}
			awaitCall(t, p)
			p.results <- nil
			awaitAgent(t, reopened, a.ID, func(a Agent) bool { return a.State == "idle" })
		})
	}
}

func TestSQLiteQueueEditFailureRestoresAliasedEntryOnDisk(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	if _, err := s.Submit(a.ID, "active", ""); err != nil {
		t.Fatal(err)
	}
	awaitCall(t, p)
	pending, err := s.Submit(a.ID, "pending", "retained-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetSettled(a.ID, true); err != nil {
		t.Fatal(err)
	}
	awaitAgent(t, s, a.ID, func(a Agent) bool { return a.State == "idle" })
	before, err := s.store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.db.Exec("CREATE TRIGGER reject_cancel AFTER INSERT ON events WHEN NEW.type = 'message.cancelled' BEGIN SELECT RAISE(ABORT, 'cancel failure'); END"); err != nil {
		t.Fatal(err)
	}
	assertStatus(t, s.DeletePending(a.ID, pending.ID), 503)
	got, err := s.GetAgent(a.ID)
	if err != nil || got.Queue[1].Status != "pending" {
		t.Fatalf("aliased queue edit not rolled back: %+v %v", got, err)
	}
	if err = s.Close(context.Background()); err == nil {
		t.Fatal("close hid storage failure")
	}
	requireDiskState(t, before, readClosedSQLiteState(t, s.dir))
	noCall(t, p)
}

func TestSQLiteTransactionFailureCancelsActiveWorkerAndRecoversReservation(t *testing.T) {
	s, p, _, a := serviceFixture(t)
	running, err := s.Submit(a.ID, "already acknowledged", "running-key")
	if err != nil {
		t.Fatal(err)
	}
	call := awaitCall(t, p)
	before, err := s.store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.store.db.Exec("CREATE TRIGGER reject_receipt AFTER INSERT ON receipts BEGIN SELECT RAISE(ABORT, 'receipt failure'); END"); err != nil {
		t.Fatal(err)
	}
	_, err = s.Submit(a.ID, "unacknowledged next turn", "failed-key")
	assertStatus(t, err, 503)
	select {
	case <-call.Context.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("SQL transaction failure did not cancel active provider")
	}
	if err = s.Close(context.Background()); err == nil {
		t.Fatal("close hid storage failure")
	}
	// The failed worker completion cannot overwrite the last acknowledged
	// reservation. Startup, not an uncommitted worker, must repair it.
	requireDiskState(t, before, readClosedSQLiteState(t, s.dir))
	reopened, err := NewService(s.dir, nil, []agent.Provider{p})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	noCall(t, p)
	got, err := reopened.GetAgent(a.ID)
	if err != nil || len(got.Queue) != 1 || got.Queue[0].ID != running.ID || got.Queue[0].Status != "interrupted" || !got.Held {
		t.Fatalf("reservation recovery: %+v %v", got, err)
	}
	events, err := reopened.Events(a.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	original := before.Agents[a.ID].Events
	if len(events) <= len(original) || !reflect.DeepEqual(events[:len(original)], original) {
		t.Fatal("failure changed acknowledged event history")
	}
	if err = reopened.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	durable := readClosedSQLiteState(t, s.dir)
	if durable.Agents[a.ID].Agent.Queue[0].Status != "interrupted" || len(durable.Receipts) != len(before.Receipts) {
		t.Fatal("repaired reservation or receipt rollback not durable")
	}
}
