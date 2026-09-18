package browser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	modeHeadless = "headless"
	modeHeaded   = "headed"
)

type launchConfig struct {
	Mode       string `json:"mode"`
	Executable string `json:"executable,omitempty"`
}

func readLaunchConfig(home string) (launchConfig, error) {
	config := launchConfig{}
	path := filepath.Join(home, "browser", "config.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return launchConfig{Mode: modeHeadless}, nil
	}
	if err != nil {
		return config, fmt.Errorf("read browser configuration %s: %w", path, err)
	}
	invalid := func(err error) (launchConfig, error) {
		return launchConfig{}, fmt.Errorf("browser configuration %s: %w", path, err)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return invalid(fmt.Errorf("expected a JSON object"))
	}
	var file struct {
		Mode       json.RawMessage `json:"mode"`
		Executable string          `json:"executable"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return invalid(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return invalid(fmt.Errorf("expected exactly one JSON object"))
	}
	config.Executable = file.Executable
	if len(file.Mode) == 0 {
		config.Mode = modeHeadless
	} else if err := json.Unmarshal(file.Mode, &config.Mode); err != nil {
		return invalid(fmt.Errorf("mode: %w", err))
	}
	if config.Mode != modeHeadless && config.Mode != modeHeaded {
		return invalid(fmt.Errorf("mode must be %q or %q", modeHeadless, modeHeaded))
	}
	if config.Executable != "" && !filepath.IsAbs(config.Executable) {
		return invalid(fmt.Errorf("executable must be an absolute Chrome/Chromium path"))
	}
	return config, nil
}

func (m *manager) launchConfig() (launchConfig, error) {
	m.configOnce.Do(func() { m.config, m.configErr = readLaunchConfig(m.home) })
	return m.config, m.configErr
}
