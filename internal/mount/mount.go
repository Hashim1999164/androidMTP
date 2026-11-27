package mount

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Hashim1999164/androidMTP/internal/device"
	mtpfs "github.com/Hashim1999164/androidMTP/internal/fs"
	"github.com/Hashim1999164/androidMTP/internal/status"
)

// Options control how a device is mounted into Finder.
type Options struct {
	Selector   string
	MountPoint string
	VolumeName string
	AllowOther bool
	AndroidExt bool
	VFATMunge  bool // retained for CLI compatibility; VFAT munging is best-effort in FS
	USBTimeout int
	Debug      string
}

// ResolveMountPoint picks /Volumes/<name> or ~/Volumes/<name>.
func ResolveMountPoint(explicit, volName string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	vol := filepath.Base(volName)
	candidates := []string{
		filepath.Join("/Volumes", vol),
		filepath.Join(homeDir(), "Volumes", vol),
	}
	for _, c := range candidates {
		if err := ensureMountDir(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("could not create mount point under /Volumes or ~/Volumes — try --mount")
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "/tmp"
	}
	return h
}

func ensureMountDir(path string) error {
	fi, err := os.Stat(path)
	if err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("%s exists and is not a directory", path)
		}
		if mounted, _ := IsMounted(path); mounted {
			return fmt.Errorf("%s is already a mount point", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(path, 0o755)
}

// RemoveIdleMountPoint deletes a mount folder that is no longer mounted.
// Call it only after Finder has already dropped the disk. Deleting the
// folder while Finder still shows the icon leaves that icon stuck.
func RemoveIdleMountPoint(path string) {
	if path == "" {
		return
	}
	base := filepath.Base(path)
	if base == "" || base == "." || base == "/" || base == "Volumes" || base == "AndroidMTP" {
		return
	}
	if ok, _ := IsMounted(path); ok {
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".DS_Store" || strings.HasPrefix(name, "._") {
			_ = os.Remove(filepath.Join(path, name))
			continue
		}
		return
	}
	_ = os.Remove(path)
}

// IsMounted reports whether path is an active mount.
func IsMounted(path string) (bool, error) {
	out, err := exec.Command("mount").Output()
	if err != nil {
		return false, err
	}
	return strings.Contains(string(out), " on "+path+" ("), nil
}

// ReleaseDesktop waits until Finder has dropped the phone, then deletes the
// empty mount folder. The folder has to stay until Finder lets go; removing
// it sooner leaves the Desktop icon behind with no disk under it.
func ReleaseDesktop(path, name string) {
	if path == "" || !idlePathAllowed(path) {
		return
	}
	if ok, _ := IsMounted(path); ok {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = filepath.Base(path)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		listed, ok := finderVolumeNames()
		if ok && !strings.Contains(listed, name) {
			RemoveIdleMountPoint(path)
			return
		}
		if time.Now().After(deadline) {
			return
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			_ = os.Mkdir(path, 0o755)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func idlePathAllowed(path string) bool {
	base := filepath.Base(path)
	return base != "" && base != "." && base != "/" && base != "Volumes" && base != "AndroidMTP"
}

func finderVolumeNames() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "osascript", "-e", `tell application "Finder" to get (name of every disk as string) & linefeed & (name of every item of desktop as string)`).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// Unmount forcibly unmounts a path using diskutil then umount.
func Unmount(path string) error {
	if path == "" {
		return fmt.Errorf("empty mount path")
	}
	if ok, _ := IsMounted(path); !ok {
		return nil
	}
	if err := exec.Command("diskutil", "unmount", "force", path).Run(); err == nil {
		return nil
	}
	if err := exec.Command("umount", "-f", path).Run(); err == nil {
		return nil
	}
	return fmt.Errorf("failed to unmount %s", path)
}

// Run opens the device and mounts it as a local Finder volume.
// MTP is not safe to call from several threads, so this goroutine stays
// pinned to one OS thread for the whole session.
func Run(opts Options) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	dev, sids, di, err := device.Open(opts.Selector, opts.USBTimeout)
	if err != nil {
		return err
	}
	defer func() {
		_ = dev.Close()
		dev.Done()
	}()

	id, _ := dev.ID()
	volName := opts.VolumeName
	if volName == "" {
		volName = device.FriendlyName(di, id)
	}

	mp, err := ResolveMountPoint(opts.MountPoint, volName)
	if err != nil {
		return err
	}

	fsys, err := mtpfs.New(dev, sids, mtpfs.Options{Android: opts.AndroidExt})
	if err != nil {
		return fmt.Errorf("create MTP filesystem: %w", err)
	}

	_ = status.Write(status.State{
		PID:        os.Getpid(),
		MountPoint: mp,
		VolumeName: volName,
		DeviceID:   id,
		Model:      di.Model,
		StartedAt:  time.Now().Format(time.RFC3339),
	})
	defer status.RemovePID(os.Getpid())

	fmt.Fprintf(os.Stderr, "mounted %s → %s\n", volName, mp)
	return mtpfs.MountNative(fsys, mp, volName)
}

// Eject stops every running mount. It does not call umount: a forced
// unmount of this volume stays stuck in the kernel and freezes Finder.
func Eject() error {
	all := status.List()
	if len(all) == 0 {
		return fmt.Errorf("nothing mounted (no status file)")
	}
	for _, st := range all {
		if st.PID > 0 && st.PID != os.Getpid() {
			_ = syscall.Kill(st.PID, syscall.SIGTERM)
		}
	}
	deadline := time.Now().Add(800 * time.Millisecond)
	for time.Now().Before(deadline) {
		alive := false
		for _, st := range all {
			if st.PID > 0 && syscall.Kill(st.PID, 0) == nil {
				alive = true
			}
		}
		if !alive {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, st := range all {
		if st.PID > 0 && st.PID != os.Getpid() {
			_ = syscall.Kill(st.PID, syscall.SIGKILL)
		}
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		left := false
		for _, st := range all {
			if ok, _ := IsMounted(st.MountPoint); ok {
				left = true
			}
		}
		if !left {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, st := range all {
		ReleaseDesktop(st.MountPoint, st.VolumeName)
	}
	_ = status.Clear()
	for _, st := range all {
		fmt.Printf("ejected %s (%s)\n", st.VolumeName, st.MountPoint)
		notifyDone(st.VolumeName)
	}
	return nil
}

func notifyDone(name string) {
	// avoid import cycle — soft notification via osascript
	_ = exec.Command("osascript", "-e",
		`display notification "Ejected `+name+`" with title "androidmtp"`).Start()
}
