// Package config manages a small local file of saved connection targets
// (~/.ccdc-cli/targets.yaml) so you don't have to retype -H/-u/-p for every
// host during a competition. It's intentionally simple: no encryption,
// no passwords stored (those are always prompted at use-time), just
// host/port/username/type/kubeconfig-path per named target.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// TargetType identifies which module a target is for.
type TargetType string

const (
	TypeMySQL TargetType = "mysql"
	TypePsql  TargetType = "psql"
	TypeK8s   TargetType = "k8"
)

// Target is a saved, named connection profile. Passwords are never stored
// here - only enough connection info to skip re-typing flags.
type Target struct {
	Name           string     `yaml:"name"`
	Type           TargetType `yaml:"type"`
	Host           string     `yaml:"host,omitempty"`
	Port           int        `yaml:"port,omitempty"`
	Username       string     `yaml:"username,omitempty"`
	KubeconfigPath string     `yaml:"kubeconfig_path,omitempty"`
	Notes          string     `yaml:"notes,omitempty"`
}

type fileFormat struct {
	Targets []Target `yaml:"targets"`
}

// ConfigDir returns ~/.ccdc-cli, creating it if necessary.
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	dir := filepath.Join(home, ".ccdc-cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("could not create config directory: %w", err)
	}
	return dir, nil
}

func targetsPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "targets.yaml"), nil
}

// LoadTargets reads the saved targets file. A missing file is not an
// error - it just returns an empty slice, so a fresh install works fine.
func LoadTargets() ([]Target, error) {
	path, err := targetsPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Target{}, nil
		}
		return nil, fmt.Errorf("could not read targets file: %w", err)
	}

	var ff fileFormat
	if err := yaml.Unmarshal(data, &ff); err != nil {
		return nil, fmt.Errorf("could not parse targets file: %w", err)
	}
	return ff.Targets, nil
}

// SaveTargets writes the full target list back to disk, overwriting
// whatever was there.
func SaveTargets(targets []Target) error {
	path, err := targetsPath()
	if err != nil {
		return err
	}

	data, err := yaml.Marshal(fileFormat{Targets: targets})
	if err != nil {
		return fmt.Errorf("could not encode targets file: %w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("could not write targets file: %w", err)
	}
	return nil
}

// AddTarget appends a new target, replacing any existing target with the
// same name.
func AddTarget(t Target) error {
	targets, err := LoadTargets()
	if err != nil {
		return err
	}

	replaced := false
	for i, existing := range targets {
		if existing.Name == t.Name {
			targets[i] = t
			replaced = true
			break
		}
	}
	if !replaced {
		targets = append(targets, t)
	}

	return SaveTargets(targets)
}

// RemoveTarget deletes a target by name. Returns an error if no target
// with that name exists.
func RemoveTarget(name string) error {
	targets, err := LoadTargets()
	if err != nil {
		return err
	}

	out := make([]Target, 0, len(targets))
	found := false
	for _, t := range targets {
		if t.Name == name {
			found = true
			continue
		}
		out = append(out, t)
	}
	if !found {
		return fmt.Errorf("no target named %q", name)
	}

	return SaveTargets(out)
}

// FindTarget looks up a target by name.
func FindTarget(name string) (Target, error) {
	targets, err := LoadTargets()
	if err != nil {
		return Target{}, err
	}
	for _, t := range targets {
		if t.Name == name {
			return t, nil
		}
	}
	return Target{}, fmt.Errorf("no target named %q", name)
}
