package agent

import (
	"os/exec"
	"strings"
)

// ShowOnDesktop makes the phone a normal disk on the Desktop and in Finder.
// Connected servers stay off the Desktop, so the phone is not a network icon.
// Finder is restarted only when a setting actually changes.
func ShowOnDesktop() {
	wanted := []struct {
		key string
		on  bool
	}{
		{"CreateDesktop", true},
		{"ShowRemovableMediaOnDesktop", true},
		{"ShowExternalHardDrivesOnDesktop", true},
		{"ShowMountedServersOnDesktop", false},
	}
	changed := false
	for _, item := range wanted {
		if finderBool(item.key) == item.on {
			continue
		}
		val := "false"
		if item.on {
			val = "true"
		}
		if exec.Command("defaults", "write", "com.apple.finder", item.key, "-bool", val).Run() == nil {
			changed = true
		}
	}
	if !changed {
		return
	}
	_ = exec.Command("launchctl", "kickstart", "-k", "gui/"+uid()+"/com.apple.Finder").Run()
}

func finderBool(key string) bool {
	out, err := exec.Command("defaults", "read", "com.apple.finder", key).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

func uid() string {
	out, err := exec.Command("id", "-u").Output()
	if err != nil {
		return "501"
	}
	return strings.TrimSpace(string(out))
}
