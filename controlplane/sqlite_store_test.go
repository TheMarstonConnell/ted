package controlplane

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
)

func replaceSQLiteState(t testing.TB, store *sqliteStore, state diskState) {
	t.Helper()
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`PRAGMA defer_foreign_keys = ON`); err == nil {
		_, err = tx.Exec(`DELETE FROM projects`)
	}
	if err == nil {
		err = importSQLiteState(tx, state)
	}
	if err == nil {
		err = checkForeignKeys(tx)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		t.Fatal(err)
	}
}

func backendState() diskState {
	created := time.Date(2025, time.January, 2, 3, 4, 5, 600, time.FixedZone("", -7*60*60))
	state := emptyState()
	state.Projects["p"] = Project{ID: "p", Name: "project", Root: "/tmp/project"}
	for i, id := range []string{"a1", "a2"} {
		at := created.Add(time.Duration(i) * time.Second)
		state.Agents[id] = &storedAgent{
			Agent: Agent{
				ID:        id,
				ProjectID: "p",
				Title:     id,
				Queue: []QueuedMessage{{
					ID: id + "-message", Text: id, Status: "pending", CreatedAt: at,
				}},
				Messages:  []agent.Message{{Role: "user", Content: agent.TextContent(id)}},
				CreatedAt: at,
				UpdatedAt: at,
			},
			Events: []Event{},
		}
	}
	return state
}

func TestSQLiteLegacyMigrationBackupRestartAndTimestamp(t *testing.T) {
	for _, version := range []int{legacyStoreVersion, storeVersion} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			dir := t.TempDir()
			created := time.Date(2024, time.March, 4, 5, 6, 7, 890123456, time.FixedZone("", 5*60*60+30*60))
			state := emptyState()
			state.Version = version
			state.Projects["p"] = Project{ID: "p", Name: "legacy", Root: "/tmp/legacy"}
			state.Agents["a"] = &storedAgent{
				Agent: Agent{
					ID: "a", ProjectID: "p", Title: "legacy agent",
					Queue:     []QueuedMessage{},
					CreatedAt: created, UpdatedAt: created,
					Cursor: 1,
				},
				Events: []Event{{AgentID: "a", Cursor: 1, Type: "output", Data: json.RawMessage(`{"ResponseType":"agent"}`), CreatedAt: created}},
			}
			legacy, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(dir, "state.json"), legacy, 0644); err != nil {
				t.Fatal(err)
			}
			store, migrated, err := openSQLiteStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			got := migrated.Agents["a"]
			if got.Agent.CreatedAt.Format(time.RFC3339Nano) != created.Format(time.RFC3339Nano) || got.Events[0].CreatedAt.Format(time.RFC3339Nano) != created.Format(time.RFC3339Nano) {
				t.Fatalf("timestamp representation changed: agent=%s event=%s", got.Agent.CreatedAt.Format(time.RFC3339Nano), got.Events[0].CreatedAt.Format(time.RFC3339Nano))
			}
			if version == legacyStoreVersion && (got.Agent.LastResponseCursor != 1 || got.Agent.ReadCursor != 1) {
				t.Fatalf("v1 read cursors not migrated: %+v", got.Agent)
			}
			backup, err := os.ReadFile(filepath.Join(dir, sqliteBackupName))
			if err != nil || !reflect.DeepEqual(backup, legacy) {
				t.Fatalf("legacy backup changed: %v", err)
			}
			assertPrivateSQLiteFiles(t, dir)
			if err = store.close(); err != nil {
				t.Fatal(err)
			}
			store, restarted, err := openSQLiteStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.close()
			if restarted.Agents["a"].Events[0].CreatedAt.Format(time.RFC3339Nano) != created.Format(time.RFC3339Nano) {
				t.Fatal("event offset changed after restart")
			}
		})
	}
}

func TestSQLiteInitializedPendingPromotionLeavesBackupUntouched(t *testing.T) {
	dir := t.TempDir()
	legacyState := emptyState()
	legacyState.Projects["p"] = Project{ID: "p", Root: "/tmp/pending-promotion"}
	legacy, err := json.Marshal(legacyState)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "state.json"), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	store, _, err := openSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.close(); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(dir, sqliteBackupName)
	backupBefore, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	backupInfo, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = writeGuard(dir, "pending", true); err != nil {
		t.Fatal(err)
	}
	store, state, err := openSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Projects["p"].Root != "/tmp/pending-promotion" {
		t.Fatal("initialized database did not win pending recovery")
	}
	if err = store.close(); err != nil {
		t.Fatal(err)
	}
	backupAfter, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	backupInfoAfter, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backupBefore, backupAfter) || !backupInfo.ModTime().Equal(backupInfoAfter.ModTime()) {
		t.Fatal("pending-guard promotion rewrote the legacy backup")
	}
	guardPath := filepath.Join(dir, "state.json")
	guardInfo, err := os.Stat(guardPath)
	if err != nil {
		t.Fatal(err)
	}
	store, _, err = openSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.close(); err != nil {
		t.Fatal(err)
	}
	guardInfoAfter, err := os.Stat(guardPath)
	if err != nil {
		t.Fatal(err)
	}
	if !guardInfo.ModTime().Equal(guardInfoAfter.ModTime()) {
		t.Fatal("normal active startup rewrote the guard")
	}
}

func TestSQLitePendingEmptySeedRecoveryAndActiveGuardFailure(t *testing.T) {
	t.Run("pending empty", func(t *testing.T) {
		dir := t.TempDir()
		if err := writeGuard(dir, "pending", false); err != nil {
			t.Fatal(err)
		}
		store, state, err := openSQLiteStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer store.close()
		if len(state.Projects) != 0 || len(state.Agents) != 0 {
			t.Fatalf("unexpected recovered state: %+v", state)
		}
		if _, err = os.Stat(filepath.Join(dir, sqliteBackupName)); !os.IsNotExist(err) {
			t.Fatalf("empty seed created backup: %v", err)
		}
	})

	t.Run("active uninitialized header", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, sqliteFilename)
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`CREATE TABLE scratch(value); DROP TABLE scratch`); err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
		if err = writeGuard(dir, "active", false); err != nil {
			t.Fatal(err)
		}
		if store, _, err := openSQLiteStore(dir); err == nil {
			store.close()
			t.Fatal("active guard accepted an uninitialized database")
		}
	})
}

func TestSQLiteTouchedRowsOnlyAndAtomicRollback(t *testing.T) {
	dir := t.TempDir()
	store, _, err := openSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	state := backendState()
	replaceSQLiteState(t, store, state)

	if _, err = store.db.Exec(`CREATE TRIGGER reject_unrelated BEFORE UPDATE ON agents WHEN OLD.id = 'a2' BEGIN SELECT RAISE(ABORT, 'unrelated row touched'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`CREATE TRIGGER reject_queue_update BEFORE UPDATE ON queue_messages BEGIN SELECT RAISE(ABORT, 'queue touched'); END`); err != nil {
		t.Fatal(err)
	}
	changes := newStateChanges()
	changes.agent(state, "a1")
	state.Agents["a1"].Agent.Title = "metadata only"
	if err = store.apply(state, changes); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Agents["a1"].Agent.Title != "metadata only" || persisted.Agents["a2"].Agent.Title != "a2" {
		t.Fatalf("unexpected metadata update: %+v", persisted.Agents)
	}

	if _, err = store.db.Exec(`CREATE TRIGGER reject_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'event rollback'); END`); err != nil {
		t.Fatal(err)
	}
	beforeSQL := cloneDiskState(persisted)
	changes = newStateChanges()
	changes.agent(state, "a1")
	changes.receipt(state, "new-receipt")
	at := state.Agents["a1"].Agent.UpdatedAt.Add(time.Second)
	state.Agents["a1"].Agent.Queue = append(state.Agents["a1"].Agent.Queue, QueuedMessage{ID: "new-message", Status: "pending", CreatedAt: at})
	state.Agents["a1"].Agent.Cursor = 1
	state.Agents["a1"].Agent.UpdatedAt = at
	state.Agents["a1"].Events = append(state.Agents["a1"].Events, Event{AgentID: "a1", Cursor: 1, Type: "new", Data: json.RawMessage(`{"ok":true}`), CreatedAt: at})
	state.Receipts["new-receipt"] = receipt{Fingerprint: "fingerprint", AgentID: "a1", MessageID: "new-message"}
	if err = store.apply(state, changes); err == nil {
		t.Fatal("trigger did not fail transaction")
	}
	afterSQL, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeSQL, afterSQL) {
		t.Fatal("failed transaction changed durable state")
	}
}

func TestSQLiteRejectsMissingChildTails(t *testing.T) {
	for _, table := range []string{"queue_messages", "transcript_messages"} {
		t.Run(table, func(t *testing.T) {
			dir := t.TempDir()
			store, _, err := openSQLiteStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.close()
			replaceSQLiteState(t, store, backendState())
			if _, err = store.db.Exec(`DELETE FROM ` + table + ` WHERE agent_id = 'a1' AND ordinal = 0`); err != nil {
				t.Fatal(err)
			}
			if _, err = store.readState(); err == nil {
				t.Fatalf("missing final %s row was accepted", table)
			}
		})
	}
}

func TestSQLiteExistingAgentUpdateDoesNotRecreateMissingRow(t *testing.T) {
	dir := t.TempDir()
	store, _, err := openSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	state := backendState()
	replaceSQLiteState(t, store, state)
	changes := newStateChanges()
	changes.agent(state, "a1")
	state.Agents["a1"].Agent.Title = "must not recreate"
	if _, err = store.db.Exec(`DELETE FROM agents WHERE id = 'a1'`); err != nil {
		t.Fatal(err)
	}
	if err = store.apply(state, changes); err == nil {
		t.Fatal("existing-agent update recreated a missing durable row")
	}
	var count int
	if err = store.db.QueryRow(`SELECT count(*) FROM agents WHERE id = 'a1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("missing durable agent was recreated")
	}
}

func TestSQLitePragmasPermissionsForeignKeysAndPlans(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	store, _, err := openSQLiteStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	assertPrivateSQLiteFiles(t, dir)

	var journal string
	var synchronous, foreignKeys int
	if err = store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(journal, "wal") || synchronous != 2 || foreignKeys != 1 {
		t.Fatalf("unsafe pragmas: %s %d %d", journal, synchronous, foreignKeys)
	}

	for _, recent := range []bool{false, true} {
		for _, includeSettled := range []bool{false, true} {
			for _, projectID := range []string{"", "p"} {
				query, _, args := agentSelectionQueries(includeSettled, projectID, recent)
				index := "agents_"
				if projectID != "" {
					index += "project_"
				}
				if !includeSettled {
					index += "active_"
				}
				if recent {
					index += "recent"
				} else {
					index += "created"
				}
				assertQueryPlanUses(t, store.db, query, index, args...)
			}
		}
	}
	assertQueryPlanUses(t, store.db, queuePositionQuery, "sqlite_autoindex_queue_messages_2", "a", "message")
	assertQueryPlanUses(t, store.db, firstPendingQuery, "queue_pending", "a")
	assertQueryPlanUses(t, store.db, projectByRootQuery, "sqlite_autoindex_projects_2", "/tmp/project")
	assertIndexExists(t, store.db, "agents", "agents_parent")
	assertIndexExists(t, store.db, "receipts", "receipts_agent")

	rows, err := store.db.Query(`PRAGMA foreign_key_list(receipts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	compositeParts := 0
	for rows.Next() {
		var id, seq int
		var table, from, to, onUpdate, onDelete, match string
		if err = rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatal(err)
		}
		if table == "queue_messages" {
			compositeParts++
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if compositeParts != 2 {
		t.Fatalf("receipt-to-queue foreign key has %d parts", compositeParts)
	}
}

func TestSQLiteRejectsUnknownHeaderVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, sqliteFilename)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if store, _, err := openSQLiteStore(dir); err == nil {
		store.close()
		t.Fatal("unknown SQLite user_version was initialized")
	}
}

func assertPrivateSQLiteFiles(t testing.TB, dir string) {
	t.Helper()
	for _, name := range []string{sqliteFilename, sqliteFilename + "-wal", sqliteFilename + "-shm", "state.json", sqliteBackupName} {
		info, err := os.Stat(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s mode is %o", name, info.Mode().Perm())
		}
	}
}

func assertQueryPlanUses(t testing.TB, db *sql.DB, query, index string, args ...any) {
	t.Helper()
	rows, err := db.Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, index) {
		t.Fatalf("query plan does not use %s: %v", index, details)
	}
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("query plan sorts through a temporary tree: %v", details)
	}
}

func assertIndexExists(t testing.TB, db *sql.DB, table, index string) {
	t.Helper()
	rows, err := db.Query(`PRAGMA index_list(` + table + `)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		if err = rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		if name == index {
			return
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("missing index %s on %s", index, table)
}

func TestSQLiteFileURL(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{`C:\Users\me\state ?#%.sqlite`, `file:///C:/Users/me/state%20%3F%23%25.sqlite`},
		{`/tmp/state ?#%.sqlite`, `file:///tmp/state%20%3F%23%25.sqlite`},
	}
	for _, test := range tests {
		got, err := sqliteFileURL(test.path)
		if err != nil {
			t.Fatalf("sqliteFileURL(%q): %v", test.path, err)
		}
		if got != test.want {
			t.Fatalf("sqliteFileURL(%q) = %q, want %q", test.path, got, test.want)
		}
	}
	for _, path := range []string{`//server/share/state.sqlite`, `\\server\share\state.sqlite`} {
		if _, err := sqliteFileURL(path); err == nil {
			t.Fatalf("UNC SQLite path %q was accepted", path)
		}
	}
}
