package agent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"
)

const label = "com.hashim1999164.androidmtp"

// PlistPath returns the LaunchAgent plist path.
func PlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// Install writes and loads a LaunchAgent that runs `androidmtp serve`.
func Install(bin string) error {
	if bin == "" {
		var err error
		bin, err = exec.LookPath("androidmtp")
		if err != nil {
			return fmt.Errorf("androidmtp not on PATH: %w", err)
		}
	}
	abs, err := filepath.Abs(bin)
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	logDir := filepath.Join(home, "Library", "Logs")
	_ = os.MkdirAll(logDir, 0o755)
	_ = os.MkdirAll(filepath.Join(home, "Library", "LaunchAgents"), 0o755)

	plist, err := PlistPath()
	if err != nil {
		return err
	}

	const tmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>{{.Label}}</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.Bin}}</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>{{.LogDir}}/androidmtp.log</string>
  <key>StandardErrorPath</key>
  <string>{{.LogDir}}/androidmtp.err.log</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    <key>PKG_CONFIG_PATH</key>
    <string>/usr/local/lib/pkgconfig</string>
  </dict>
</dict>
</plist>
`
	f, err := os.Create(plist)
	if err != nil {
		return err
	}
	defer f.Close()
	t := template.Must(template.New("plist").Parse(tmpl))
	if err := t.Execute(f, map[string]string{
		"Label":  label,
		"Bin":    abs,
		"LogDir": logDir,
	}); err != nil {
		return err
	}

	_ = exec.Command("launchctl", "unload", plist).Run()
	if err := exec.Command("launchctl", "load", plist).Run(); err != nil {
		return fmt.Errorf("launchctl load: %w", err)
	}
	fmt.Printf("installed LaunchAgent %s\n", plist)
	fmt.Println("androidmtp will auto-mount MTP devices at login")
	return nil
}

// Uninstall unloads and removes the LaunchAgent.
func Uninstall() error {
	plist, err := PlistPath()
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "unload", plist).Run()
	if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println("uninstalled androidmtp LaunchAgent")
	return nil
}

// Status reports whether the agent plist exists.
func Status() (string, error) {
	plist, err := PlistPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(plist); err == nil {
		return fmt.Sprintf("installed: %s", plist), nil
	}
	return "not installed", nil
}
