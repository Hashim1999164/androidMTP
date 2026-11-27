package status

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// State records one mounted phone so other CLI invocations can inspect or eject it.
type State struct {
	PID        int    `json:"pid"`
	MountPoint string `json:"mount_point"`
	VolumeName string `json:"volume_name"`
	DeviceID   string `json:"device_id"`
	Model      string `json:"model"`
	StartedAt  string `json:"started_at"`
}

type file struct {
	Mounts []State `json:"mounts"`
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

func load() ([]State, error) {
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
	if strings.Contains(string(b), `"mounts"`) {
		var f file
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, err
		}
		return f.Mounts, nil
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	if s.PID == 0 && s.MountPoint == "" {
		return nil, nil
	}
	return []State{s}, nil
}

func save(mounts []State) error {
	p, err := path()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(file{Mounts: mounts}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// Write records one mounted phone. Several phones can be mounted at once.
func Write(s State) error {
	mu.Lock()
	defer mu.Unlock()
	mounts, err := load()
	if err != nil {
		return err
	}
	replaced := false
	for i := range mounts {
		if mounts[i].PID == s.PID {
			mounts[i] = s
			replaced = true
			break
		}
	}
	if !replaced {
		mounts = append(mounts, s)
	}
	return save(mounts)
}

// List returns every mounted phone.
func List() []State {
	mu.Lock()
	defer mu.Unlock()
	mounts, err := load()
	if err != nil || len(mounts) == 0 {
		return nil
	}
	out := make([]State, len(mounts))
	copy(out, mounts)
	return out
}

// Read returns one mounted phone, or nil when nothing is mounted.
func Read() (*State, error) {
	all := List()
	if len(all) == 0 {
		return nil, nil
	}
	s := all[0]
	return &s, nil
}

// RemovePID drops one phone from the status file.
func RemovePID(pid int) error {
	mu.Lock()
	defer mu.Unlock()
	mounts, err := load()
	if err != nil {
		return err
	}
	kept := make([]State, 0, len(mounts))
	for _, m := range mounts {
		if m.PID != pid {
			kept = append(kept, m)
		}
	}
	if len(kept) == 0 {
		return clearLocked()
	}
	return save(kept)
}

// Clear removes the status file.
func Clear() error {
	mu.Lock()
	defer mu.Unlock()
	return clearLocked()
}

func clearLocked() error {
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
