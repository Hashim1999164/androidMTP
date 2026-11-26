package notify

import (
	"fmt"
	"os/exec"
	"strings"
)

// Finder opens a path in Finder.
func Finder(path string) {
	if path == "" {
		return
	}
	_ = exec.Command("open", path).Start()
}

// Banner shows a macOS notification.
func Banner(title, body string) {
	title = escape(title)
	body = escape(body)
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, body, title)
	_ = exec.Command("osascript", "-e", script).Start()
}

func escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
