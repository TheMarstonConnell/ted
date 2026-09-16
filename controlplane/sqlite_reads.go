package controlplane

import (
	"fmt"
	"math"
)

// Library snapshots remain usable after Close; HTTP selection reports database failures.
func (s *Service) listAgents(includeSettled bool, projectID string, page, pageSize int64) ([]Agent, int, error) {
	if s.store == nil {
		if pageSize > 0 {
			rows, total := s.AgentsPage(includeSettled, projectID, page, pageSize)
			return rows, total, nil
		}
		rows := s.Agents(includeSettled, projectID)
		return rows, len(rows), nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	recent := pageSize > 0
	offset, limit := int64(0), int64(-1)
	if recent {
		if page < 1 {
			return nil, 0, problem(400, "invalid", "page must be positive")
		}
		limit = pageSize
		if page-1 > math.MaxInt64/pageSize {
			offset = math.MaxInt64
		} else {
			offset = (page - 1) * pageSize
		}
	}
	ids, total, err := s.store.agentIDs(includeSettled, projectID, recent, offset, limit)
	if err != nil {
		return nil, 0, problem(503, "storage_failed", "cannot read agent index")
	}
	result := make([]Agent, 0, len(ids))
	for _, id := range ids {
		a := s.state.Agents[id]
		if a == nil {
			return nil, 0, problem(503, "storage_failed", "agent index disagrees with runtime state")
		}
		result = append(result, cloneAgent(a.Agent))
	}
	return result, total, nil
}

func (s *Service) queuePositionLocked(a *storedAgent, messageID string) (int, bool, error) {
	if s.store == nil {
		for i, m := range a.Agent.Queue {
			if m.ID == messageID {
				return i, true, nil
			}
		}
		return 0, false, nil
	}
	index, found, err := s.store.queuePosition(a.Agent.ID, messageID)
	if err != nil {
		return 0, false, err
	}
	if found && (index < 0 || index >= len(a.Agent.Queue) || a.Agent.Queue[index].ID != messageID) {
		return 0, false, fmt.Errorf("queue index disagrees with runtime state for %s", a.Agent.ID)
	}
	return index, found, nil
}

func (s *Service) firstPendingLocked(a *storedAgent) (int, bool, error) {
	if s.store == nil {
		for i, m := range a.Agent.Queue {
			if m.Status == "pending" {
				return i, true, nil
			}
		}
		return 0, false, nil
	}
	index, found, err := s.store.firstPending(a.Agent.ID)
	if err != nil {
		return 0, false, err
	}
	if found && (index < 0 || index >= len(a.Agent.Queue) || a.Agent.Queue[index].Status != "pending") {
		return 0, false, fmt.Errorf("pending index disagrees with runtime state for %s", a.Agent.ID)
	}
	return index, found, nil
}

func (s *Service) projectByRootLocked(root string) (string, bool, error) {
	if s.store != nil {
		return s.store.projectByRoot(root)
	}
	for id, p := range s.state.Projects {
		if p.Root == root {
			return id, true, nil
		}
	}
	return "", false, nil
}
