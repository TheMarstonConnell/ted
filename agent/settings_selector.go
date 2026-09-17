package agent

// NewSettingsSelector validates model and effort choices without creating a
// runtime, resolving a working directory, or writing a session.
func NewSettingsSelector(providers []Provider) *Agent {
	a := &Agent{providers: append([]Provider(nil), providers...)}
	if models := a.ListModels(); len(models) > 0 {
		_, _ = a.SetModel(models[0].ID)
	}
	return a
}
