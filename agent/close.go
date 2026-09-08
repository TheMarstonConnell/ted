package agent

import (
	"context"
	"time"

	"github.com/TheMarstonConnell/ted/browser"
)

var closeBrowserSessionIfRunning = browser.CloseSessionIfRunning

// Close releases this agent's browser tabs. The backend call only contacts an
// already-running daemon, so constructing or closing an agent that never used
// the browser cannot start one.
func (a *Agent) Close() error {
	a.mu.Lock()
	threadID, projectRoot := a.threadID, a.projectRoot
	a.mu.Unlock()
	if threadID == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return closeBrowserSessionIfRunning(ctx, projectRoot, threadID)
}
