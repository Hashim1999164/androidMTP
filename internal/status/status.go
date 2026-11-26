package status

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// State records the active mount so other CLI invocations can inspect/eject it.
type State struct {
	PID        int    `json:"pid"`
	MountPoint string `json:"mount_point"`
	VolumeName string `json:"volume_name"`
	DeviceID   string `json:"device_id"`
	Model      string `json:"model"`
	StartedAt  string `json:"started_at"`
}

var mu sync.Mutex

func dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(home, "Library", "Application Support", "androidmtp")
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	return d, nil
}

func path() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "status.json"), nil
}

// Write persists mount state.
func Write(s State) error {
	mu.Lock()
	defer mu.Unlock()
	p, err := path()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// Read loads mount state. Returns nil, nil when no status file exists.
func Read() (*State, error) {
	mu.Lock()
	defer mu.Unlock()
	p, err := path()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Clear removes the status file.
func Clear() error {
	mu.Lock()
	defer mu.Unlock()
	p, err := path()
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// RuntimeDir returns the androidmtp support directory.
func RuntimeDir() (string, error) {
	return dir()
}
