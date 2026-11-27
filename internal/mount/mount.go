package mount

import (
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

// IsMounted reports whether path is an active mount.
func IsMounted(path string) (bool, error) {
	out, err := exec.Command("mount").Output()
	if err != nil {
		return false, err
	}
	return strings.Contains(string(out), " on "+path+" ("), nil
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
