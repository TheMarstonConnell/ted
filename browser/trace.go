package browser

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const maxTraceBytes int64 = 512 << 10

type traceRecord struct {
	Time       time.Time `json:"time"`
	Action     string    `json:"action"`
	OK         bool      `json:"ok"`
	Code       string    `json:"code,omitempty"`
	DurationMS int64     `json:"duration_ms"`
}

// writeTrace intentionally accepts no Params or response Data. In particular,
// form values, URLs, selectors and raw CDP payloads must never reach the trace.
// One current and one previous 512 KiB file bounds disk use per thread.
func (s *session) writeTrace(action string, started time.Time, actionErr error) {
	record := traceRecord{Time: started.UTC(), Action: action, OK: actionErr == nil, DurationMS: time.Since(started).Milliseconds()}
	if actionErr != nil {
		record.Code = errorCode(actionErr)
	}
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	line = append(line, '\n')

	// Sessions for the same thread can technically exist in separate project
	// browsers, so use the manager lock to coordinate their common trace file.
	m := s.project.manager
	m.traceMu.Lock()
	defer m.traceMu.Unlock()
	path := filepath.Join(s.dir, "browser-trace.jsonl")
	if st, err := os.Stat(path); err == nil && st.Size()+int64(len(line)) > maxTraceBytes {
		previous := filepath.Join(s.dir, "browser-trace.previous.jsonl")
		_ = os.Remove(previous)
		_ = os.Rename(path, previous)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_ = os.Chmod(path, 0o600)
	_, _ = f.Write(line)
	_ = f.Close()
}

func errorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var service *serviceError
	if errors.As(err, &service) {
		return service.code
	}
	return "internal"
}
