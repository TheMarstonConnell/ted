package agent

import (
	"errors"
	"fmt"
	"slices"
)

// Effort is a model-supported reasoning level. Empty means unsupported/unset.
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
)

var ErrBusy = errors.New("agent is busy; try again after this turn")

// ModelInfo carries the supported subset of a model's capabilities. An empty
// Efforts list means this integration does not expose configurable effort.
type ModelInfo struct {
	ID            string
	Name          string
	Provider      string
	Efforts       []Effort
	DefaultEffort Effort
}

type Settings struct {
	Model    string
	Provider string
	Effort   Effort
}

// SettingsChange describes an atomic settings update, including any fallback.
type SettingsChange struct {
	Before         Settings
	After          Settings
	EffortAdjusted bool
}

func (a *Agent) Settings() Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings
}

// ListModels returns detached metadata from configured providers in priority order.
func (a *Agent) ListModels() []ModelInfo {
	var models []ModelInfo
	for _, provider := range a.providers {
		for _, model := range provider.ListModels() {
			model.Provider = provider.Name()
			if model.Name == "" {
				model.Name = model.ID
			}
			model.ID = model.Provider + "/" + model.ID
			model.Efforts = slices.Clone(model.Efforts)
			models = append(models, model)
		}
	}
	return models
}

func (a *Agent) currentModel() ModelInfo {
	for _, model := range a.ListModels() {
		if model.ID == a.settings.Model {
			return model
		}
	}
	return ModelInfo{}
}

func (a *Agent) ListEfforts() []Effort {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.currentModel().Efforts
}

func (a *Agent) SetModel(id string) (SettingsChange, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return SettingsChange{}, ErrBusy
	}
	for _, model := range a.ListModels() {
		if model.ID != id {
			continue
		}
		change := SettingsChange{Before: a.settings}
		effort := a.settings.Effort
		if !slices.Contains(model.Efforts, effort) {
			effort = model.DefaultEffort
		}
		if len(model.Efforts) == 0 {
			effort = ""
		}
		if effort != "" && !slices.Contains(model.Efforts, effort) {
			return SettingsChange{}, fmt.Errorf("model %q has an unsupported default effort", id)
		}
		a.settings = Settings{Model: id, Provider: model.Provider, Effort: effort}
		change.After = a.settings
		change.EffortAdjusted = change.Before.Effort != effort
		return change, nil
	}
	return SettingsChange{}, fmt.Errorf("unknown model %q", id)
}

func (a *Agent) SetEffort(effort Effort) (SettingsChange, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return SettingsChange{}, ErrBusy
	}
	model := a.currentModel()
	if len(model.Efforts) == 0 {
		return SettingsChange{}, fmt.Errorf("model %q does not expose configurable effort", a.settings.Model)
	}
	if !slices.Contains(model.Efforts, effort) {
		return SettingsChange{}, fmt.Errorf("unsupported effort %q for %s", effort, a.settings.Model)
	}
	change := SettingsChange{Before: a.settings}
	a.settings.Effort = effort
	change.After = a.settings
	return change, nil
}
