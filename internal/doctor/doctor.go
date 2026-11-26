package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Hashim1999164/androidMTP/internal/agent"
	"github.com/Hashim1999164/androidMTP/internal/device"
	"github.com/Hashim1999164/androidMTP/internal/version"
)

// Check runs environment diagnostics and prints a report.
func Check() int {
	ok := true
	fmt.Printf("androidmtp doctor %s\n", version.Version)
	fmt.Println(strings.Repeat("─", 48))

	check := func(name string, pass bool, detail string) {
		mark := "ok"
		if !pass {
			mark = "FAIL"
			ok = false
		}
		fmt.Printf("[%s] %s — %s\n", mark, name, detail)
	}

	check("GOARCH", runtime.GOARCH == "arm64" || runtime.GOARCH == "amd64",
		fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH))
	if runtime.GOOS != "darwin" {
		check("macOS", false, "androidmtp targets macOS")
	} else {
		check("macOS", true, runtime.GOOS)
	}

	fuseOK := false
	fuseDetail := "fuse-t not found"
	for _, p := range []string{
		"/usr/local/lib/libfuse-t.dylib",
		filepath.Join(os.Getenv("HOME"), ".fuse-t/usr/local/lib/libfuse-t.dylib"),
		"/Library/Application Support/fuse-t/lib/libfuse-t-1.2.9.dylib",
		filepath.Join(os.Getenv("HOME"), "Library/Application Support/fuse-t/lib/libfuse-t-1.2.9.dylib"),
	} {
		if _, err := os.Stat(p); err == nil {
			fuseOK = true
			fuseDetail = p
			break
		}
	}
	if !fuseOK {
		if _, err := exec.LookPath("go-nfsv4"); err == nil {
			fuseOK = true
			fuseDetail = "go-nfsv4 on PATH"
		}
	}
	check("FUSE-T", fuseOK, fuseDetail+"  (brew install --cask fuse-t)")

	switch fskitState() {
	case fskitReady:
		check("FSKit (local volume)", true, "enabled — phone mounts beside Macintosh HD, not under Network")
	case fskitInstalled:
		fmt.Printf("[warn] FSKit — fuse-t is installed but off. System Settings → General → Login Items & Extensions → By Category → File System Extensions → ⓘ → turn on fuse-t\n")
	default:
		fmt.Printf("[warn] FSKit — module not registered yet (install.sh registers it)\n")
	}

	driverOut, _ := exec.Command("pluginkit", "-m", "-i", "org.hashim.androidmtp.fsmodule").Output()
	driverLine := strings.TrimSpace(string(driverOut))
	if !strings.HasPrefix(driverLine, "+") && driverLine == "" {
		fmt.Printf("[warn] Android MTP driver — not installed yet (bash driver/build.sh)\n")
	} else if driverDisabled() {
		fmt.Printf("[warn] Android MTP driver — installed but off. System Settings → General → Login Items & Extensions → By Category → File System Extensions → ⓘ → turn on Android MTP\n")
	} else {
		check("Android MTP driver", true, "on — the phone mounts as a local volume")
	}

	libusbOK := false
	libusbDetail := "libusb not found"
	for _, p := range []string{
		"/opt/homebrew/lib/libusb-1.0.dylib",
		"/usr/local/lib/libusb-1.0.dylib",
	} {
		if _, err := os.Stat(p); err == nil {
			libusbOK = true
			libusbDetail = p
			break
		}
	}
	check("libusb", libusbOK, libusbDetail+"  (brew install libusb)")

	bin, err := exec.LookPath("androidmtp")
	if err != nil {
		fmt.Printf("[warn] androidmtp on PATH — not found (ok when running from ./bin)\n")
	} else {
		check("androidmtp on PATH", true, bin)
	}

	if agent.Enabled() {
		check("plug-and-play", true, "enabled — phones auto-mount on connect")
	} else {
		fmt.Printf("[warn] plug-and-play — disabled  (run: androidmtp enable)\n")
	}

	// ptpcamerad often claims PTP/MTP interfaces first.
	out, _ := exec.Command("pgrep", "-l", "ptpcamerad").Output()
	if len(strings.TrimSpace(string(out))) > 0 {
		check("ptpcamerad", true, "running — if claim fails, unplug/replug after starting androidmtp")
	} else {
		check("ptpcamerad", true, "not running")
	}

	infos, err := device.ListInfos("")
	if err != nil {
		check("MTP device", false, err.Error())
	} else {
		check("MTP device", true, fmt.Sprintf("%d found", len(infos)))
		for _, info := range infos {
			fmt.Printf("       #%d  %s", info.Index, info.ID)
			if info.Model != "" {
				fmt.Printf("  (%s)", info.Model)
			}
			fmt.Println()
			for _, s := range info.Storages {
				fmt.Printf("         storage: %s  free=%s / total=%s\n",
					s.Description, human(s.FreeBytes), human(s.TotalBytes))
			}
		}
	}

	fmt.Println(strings.Repeat("─", 48))
	if ok {
		fmt.Println("ready — plug in the phone, choose File Transfer, and it appears in Finder")
		return 0
	}
	fmt.Println("fix the FAIL items above, then re-run: androidmtp doctor")
	return 1
}

type fskitStatus int

const (
	fskitMissing fskitStatus = iota
	fskitInstalled
	fskitReady
)

// fskitState reports whether macOS will actually mount type "fuset".
// pluginkit's "+" flag is not that switch.
func fskitState() fskitStatus {
	out, _ := exec.Command("pluginkit", "-m", "-i", "org.fuset.fskit-srv.module").Output()
	line := strings.TrimSpace(string(out))
	if line == "" {
		return fskitMissing
	}
	dir, err := os.MkdirTemp("", "androidmtp-fskit-")
	if err != nil {
		return fskitInstalled
	}
	defer os.RemoveAll(dir)
	probe := exec.Command("mount", "-t", "fuset", "/tmp/androidmtp-not-a-session", dir)
	msg, _ := probe.CombinedOutput()
	if strings.Contains(string(msg), "is disabled") {
		return fskitInstalled
	}
	return fskitReady
}

func driverDisabled() bool {
	dir, err := os.MkdirTemp("", "androidmtp-driver-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	probe := exec.Command("mount", "-t", "androidmtp", "/tmp/androidmtp-not-a-session", dir)
	msg, _ := probe.CombinedOutput()
	return strings.Contains(string(msg), "is disabled")
}

func human(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
