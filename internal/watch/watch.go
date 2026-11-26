package watch

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/Hashim1999164/androidMTP/internal/device"
	"github.com/Hashim1999164/androidMTP/internal/mount"
	"github.com/Hashim1999164/androidMTP/internal/status"
)

// Options for the auto-mount daemon.
type Options struct {
	Interval  time.Duration
	MountOpts mount.Options
	Once      bool
}

// Run polls for MTP devices and mounts the first one when none is mounted.
func Run(ctx context.Context, opts Options) error {
	if opts.Interval <= 0 {
		opts.Interval = 2 * time.Second
	}
	fmt.Fprintln(os.Stderr, "androidmtp daemon watching for MTP devices…")
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
			_ = status.Clear()
		}

		infos, err := device.ListInfos(opts.MountOpts.Selector)
		if err != nil || len(infos) == 0 {
			sleep(ctx, opts.Interval)
			continue
		}

		fmt.Fprintf(os.Stderr, "detected %s — mounting\n", infos[0].ID)
		mo := opts.MountOpts
		if mo.Selector == "" {
			mo.Selector = "0"
		}
		err = mount.Run(mo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mount error: %v\n", err)
			sleep(ctx, opts.Interval)
		}
		if opts.Once {
			return err
		}
	}
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
