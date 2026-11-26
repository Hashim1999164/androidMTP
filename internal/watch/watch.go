package watch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/Hashim1999164/androidMTP/internal/device"
	"github.com/Hashim1999164/androidMTP/internal/mount"
	"github.com/Hashim1999164/androidMTP/internal/notify"
	"github.com/Hashim1999164/androidMTP/internal/status"
)

// Options for the auto-mount daemon.
type Options struct {
	Interval  time.Duration
	MountOpts mount.Options
	Once      bool
	NoNotify  bool
	NoFinder  bool
}

// Run polls for MTP devices and auto-mounts them (plug and play).
func Run(ctx context.Context, opts Options) error {
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	bin, err := os.Executable()
	if err != nil {
		bin = "androidmtp"
	}

	fmt.Fprintln(os.Stderr, "androidmtp: plug-and-play watcher active — connect a phone in File Transfer mode")

	failStreak := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		st, err := status.Read()
		if err != nil {
			return err
		}
		if st != nil {
			if ok, _ := mount.IsMounted(st.MountPoint); ok && alive(st.PID) {
				if opts.Once {
					return nil
				}
				sleep(ctx, opts.Interval)
				continue
			}
			// Stale status (unplugged / crashed mount).
			if st.MountPoint != "" {
				_ = mount.Unmount(st.MountPoint)
			}
			_ = status.Clear()
		}

		infos, err := device.ProbeConnected(opts.MountOpts.Selector)
		if err != nil || len(infos) == 0 {
			failStreak = 0
			sleep(ctx, opts.Interval)
			continue
		}

		info := infos[0]
		fmt.Fprintf(os.Stderr, "androidmtp: detected %s — mounting…\n", info.ID)
		if !opts.NoNotify {
			name := info.Model
			if name == "" {
				name = info.Product
			}
			if name == "" {
				name = "Android"
			}
			notify.Banner("androidmtp", name+" connected — mounting…")
		}

		cmd := exec.CommandContext(ctx, bin, buildMountArgs(opts.MountOpts, info.Index)...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "androidmtp: failed to start mount: %v\n", err)
			sleep(ctx, opts.Interval)
			continue
		}

		// Wait briefly for mount to appear, then open Finder.
		opened := false
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if !alive(cmd.Process.Pid) {
				break
			}
			st, _ := status.Read()
			if st != nil {
				if ok, _ := mount.IsMounted(st.MountPoint); ok {
					if !opts.NoFinder {
						notify.Finder(st.MountPoint)
					}
					if !opts.NoNotify {
						notify.Banner("androidmtp", st.VolumeName+" ready in Finder")
					}
					opened = true
					break
				}
			}
			sleep(ctx, 200*time.Millisecond)
		}
		if !opened {
			failStreak++
			if failStreak >= 3 && !opts.NoNotify {
				notify.Banner("androidmtp", "Mount failed — unlock the phone, choose File Transfer, and turn on fuse-t under File System Extensions")
			}
		} else {
			failStreak = 0
		}

		err = cmd.Wait()
		_ = status.Clear()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "androidmtp: mount ended: %v\n", err)
			// Brief backoff before reclaiming USB after ptpcamerad races.
			sleep(ctx, time.Second)
		}
		if opts.Once {
			return err
		}
	}
}

func buildMountArgs(mo mount.Options, index int) []string {
	args := []string{"mount"}
	if mo.Selector != "" {
		args = append(args, "--dev", mo.Selector)
	} else {
		args = append(args, "--dev", fmt.Sprintf("%d", index))
	}
	if mo.MountPoint != "" {
		args = append(args, "--mount", mo.MountPoint)
	}
	if mo.VolumeName != "" {
		args = append(args, "--volname", mo.VolumeName)
	}
	if mo.AllowOther {
		args = append(args, "--allow-other")
	}
	if !mo.AndroidExt {
		args = append(args, "--android=false")
	}
	if !mo.VFATMunge {
		args = append(args, "--vfat=false")
	}
	if mo.USBTimeout > 0 {
		args = append(args, "--usb-timeout", fmt.Sprintf("%d", mo.USBTimeout))
	}
	if mo.Debug != "" {
		args = append(args, "--debug", mo.Debug)
	}
	return args
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
