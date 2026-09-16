package controlplane

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TheMarstonConnell/ted/agent"
	_ "modernc.org/sqlite"
)

const (
	sqliteSchemaVersion = 1
	sqliteStateVersion  = 3
	sqliteFilename      = "state.sqlite"
	sqliteBackupName    = "state.json.pre-sqlite"
)

const sqliteSchema = `
CREATE TABLE store_metadata (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    storage TEXT NOT NULL CHECK (storage = 'sqlite'),
    schema_version INTEGER NOT NULL,
    initialized INTEGER NOT NULL CHECK (initialized = 1)
);
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    root TEXT NOT NULL UNIQUE,
    data BLOB NOT NULL
);
CREATE TABLE agents (
    id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    parent_agent_id TEXT REFERENCES agents(id) DEFERRABLE INITIALLY DEFERRED,
    settled INTEGER NOT NULL CHECK (settled IN (0, 1)),
    created_sec INTEGER NOT NULL,
    created_nsec INTEGER NOT NULL CHECK (created_nsec BETWEEN 0 AND 999999999),
    updated_sec INTEGER NOT NULL,
    updated_nsec INTEGER NOT NULL CHECK (updated_nsec BETWEEN 0 AND 999999999),
    manifest_offset INTEGER NOT NULL,
    queue_count INTEGER NOT NULL CHECK (queue_count >= 0),
    transcript_count INTEGER NOT NULL CHECK (transcript_count >= 0),
    queue_nil INTEGER NOT NULL CHECK (queue_nil IN (0, 1)),
    transcript_nil INTEGER NOT NULL CHECK (transcript_nil IN (0, 1)),
    events_nil INTEGER NOT NULL CHECK (events_nil IN (0, 1)),
    data BLOB NOT NULL,
    CHECK (queue_nil = 0 OR queue_count = 0),
    CHECK (transcript_nil = 0 OR transcript_count = 0)
);
CREATE TABLE queue_messages (
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    message_id TEXT NOT NULL,
    status TEXT NOT NULL,
    data BLOB NOT NULL,
    PRIMARY KEY (agent_id, ordinal),
    UNIQUE (agent_id, message_id)
);
CREATE TABLE transcript_messages (
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    data BLOB NOT NULL,
    PRIMARY KEY (agent_id, ordinal)
);
CREATE TABLE events (
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    cursor INTEGER NOT NULL CHECK (cursor > 0),
    type TEXT NOT NULL,
    data BLOB NOT NULL,
    created_sec INTEGER NOT NULL,
    created_nsec INTEGER NOT NULL CHECK (created_nsec BETWEEN 0 AND 999999999),
    created_at TEXT NOT NULL,
    PRIMARY KEY (agent_id, cursor)
);
CREATE TABLE receipts (
    scope TEXT PRIMARY KEY,
    fingerprint TEXT NOT NULL,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
    message_id TEXT,
    FOREIGN KEY (agent_id, message_id) REFERENCES queue_messages(agent_id, message_id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX agents_parent ON agents(parent_agent_id);
CREATE INDEX agents_created ON agents(created_sec, created_nsec, id);
CREATE INDEX agents_active_created ON agents(settled, created_sec, created_nsec, id);
CREATE INDEX agents_project_created ON agents(project_id, created_sec, created_nsec, id);
CREATE INDEX agents_project_active_created ON agents(project_id, settled, created_sec, created_nsec, id);
CREATE INDEX agents_recent ON agents(updated_sec DESC, updated_nsec DESC, id);
CREATE INDEX agents_active_recent ON agents(settled, updated_sec DESC, updated_nsec DESC, id);
CREATE INDEX agents_project_recent ON agents(project_id, updated_sec DESC, updated_nsec DESC, id);
CREATE INDEX agents_project_active_recent ON agents(project_id, settled, updated_sec DESC, updated_nsec DESC, id);
CREATE INDEX queue_pending ON queue_messages(agent_id, ordinal) WHERE status = 'pending';
CREATE INDEX receipts_agent ON receipts(agent_id);
`

type sqliteStore struct {
	db        *sql.DB
	dir       string
	closeOnce sync.Once
	closeErr  error
}

type sqliteGuard struct {
	Version   int    `json:"version"`
	Storage   string `json:"storage"`
	Phase     string `json:"phase"`
	HasLegacy *bool  `json:"has_legacy,omitempty"`
}

func openSQLiteStore(dir string) (_ *sqliteStore, _ diskState, retErr error) {
	path := filepath.Join(dir, sqliteFilename)
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := rejectSymlink(candidate); err != nil {
			return nil, diskState{}, err
		}
	}

	dbExisted, dbEmpty, err := databaseFileStatus(path)
	if err != nil {
		return nil, diskState{}, err
	}
	guard, stateJSON, err := readGuard(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, diskState{}, err
	}
	if guard != nil && guard.Phase == "active" && (!dbExisted || dbEmpty) {
		return nil, diskState{}, errors.New("SQLite control-plane state is missing after activation")
	}
	fileURL, err := sqliteFileURL(path)
	if err != nil {
		return nil, diskState{}, err
	}
	// A crash while SQLite first creates its header must retain the empty seed.
	if guard == nil && len(stateJSON) == 0 && (!dbExisted || dbEmpty) {
		if err = writeGuard(dir, "pending", false); err != nil {
			return nil, diskState{}, err
		}
		empty := false
		guard = &sqliteGuard{Version: sqliteStateVersion, Storage: "sqlite", Phase: "pending", HasLegacy: &empty}
	}
	if err = prepareSQLiteMain(path, dbExisted); err != nil {
		return nil, diskState{}, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = makeExistingFilePrivate(path + suffix); err != nil {
			return nil, diskState{}, err
		}
	}

	dsn := fileURL + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)&_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, diskState{}, err
	}
	s := &sqliteStore{db: db, dir: dir}
	defer func() {
		if retErr != nil {
			_ = s.close()
		}
	}()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err = db.Ping(); err != nil {
		return nil, diskState{}, fmt.Errorf("open SQLite control-plane state: %w", err)
	}
	if err = s.checkPragmas(); err != nil {
		return nil, diskState{}, err
	}
	if err = s.privateFiles(); err != nil {
		return nil, diskState{}, err
	}

	initialized, objects, err := inspectSQLite(db)
	if err != nil {
		return nil, diskState{}, err
	}
	if initialized {
		state, err := s.readState()
		if err != nil {
			return nil, diskState{}, err
		}
		if guard == nil && len(stateJSON) > 0 {
			return nil, diskState{}, errors.New("initialized SQLite state conflicts with an ordinary state.json")
		}
		if guard == nil || guard.Phase != "active" {
			hasLegacy := false
			if guard != nil {
				hasLegacy, err = guardLegacySeed(dir, guard)
				if err != nil {
					return nil, diskState{}, err
				}
			}
			if err = writeGuard(dir, "active", hasLegacy); err != nil {
				return nil, diskState{}, err
			}
		}
		return s, state, nil
	}
	if guard != nil && guard.Phase == "active" {
		return nil, diskState{}, errors.New("activated SQLite control-plane state is uninitialized")
	}
	if objects {
		return nil, diskState{}, errors.New("SQLite control-plane state has no valid schema authority marker")
	}
	if dbExisted && !dbEmpty && guard == nil && len(stateJSON) == 0 {
		return nil, diskState{}, errors.New("uninitialized SQLite control-plane state cannot be recovered")
	}

	legacyBytes := stateJSON
	hasLegacy := len(legacyBytes) > 0
	if guard != nil {
		if guard.Phase != "pending" {
			return nil, diskState{}, fmt.Errorf("invalid SQLite migration phase %q", guard.Phase)
		}
		hasLegacy, err = guardLegacySeed(dir, guard)
		if err != nil {
			return nil, diskState{}, err
		}
		legacyBytes = nil
		if hasLegacy {
			legacyBytes, err = readRegularFile(filepath.Join(dir, sqliteBackupName))
			if err != nil {
				return nil, diskState{}, fmt.Errorf("read pending SQLite migration backup: %w", err)
			}
		}
	}
	legacy := emptyState()
	if len(legacyBytes) != 0 {
		legacy, err = decodeLegacyState(legacyBytes)
		if err != nil {
			return nil, diskState{}, err
		}
		if err = validateParentDAG(legacy.Agents); err != nil {
			return nil, diskState{}, err
		}
	}
	if err = s.initialize(legacy, func() error {
		if guard == nil && len(legacyBytes) > 0 {
			if err := preserveBackup(dir, legacyBytes); err != nil {
				return err
			}
		}
		return writeGuard(dir, "pending", hasLegacy)
	}); err != nil {
		return nil, diskState{}, err
	}
	if err = writeGuard(dir, "active", hasLegacy); err != nil {
		return nil, diskState{}, err
	}
	if err = s.privateFiles(); err != nil {
		return nil, diskState{}, err
	}
	state, err := s.readState()
	if err != nil {
		return nil, diskState{}, err
	}
	return s, state, nil
}

func sqliteFileURL(path string) (string, error) {
	if strings.HasPrefix(path, `\\`) {
		return "", errors.New("SQLite state on UNC paths is unsupported")
	}
	normalized := filepath.ToSlash(path)
	if len(normalized) >= 2 && normalized[1] == ':' {
		normalized = strings.ReplaceAll(normalized, `\`, "/")
		if len(normalized) < 3 || normalized[2] != '/' {
			return "", errors.New("SQLite state path must be absolute")
		}
		normalized = "/" + normalized
	}
	if strings.HasPrefix(normalized, "//") {
		return "", errors.New("SQLite state on UNC paths is unsupported")
	}
	return (&url.URL{Scheme: "file", Path: normalized}).String(), nil
}

func prepareSQLiteMain(path string, existed bool) error {
	if existed {
		return os.Chmod(path, 0600)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = f.Chmod(0600); err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	return err
}

func databaseFileStatus(path string) (exists, empty bool, err error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	if !info.Mode().IsRegular() {
		return false, false, fmt.Errorf("SQLite state %q is not a regular file", path)
	}
	return true, info.Size() == 0, nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing SQLite state symlink %q", path)
	}
	return nil
}

func makeExistingFilePrivate(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("SQLite state %q is not a regular file", path)
	}
	if info.Mode().Perm() == 0600 {
		return nil
	}
	return os.Chmod(path, 0600)
}

func readGuard(path string) (*sqliteGuard, []byte, error) {
	if err := rejectSymlink(path); err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var guard sqliteGuard
	if json.Unmarshal(b, &guard) == nil && guard.Storage == "sqlite" {
		if guard.Version != sqliteStateVersion || (guard.Phase != "pending" && guard.Phase != "active") {
			return nil, nil, errors.New("unsupported or incomplete SQLite storage guard")
		}
		return &guard, b, nil
	}
	return nil, b, nil
}

func readRegularFile(path string) ([]byte, error) {
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%q is not a regular file", path)
	}
	return os.ReadFile(path)
}

func preserveBackup(dir string, original []byte) error {
	path := filepath.Join(dir, sqliteBackupName)
	if err := rejectSymlink(path); err != nil {
		return err
	}
	if existing, err := os.ReadFile(path); err == nil {
		if !bytes.Equal(existing, original) {
			return errors.New("existing pre-SQLite backup does not match legacy state")
		}
		if err = os.Chmod(path, 0600); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = f.Sync()
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = d.Sync()
		closeErr = d.Close()
		if err == nil {
			err = closeErr
		}
		return err
	} else if !os.IsNotExist(err) {
		return err
	}
	return atomicWritePrivate(dir, sqliteBackupName, original)
}

func guardLegacySeed(dir string, guard *sqliteGuard) (bool, error) {
	if guard.HasLegacy != nil {
		return *guard.HasLegacy, nil
	}
	path := filepath.Join(dir, sqliteBackupName)
	if err := rejectSymlink(path); err != nil {
		return false, err
	}
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func writeGuard(dir, phase string, hasLegacy bool) error {
	b, err := json.Marshal(sqliteGuard{Version: sqliteStateVersion, Storage: "sqlite", Phase: phase, HasLegacy: &hasLegacy})
	if err != nil {
		return err
	}
	return atomicWritePrivate(dir, "state.json", b)
}

func atomicWritePrivate(dir, name string, content []byte) error {
	f, err := os.CreateTemp(dir, ".sqlite-state-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(content)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func inspectSQLite(db *sql.DB) (initialized, objects bool, err error) {
	var check string
	if err = db.QueryRow(`PRAGMA quick_check`).Scan(&check); err != nil {
		return false, false, fmt.Errorf("check SQLite control-plane state: %w", err)
	}
	if check != "ok" {
		return false, false, fmt.Errorf("corrupt SQLite control-plane state: %s", check)
	}
	var userVersion int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
		return false, false, err
	}
	if userVersion != 0 && userVersion != sqliteSchemaVersion {
		return false, false, fmt.Errorf("unsupported SQLite user schema version %d", userVersion)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return false, false, err
	}
	objects = count != 0 || userVersion != 0
	var storage string
	var version, complete int
	err = db.QueryRow(`SELECT storage, schema_version, initialized FROM store_metadata WHERE id = 1`).Scan(&storage, &version, &complete)
	if errors.Is(err, sql.ErrNoRows) || strings.Contains(sqliteErrorMessage(err), "no such table") {
		return false, objects, nil
	}
	if err != nil {
		return false, objects, fmt.Errorf("read SQLite schema authority marker: %w", err)
	}
	if storage != "sqlite" || complete != 1 || version != sqliteSchemaVersion {
		return false, objects, fmt.Errorf("unsupported SQLite control-plane schema version %d", version)
	}
	if userVersion != sqliteSchemaVersion {
		return false, objects, fmt.Errorf("SQLite schema version markers disagree (%d and %d)", version, userVersion)
	}
	return true, objects, nil
}

func sqliteErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *sqliteStore) checkPragmas() error {
	var journal string
	var synchronous, foreignKeys, busyTimeout int
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		return err
	}
	if err := s.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		return err
	}
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return err
	}
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		return err
	}
	if !strings.EqualFold(journal, "wal") || synchronous != 2 || foreignKeys != 1 || busyTimeout < 1 {
		return fmt.Errorf("unsafe SQLite pragmas: journal=%s synchronous=%d foreign_keys=%d busy_timeout=%d", journal, synchronous, foreignKeys, busyTimeout)
	}
	return nil
}

func (s *sqliteStore) privateFiles() error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := filepath.Join(s.dir, sqliteFilename) + suffix
		if err := makeExistingFilePrivate(path); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqliteStore) initialize(state diskState, beforeCommit func() error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(sqliteSchema); err != nil {
		return fmt.Errorf("create SQLite control-plane schema: %w", err)
	}
	if _, err = tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, sqliteSchemaVersion)); err != nil {
		return err
	}
	if err = importSQLiteState(tx, state); err != nil {
		return err
	}
	if err = checkForeignKeys(tx); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO store_metadata(id, storage, schema_version, initialized) VALUES(1, 'sqlite', ?, 1)`, sqliteSchemaVersion); err != nil {
		return err
	}
	if err = beforeCommit(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit initial SQLite state: %w", err)
	}
	return nil
}

func importSQLiteState(tx *sql.Tx, state diskState) error {
	for _, p := range state.Projects {
		data, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO projects(id, root, data) VALUES(?, ?, ?)`, p.ID, p.Root, data); err != nil {
			return err
		}
	}
	for id, stored := range state.Agents {
		if stored == nil {
			return fmt.Errorf("invalid agent record %q", id)
		}
		if err := insertAgent(tx, stored); err != nil {
			return err
		}
	}
	for scope, r := range state.Receipts {
		if _, err := tx.Exec(`INSERT INTO receipts(scope, fingerprint, agent_id, message_id) VALUES(?, ?, ?, nullif(?, ''))`, scope, r.Fingerprint, r.AgentID, r.MessageID); err != nil {
			return err
		}
	}
	return nil
}

func insertAgent(tx *sql.Tx, stored *storedAgent) error {
	data, err := marshalAgentMetadata(stored.Agent)
	if err != nil {
		return err
	}
	createdSec, createdNS := splitTime(stored.Agent.CreatedAt)
	updatedSec, updatedNS := splitTime(stored.Agent.UpdatedAt)
	eventsNil := boolInt(stored.Events == nil)
	if _, err = tx.Exec(`INSERT INTO agents(id, project_id, parent_agent_id, settled, created_sec, created_nsec, updated_sec, updated_nsec, manifest_offset, queue_count, transcript_count, queue_nil, transcript_nil, events_nil, data)
		VALUES(?, ?, nullif(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, stored.Agent.ID, stored.Agent.ProjectID, stored.Agent.ParentAgentID, boolInt(stored.Agent.Settled), createdSec, createdNS, updatedSec, updatedNS, stored.ManifestOffset, len(stored.Agent.Queue), len(stored.Agent.Messages), boolInt(stored.Agent.Queue == nil), boolInt(stored.Agent.Messages == nil), eventsNil, data); err != nil {
		return err
	}
	for i, message := range stored.Agent.Queue {
		if err = insertQueue(tx, stored.Agent.ID, i, message); err != nil {
			return err
		}
	}
	for i, message := range stored.Agent.Messages {
		data, err = json.Marshal(message)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO transcript_messages(agent_id, ordinal, data) VALUES(?, ?, ?)`, stored.Agent.ID, i, data); err != nil {
			return err
		}
	}
	for _, event := range stored.Events {
		if err = insertEvent(tx, event); err != nil {
			return err
		}
	}
	return nil
}

func marshalAgentMetadata(value Agent) ([]byte, error) {
	metadata := value
	if value.Queue != nil {
		metadata.Queue = []QueuedMessage{}
	}
	if value.Messages != nil {
		metadata.Messages = []agent.Message{}
	}
	metadata.Events = nil
	return json.Marshal(metadata)
}

func splitTime(value time.Time) (int64, int) {
	return value.Unix(), value.Nanosecond()
}

func sameStoredTime(value time.Time, seconds int64, nanoseconds int) bool {
	return value.Unix() == seconds && value.Nanosecond() == nanoseconds
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func checkForeignKeys(tx *sql.Tx) error {
	rows, err := tx.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var constraint int
		if err = rows.Scan(&table, &rowID, &parent, &constraint); err != nil {
			return err
		}
		return fmt.Errorf("SQLite foreign-key violation in %s", table)
	}
	return rows.Err()
}

func (s *sqliteStore) apply(state diskState, before *stateChanges) error {
	if before == nil {
		return errors.New("missing state change set")
	}
	if len(before.projects) == 0 && len(before.agents) == 0 && len(before.receipts) == 0 && len(before.queue) == 0 {
		return nil
	}
	if err := s.privateFiles(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`PRAGMA defer_foreign_keys = ON`); err != nil {
		return err
	}

	for id, old := range before.projects {
		p, exists := state.Projects[id]
		if !exists {
			if _, err = tx.Exec(`DELETE FROM projects WHERE id = ?`, id); err != nil {
				return err
			}
			continue
		}
		if p.ID != id {
			return fmt.Errorf("project identity changed for %q", id)
		}
		data, marshalErr := json.Marshal(p)
		if marshalErr != nil {
			return marshalErr
		}
		if old == nil {
			if _, err = tx.Exec(`INSERT INTO projects(id, root, data) VALUES(?, ?, ?)`, id, p.Root, data); err != nil {
				return err
			}
			continue
		}
		oldData, marshalErr := json.Marshal(*old)
		if marshalErr != nil {
			return marshalErr
		}
		result, execErr := tx.Exec(`UPDATE projects SET root = ?, data = ? WHERE id = ? AND root = ? AND data = ?`, p.Root, data, id, old.Root, oldData)
		if err = requireOneChanged(result, execErr, "project", id); err != nil {
			return err
		}
	}

	for id, old := range before.agents {
		current := state.Agents[id]
		if current == nil {
			if _, err = tx.Exec(`DELETE FROM agents WHERE id = ?`, id); err != nil {
				return err
			}
			continue
		}
		if current.Agent.ID != id || current.Agent.Cursor != uint64(len(current.Events)) {
			return fmt.Errorf("invalid changed agent %q", id)
		}
		oldQueue, oldMessages, oldEvents := 0, 0, 0
		if old != nil {
			oldQueue = len(old.Agent.Queue)
			oldMessages = len(old.Agent.Messages)
			oldEvents = len(old.Events)
		}
		if len(current.Agent.Queue) < oldQueue || len(current.Agent.Messages) < oldMessages || len(current.Events) < oldEvents {
			return fmt.Errorf("append-only agent history shrank for %q", id)
		}
		data, marshalErr := marshalAgentMetadata(current.Agent)
		if marshalErr != nil {
			return marshalErr
		}
		createdSec, createdNS := splitTime(current.Agent.CreatedAt)
		updatedSec, updatedNS := splitTime(current.Agent.UpdatedAt)
		newArgs := []any{id, current.Agent.ProjectID, current.Agent.ParentAgentID, boolInt(current.Agent.Settled), createdSec, createdNS, updatedSec, updatedNS, current.ManifestOffset, len(current.Agent.Queue), len(current.Agent.Messages), boolInt(current.Agent.Queue == nil), boolInt(current.Agent.Messages == nil), boolInt(current.Events == nil), data}
		if old == nil {
			if _, err = tx.Exec(`INSERT INTO agents(id, project_id, parent_agent_id, settled, created_sec, created_nsec, updated_sec, updated_nsec, manifest_offset, queue_count, transcript_count, queue_nil, transcript_nil, events_nil, data)
				VALUES(?, ?, nullif(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, newArgs...); err != nil {
				return err
			}
		} else {
			oldCreatedSec, oldCreatedNS := splitTime(old.Agent.CreatedAt)
			oldUpdatedSec, oldUpdatedNS := splitTime(old.Agent.UpdatedAt)
			args := []any{current.Agent.ProjectID, current.Agent.ParentAgentID, boolInt(current.Agent.Settled), createdSec, createdNS, updatedSec, updatedNS, current.ManifestOffset, len(current.Agent.Queue), len(current.Agent.Messages), boolInt(current.Agent.Queue == nil), boolInt(current.Agent.Messages == nil), boolInt(current.Events == nil), data,
				id, old.Agent.ProjectID, old.Agent.ParentAgentID, boolInt(old.Agent.Settled), oldCreatedSec, oldCreatedNS, oldUpdatedSec, oldUpdatedNS, old.ManifestOffset, oldQueue, oldMessages, boolInt(old.Agent.Queue == nil), boolInt(old.Agent.Messages == nil), boolInt(old.Events == nil)}
			result, execErr := tx.Exec(`UPDATE agents SET project_id=?, parent_agent_id=nullif(?, ''), settled=?, created_sec=?, created_nsec=?, updated_sec=?, updated_nsec=?, manifest_offset=?, queue_count=?, transcript_count=?, queue_nil=?, transcript_nil=?, events_nil=?, data=?
				WHERE id=? AND project_id=? AND coalesce(parent_agent_id, '')=? AND settled=? AND created_sec=? AND created_nsec=? AND updated_sec=? AND updated_nsec=? AND manifest_offset=? AND queue_count=? AND transcript_count=? AND queue_nil=? AND transcript_nil=? AND events_nil=?`, args...)
			if err = requireOneChanged(result, execErr, "agent", id); err != nil {
				return err
			}
		}
		for index, oldMessage := range before.queue[id] {
			if index < 0 || index >= len(current.Agent.Queue) {
				return fmt.Errorf("edited queue position %d is invalid for agent %q", index, id)
			}
			if err = updateQueue(tx, id, index, oldMessage, current.Agent.Queue[index]); err != nil {
				return err
			}
		}
		for index := oldQueue; index < len(current.Agent.Queue); index++ {
			if _, edited := before.queue[id][index]; edited {
				continue
			}
			if err = insertQueue(tx, id, index, current.Agent.Queue[index]); err != nil {
				return err
			}
		}
		for index := oldMessages; index < len(current.Agent.Messages); index++ {
			data, err = json.Marshal(current.Agent.Messages[index])
			if err != nil {
				return err
			}
			if _, err = tx.Exec(`INSERT INTO transcript_messages(agent_id, ordinal, data) VALUES(?, ?, ?)`, id, index, data); err != nil {
				return err
			}
		}
		for index := oldEvents; index < len(current.Events); index++ {
			if current.Events[index].AgentID != id || current.Events[index].Cursor != uint64(index+1) {
				return fmt.Errorf("invalid appended event for agent %q", id)
			}
			if err = insertEvent(tx, current.Events[index]); err != nil {
				return err
			}
		}
	}

	for scope, old := range before.receipts {
		r, exists := state.Receipts[scope]
		if !exists {
			if _, err = tx.Exec(`DELETE FROM receipts WHERE scope = ?`, scope); err != nil {
				return err
			}
			continue
		}
		if old == nil {
			if _, err = tx.Exec(`INSERT INTO receipts(scope, fingerprint, agent_id, message_id) VALUES(?, ?, ?, nullif(?, ''))`, scope, r.Fingerprint, r.AgentID, r.MessageID); err != nil {
				return err
			}
			continue
		}
		result, execErr := tx.Exec(`UPDATE receipts SET fingerprint=?, agent_id=?, message_id=nullif(?, '') WHERE scope=? AND fingerprint=? AND agent_id=? AND coalesce(message_id, '')=?`, r.Fingerprint, r.AgentID, r.MessageID, scope, old.Fingerprint, old.AgentID, old.MessageID)
		if err = requireOneChanged(result, execErr, "receipt", scope); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func requireOneChanged(result sql.Result, err error, kind, id string) error {
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return fmt.Errorf("SQLite %s row %q disagrees with runtime state", kind, id)
	}
	return nil
}

func insertQueue(tx *sql.Tx, agentID string, ordinal int, message QueuedMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO queue_messages(agent_id, ordinal, message_id, status, data) VALUES(?, ?, ?, ?, ?)`, agentID, ordinal, message.ID, message.Status, data)
	return err
}

func updateQueue(tx *sql.Tx, agentID string, ordinal int, old, current QueuedMessage) error {
	oldData, err := json.Marshal(old)
	if err != nil {
		return err
	}
	data, err := json.Marshal(current)
	if err != nil {
		return err
	}
	result, execErr := tx.Exec(`UPDATE queue_messages SET message_id=?, status=?, data=? WHERE agent_id=? AND ordinal=? AND message_id=? AND status=? AND data=?`, current.ID, current.Status, data, agentID, ordinal, old.ID, old.Status, oldData)
	return requireOneChanged(result, execErr, "queue", fmt.Sprintf("%s/%d", agentID, ordinal))
}
func insertEvent(tx *sql.Tx, event Event) error {
	if event.Data == nil {
		event.Data = json.RawMessage("null")
	}
	if !json.Valid(event.Data) {
		return errors.New("invalid event JSON")
	}
	if event.Cursor > math.MaxInt64 {
		return fmt.Errorf("event cursor exceeds SQLite integer range for agent %q", event.AgentID)
	}
	sec, nsec := splitTime(event.CreatedAt)
	createdAt, err := event.CreatedAt.MarshalText()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO events(agent_id, cursor, type, data, created_sec, created_nsec, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`, event.AgentID, event.Cursor, event.Type, []byte(event.Data), sec, nsec, string(createdAt))
	return err
}

func (s *sqliteStore) readState() (diskState, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return diskState{}, err
	}
	defer tx.Rollback()
	state, err := readSQLiteState(tx)
	if err != nil {
		return diskState{}, err
	}
	if err = tx.Commit(); err != nil {
		return diskState{}, err
	}
	return state, nil
}

func validateParentDAG(agents map[string]*storedAgent) error {
	done := make(map[string]bool, len(agents))
	for start := range agents {
		if done[start] {
			continue
		}
		path := make([]string, 0)
		position := make(map[string]int)
		for id := start; id != "" && !done[id]; {
			record := agents[id]
			if record == nil {
				return fmt.Errorf("agent parent hierarchy references missing agent %q", id)
			}
			if _, exists := position[id]; exists {
				return fmt.Errorf("agent parent hierarchy contains a cycle at %q", id)
			}
			position[id] = len(path)
			path = append(path, id)
			id = record.Agent.ParentAgentID
		}
		for _, id := range path {
			done[id] = true
		}
	}
	return nil
}

func readSQLiteState(tx *sql.Tx) (diskState, error) {
	state := emptyState()
	rows, err := tx.Query(`SELECT id, root, data FROM projects`)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var id, root string
		var data []byte
		if err = rows.Scan(&id, &root, &data); err != nil {
			rows.Close()
			return state, err
		}
		var p Project
		if err = json.Unmarshal(data, &p); err != nil || p.ID != id || p.Root != root {
			rows.Close()
			return state, fmt.Errorf("invalid SQLite project record %q", id)
		}
		state.Projects[id] = p
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return state, err
	}
	if err = rows.Close(); err != nil {
		return state, err
	}

	eventsNil := make(map[string]bool)
	queueNil := make(map[string]bool)
	transcriptNil := make(map[string]bool)
	queueExpected := make(map[string]int)
	transcriptExpected := make(map[string]int)
	rows, err = tx.Query(`SELECT id, project_id, coalesce(parent_agent_id, ''), settled, created_sec, created_nsec, updated_sec, updated_nsec, manifest_offset, queue_count, transcript_count, queue_nil, transcript_nil, events_nil, data FROM agents`)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var id, projectID, parentID string
		var settled, queueIsNil, transcriptIsNil, eventNil int
		var createdSec, updatedSec, manifest int64
		var createdNS, updatedNS, expectedQueue, expectedTranscript int
		var data []byte
		if err = rows.Scan(&id, &projectID, &parentID, &settled, &createdSec, &createdNS, &updatedSec, &updatedNS, &manifest, &expectedQueue, &expectedTranscript, &queueIsNil, &transcriptIsNil, &eventNil, &data); err != nil {
			rows.Close()
			return state, err
		}
		var a Agent
		if err = json.Unmarshal(data, &a); err != nil || a.ID != id || a.ProjectID != projectID || a.ParentAgentID != parentID || boolInt(a.Settled) != settled || len(a.Queue) != 0 || len(a.Messages) != 0 || boolInt(a.Queue == nil) != queueIsNil || boolInt(a.Messages == nil) != transcriptIsNil {
			rows.Close()
			return state, fmt.Errorf("invalid SQLite agent metadata %q", id)
		}
		if !sameStoredTime(a.CreatedAt, createdSec, createdNS) || !sameStoredTime(a.UpdatedAt, updatedSec, updatedNS) {
			rows.Close()
			return state, fmt.Errorf("agent timestamp index disagrees with metadata for %q", id)
		}
		state.Agents[id] = &storedAgent{Agent: a, ManifestOffset: manifest}
		eventsNil[id] = eventNil == 1
		queueNil[id] = queueIsNil == 1
		transcriptNil[id] = transcriptIsNil == 1
		queueExpected[id] = expectedQueue
		transcriptExpected[id] = expectedTranscript
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return state, err
	}
	if err = rows.Close(); err != nil {
		return state, err
	}
	for id, stored := range state.Agents {
		if _, ok := state.Projects[stored.Agent.ProjectID]; !ok {
			return state, fmt.Errorf("agent %q references missing project", id)
		}
		if stored.Agent.ParentAgentID != "" {
			if _, ok := state.Agents[stored.Agent.ParentAgentID]; !ok {
				return state, fmt.Errorf("agent %q references missing parent", id)
			}
		}
	}
	if err = validateParentDAG(state.Agents); err != nil {
		return state, err
	}

	queueCount := make(map[string]int)
	rows, err = tx.Query(`SELECT agent_id, ordinal, message_id, status, data FROM queue_messages ORDER BY agent_id, ordinal`)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var agentID, messageID, status string
		var ordinal int
		var data []byte
		if err = rows.Scan(&agentID, &ordinal, &messageID, &status, &data); err != nil {
			rows.Close()
			return state, err
		}
		stored := state.Agents[agentID]
		if stored == nil || queueNil[agentID] || ordinal != queueCount[agentID] {
			rows.Close()
			return state, fmt.Errorf("invalid queue ordinal for agent %q", agentID)
		}
		var message QueuedMessage
		if err = json.Unmarshal(data, &message); err != nil || message.ID != messageID || message.Status != status {
			rows.Close()
			return state, fmt.Errorf("invalid queue record for agent %q", agentID)
		}
		stored.Agent.Queue = append(stored.Agent.Queue, message)
		queueCount[agentID]++
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return state, err
	}
	if err = rows.Close(); err != nil {
		return state, err
	}
	for id, expected := range queueExpected {
		if queueCount[id] != expected {
			return state, fmt.Errorf("queue row count for agent %q is %d, want %d", id, queueCount[id], expected)
		}
	}

	transcriptCount := make(map[string]int)
	rows, err = tx.Query(`SELECT agent_id, ordinal, data FROM transcript_messages ORDER BY agent_id, ordinal`)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var agentID string
		var ordinal int
		var data []byte
		if err = rows.Scan(&agentID, &ordinal, &data); err != nil {
			rows.Close()
			return state, err
		}
		stored := state.Agents[agentID]
		if stored == nil || transcriptNil[agentID] || ordinal != transcriptCount[agentID] {
			rows.Close()
			return state, fmt.Errorf("invalid transcript ordinal for agent %q", agentID)
		}
		var message agent.Message
		if err = json.Unmarshal(data, &message); err != nil {
			rows.Close()
			return state, fmt.Errorf("invalid transcript record for agent %q: %w", agentID, err)
		}
		stored.Agent.Messages = append(stored.Agent.Messages, message)
		transcriptCount[agentID]++
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return state, err
	}
	if err = rows.Close(); err != nil {
		return state, err
	}
	for id, expected := range transcriptExpected {
		if transcriptCount[id] != expected {
			return state, fmt.Errorf("transcript row count for agent %q is %d, want %d", id, transcriptCount[id], expected)
		}
	}

	eventCount := make(map[string]uint64)
	rows, err = tx.Query(`SELECT agent_id, cursor, type, data, created_sec, created_nsec, created_at FROM events ORDER BY agent_id, cursor`)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var event Event
		var cursor int64
		var data []byte
		var sec int64
		var nsec int
		var createdAt string
		if err = rows.Scan(&event.AgentID, &cursor, &event.Type, &data, &sec, &nsec, &createdAt); err != nil {
			rows.Close()
			return state, err
		}
		if err = event.CreatedAt.UnmarshalText([]byte(createdAt)); err != nil || !sameStoredTime(event.CreatedAt, sec, nsec) {
			rows.Close()
			return state, fmt.Errorf("invalid event timestamp for agent %q", event.AgentID)
		}
		stored := state.Agents[event.AgentID]
		if stored == nil || cursor <= 0 || uint64(cursor) != eventCount[event.AgentID]+1 || !json.Valid(data) {
			rows.Close()
			return state, fmt.Errorf("invalid event sequence for agent %q", event.AgentID)
		}
		event.Cursor = uint64(cursor)
		event.Data = json.RawMessage(data)
		stored.Events = append(stored.Events, event)
		eventCount[event.AgentID]++
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return state, err
	}
	if err = rows.Close(); err != nil {
		return state, err
	}
	for id, stored := range state.Agents {
		if eventsNil[id] && eventCount[id] != 0 {
			return state, fmt.Errorf("nil event history has rows for agent %q", id)
		}
		if eventsNil[id] {
			stored.Events = nil
		} else if stored.Events == nil {
			stored.Events = []Event{}
		}
		if uint64(len(stored.Events)) != stored.Agent.Cursor || stored.Agent.LastResponseCursor > stored.Agent.Cursor || stored.Agent.ReadCursor > stored.Agent.Cursor {
			return state, fmt.Errorf("invalid event cursor for agent %q", id)
		}
	}

	rows, err = tx.Query(`SELECT scope, fingerprint, agent_id, coalesce(message_id, '') FROM receipts`)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var scope string
		var r receipt
		if err = rows.Scan(&scope, &r.Fingerprint, &r.AgentID, &r.MessageID); err != nil {
			rows.Close()
			return state, err
		}
		if state.Agents[r.AgentID] == nil {
			rows.Close()
			return state, fmt.Errorf("receipt %q references missing agent", scope)
		}
		state.Receipts[scope] = r
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return state, err
	}
	if err = rows.Close(); err != nil {
		return state, err
	}
	if err = checkForeignKeys(tx); err != nil {
		return state, err
	}
	return state, nil
}

const (
	queuePositionQuery = `SELECT ordinal FROM queue_messages WHERE agent_id = ? AND message_id = ?`
	firstPendingQuery  = `SELECT ordinal FROM queue_messages WHERE agent_id = ? AND status = 'pending' ORDER BY ordinal LIMIT 1`
	projectByRootQuery = `SELECT id FROM projects WHERE root = ?`
)

func agentSelectionQueries(includeSettled bool, projectID string, recent bool) (string, string, []any) {
	conditions := make([]string, 0, 2)
	args := make([]any, 0, 2)
	if projectID != "" {
		conditions = append(conditions, "project_id = ?")
		args = append(args, projectID)
	}
	if !includeSettled {
		conditions = append(conditions, "settled = 0")
	}
	where := ""
	if len(conditions) != 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	order := "created_sec ASC, created_nsec ASC, id ASC"
	if recent {
		order = "updated_sec DESC, updated_nsec DESC, id ASC"
	}
	return `SELECT id FROM agents` + where + ` ORDER BY ` + order, `SELECT count(*) FROM agents` + where, args
}

func (s *sqliteStore) agentIDs(includeSettled bool, projectID string, recent bool, offset, limit int64) ([]string, int, error) {
	query, countQuery, args := agentSelectionQueries(includeSettled, projectID, recent)
	var total64 int64
	if err := s.db.QueryRow(countQuery, args...).Scan(&total64); err != nil {
		return nil, 0, err
	}
	if total64 > int64(math.MaxInt) {
		return nil, 0, errors.New("agent count exceeds platform limit")
	}
	if recent && limit >= 0 && (limit == 0 || offset >= total64) {
		return []string{}, int(total64), nil
	}
	queryArgs := append([]any(nil), args...)
	if recent && limit >= 0 {
		query += ` LIMIT ? OFFSET ?`
		queryArgs = append(queryArgs, limit, offset)
	}
	rows, err := s.db.Query(query, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	return ids, int(total64), rows.Err()
}

func (s *sqliteStore) queuePosition(agentID, messageID string) (int, bool, error) {
	var ordinal int
	err := s.db.QueryRow(queuePositionQuery, agentID, messageID).Scan(&ordinal)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return ordinal, err == nil, err
}

func (s *sqliteStore) firstPending(agentID string) (int, bool, error) {
	var ordinal int
	err := s.db.QueryRow(firstPendingQuery, agentID).Scan(&ordinal)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return ordinal, err == nil, err
}

func (s *sqliteStore) projectByRoot(root string) (string, bool, error) {
	var id string
	err := s.db.QueryRow(projectByRootQuery, root).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

func (s *sqliteStore) close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}
