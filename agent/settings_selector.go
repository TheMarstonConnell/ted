package agent

// SettingsSelector validates model and effort choices without creating a runtime,
// resolving a working directory, or writing a session.
type SettingsSelector struct {
	candidate *Agent
}

func NewSettingsSelector(providers []Provider) *SettingsSelector {
	a := &Agent{providers: append([]Provider(nil), providers...)}
	if models := a.ListModels(); len(models) > 0 {
		_, _ = a.SetModel(models[0].ID)
	}
	return &SettingsSelector{candidate: a}
}

func (s *SettingsSelector) Settings() Settings { return s.candidate.Settings() }
func (s *SettingsSelector) SetModel(id string) (SettingsChange, error) {
	return s.candidate.SetModel(id)
}
func (s *SettingsSelector) SetEffort(effort Effort) (SettingsChange, error) {
	return s.candidate.SetEffort(effort)
}
