package agent

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
)

const label = "com.hashim1999164.androidmtp"

// stableBinary copies a binary out of folders macOS TCC blocks for LaunchAgents
// (Desktop, Documents, Downloads). Opening those paths from launchd hangs in dyld.
func stableBinary(abs string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	protected := false
	for _, dir := range []string{
		filepath.Join(home, "Desktop"),
		filepath.Join(home, "Documents"),
		filepath.Join(home, "Downloads"),
	} {
		rel, err := filepath.Rel(dir, abs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			protected = true
			break
		}
	}
	if !protected {
		return abs, nil
	}
	dir := filepath.Join(home, "Library", "Application Support", "androidmtp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, "androidmtp")
	in, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	_ = exec.Command("xattr", "-d", "com.apple.quarantine", dest).Run()
	_ = exec.Command("codesign", "--force", "--sign", "-", dest).Run()
	return dest, nil
}

// PlistPath returns the LaunchAgent plist path.
func PlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

// Install writes and loads a LaunchAgent that runs plug-and-play `androidmtp serve`.
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
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	// Prefer the stable Homebrew symlink when this binary lives in Cellar.
	if strings.Contains(abs, "/Cellar/androidmtp/") {
		if brewBin, err := exec.LookPath("androidmtp"); err == nil {
			if br, err := filepath.EvalSymlinks(brewBin); err == nil {
				abs = br
			} else {
				abs, _ = filepath.Abs(brewBin)
			}
		}
	}
	abs, err = stableBinary(abs)
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

	pathEnv := strings.Join([]string{
		"/opt/homebrew/bin",
		"/usr/local/bin",
		filepath.Join(home, ".fuse-t/usr/local/bin"),
		"/usr/bin",
		"/bin",
	}, ":")

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
    <string>--interval</string>
    <string>1s</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>2</integer>
  <key>ProcessType</key>
  <string>Interactive</string>
  <key>StandardOutPath</key>
  <string>{{.LogDir}}/androidmtp.log</string>
  <key>StandardErrorPath</key>
  <string>{{.LogDir}}/androidmtp.err.log</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>{{.Path}}</string>
    <key>HOME</key>
    <string>{{.Home}}</string>
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
		"Path":   pathEnv,
		"Home":   home,
	}); err != nil {
		return err
	}

	uid := strconv.Itoa(os.Getuid())
	domain := "gui/" + uid
	_ = exec.Command("launchctl", "bootout", domain, plist).Run()
	_ = exec.Command("launchctl", "unload", plist).Run()
	if err := exec.Command("launchctl", "bootstrap", domain, plist).Run(); err != nil {
		// Fallback for older macOS.
		if err2 := exec.Command("launchctl", "load", "-w", plist).Run(); err2 != nil {
			return fmt.Errorf("launchctl bootstrap/load: %v / %w", err, err2)
		}
	}
	_ = exec.Command("launchctl", "enable", domain+"/"+label).Run()
	_ = exec.Command("launchctl", "kickstart", "-k", domain+"/"+label).Run()

	fmt.Println("androidmtp plug-and-play: ON")
	fmt.Printf("  agent: %s\n", plist)
	fmt.Println("  Plug in your Android → File Transfer / MTP → it appears in Finder.")
	fmt.Println("  Disable anytime: androidmtp disable")
	return nil
}

// Uninstall unloads and removes the LaunchAgent.
func Uninstall() error {
	plist, err := PlistPath()
	if err != nil {
		return err
	}
	uid := strconv.Itoa(os.Getuid())
	domain := "gui/" + uid
	_ = exec.Command("launchctl", "bootout", domain, plist).Run()
	_ = exec.Command("launchctl", "unload", plist).Run()
	if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Println("androidmtp plug-and-play: OFF")
	return nil
}

// Enabled reports whether the agent plist exists.
func Enabled() bool {
	plist, err := PlistPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(plist)
	return err == nil
}

// Status reports whether the agent plist exists.
func Status() (string, error) {
	if Enabled() {
		plist, _ := PlistPath()
		return "enabled (plug-and-play) — " + plist, nil
	}
	return "disabled", nil
}
