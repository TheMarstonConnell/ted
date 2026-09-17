package controlplane

// Events and transcripts are append-only. Only edited queue entries need an undo copy.
type stateChanges struct {
	projects map[string]*Project
	agents   map[string]*storedAgent
	receipts map[string]*receipt
	queue    map[string]map[int]QueuedMessage
}

func newStateChanges() *stateChanges {
	return &stateChanges{projects: map[string]*Project{}, agents: map[string]*storedAgent{}, receipts: map[string]*receipt{}, queue: map[string]map[int]QueuedMessage{}}
}

func (c *stateChanges) project(state diskState, id string) {
	if _, seen := c.projects[id]; seen {
		return
	}
	if p, ok := state.Projects[id]; ok {
		c.projects[id] = &p
	} else {
		c.projects[id] = nil
	}
}
func (c *stateChanges) agent(state diskState, id string) {
	if _, seen := c.agents[id]; seen {
		return
	}
	if a, ok := state.Agents[id]; ok {
		snapshot := *a
		c.agents[id] = &snapshot
	} else {
		c.agents[id] = nil
	}
}
func (c *stateChanges) receipt(state diskState, key string) {
	if _, seen := c.receipts[key]; seen {
		return
	}
	if r, ok := state.Receipts[key]; ok {
		c.receipts[key] = &r
	} else {
		c.receipts[key] = nil
	}
}
func (c *stateChanges) queued(state diskState, id string, index int) {
	c.agent(state, id)
	if c.queue[id] == nil {
		c.queue[id] = map[int]QueuedMessage{}
	}
	if _, seen := c.queue[id][index]; !seen {
		c.queue[id][index] = state.Agents[id].Agent.Queue[index]
	}
}
func (c *stateChanges) rollback(state *diskState) {
	for id, p := range c.projects {
		if p == nil {
			delete(state.Projects, id)
		} else {
			state.Projects[id] = *p
		}
	}
	for id, a := range c.agents {
		if a == nil {
			delete(state.Agents, id)
			continue
		}
		for index, m := range c.queue[id] {
			a.Agent.Queue[index] = m
		}
		state.Agents[id] = a
	}
	for key, r := range c.receipts {
		if r == nil {
			delete(state.Receipts, key)
		} else {
			state.Receipts[key] = *r
		}
	}
}
