package mount

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "on" && fields[i+1] == path {
				return true, nil
			}
		}
	}
	return false, nil
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

// ensureFuseTLibs helps cgofuse find libfuse-t.dylib at the hard-coded path.
func ensureFuseTLibs() {
	targets := []struct{ src, dst string }{
		{
			filepath.Join(homeDir(), ".fuse-t/usr/local/lib/libfuse-t.dylib"),
			"/usr/local/lib/libfuse-t.dylib",
		},
		{
			filepath.Join(homeDir(), "Library/Application Support/fuse-t/lib/libfuse-t-1.2.9.dylib"),
			"/usr/local/lib/libfuse-t.dylib",
		},
	}
	for _, t := range targets {
		if _, err := os.Stat(t.src); err != nil {
			continue
		}
		if _, err := os.Stat(t.dst); err == nil {
			return
		}
		_ = os.MkdirAll(filepath.Dir(t.dst), 0o755)
		_ = os.Symlink(t.src, t.dst)
		return
	}
}

// Run opens the device, mounts via FUSE-T/cgofuse, writes status, and blocks until unmount.
func Run(opts Options) error {
	ensureFuseTLibs()

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
	defer status.Clear()

	fmt.Fprintf(os.Stderr, "mounted %s → %s\n", volName, mp)
	if err := mtpfs.MountNative(fsys, mp, volName); err == nil {
		return nil
	} else {
		fmt.Fprintf(os.Stderr, "androidmtp: %v\n", err)
	}
	return mtpfs.Mount(fsys, mp, volName, opts.AllowOther)
}

// Eject stops a running mount using status + signals + unmount.
func Eject() error {
	st, err := status.Read()
	if err != nil {
		return err
	}
	if st == nil {
		return fmt.Errorf("nothing mounted (no status file)")
	}

	if st.PID > 0 && st.PID != os.Getpid() {
		if err := syscall.Kill(st.PID, syscall.SIGTERM); err == nil {
			time.Sleep(400 * time.Millisecond)
			_ = syscall.Kill(st.PID, syscall.SIGKILL)
		}
	}
	if err := Unmount(st.MountPoint); err != nil {
		_ = status.Clear()
		return err
	}
	_ = status.Clear()
	fmt.Printf("ejected %s (%s)\n", st.VolumeName, st.MountPoint)
	notifyDone(st.VolumeName)
	return nil
}

func notifyDone(name string) {
	// avoid import cycle — soft notification via osascript
	_ = exec.Command("osascript", "-e",
		`display notification "Ejected `+name+`" with title "androidmtp"`).Start()
}
