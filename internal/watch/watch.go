package watch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
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

type slot struct {
	info     device.Info
	cmd      *exec.Cmd
	gone     int
	seen     int
	failures int
}

// Run follows the USB cable for every Android in File Transfer. The phone's
// identity in the IORegistry is the signal: present means show the disk,
// absent means hide it. libusb keeps an open phone in its list, so it is not
// the signal. The USB device is opened only when the registry changes.
func Run(ctx context.Context, opts Options) error {
	if opts.Interval <= 0 {
		opts.Interval = 300 * time.Millisecond
	}
	bin, err := os.Executable()
	if err != nil {
		bin = "androidmtp"
	}

	fmt.Fprintln(os.Stderr, "androidmtp: plug-and-play watcher active — connect a phone in File Transfer mode")

	if androidmtpMounts() > 0 {
		fmt.Fprintln(os.Stderr, "androidmtp: refreshing disks already on the Mac")
		dropExtension()
	}

	slots := map[string]*slot{}
	var lastBus string

	for {
		if ctx.Err() != nil {
			for _, sl := range slots {
				if sl.cmd != nil && sl.cmd.Process != nil {
					stopMount(sl.cmd.Process.Pid)
					_ = sl.cmd.Wait()
				}
			}
			return ctx.Err()
		}

		bus, busOK := usbRegistry()
		if busOK && bus == lastBus && mountedAndAlive(slots) && allOnBus(slots, bus) {
			sleep(ctx, 300*time.Millisecond)
			continue
		}
		if busOK {
			lastBus = bus
		}

		for key, sl := range slots {
			if device.OnBus(sl.info) {
				sl.gone = 0
				continue
			}
			sl.gone++
			if sl.gone < 2 {
				continue
			}
			fmt.Fprintf(os.Stderr, "androidmtp: %s unplugged — hiding the disk\n", key)
			if sl.cmd != nil && sl.cmd.Process != nil {
				hidePhone(sl.cmd.Process.Pid)
				_ = sl.cmd.Wait()
			} else {
				hidePhone(0)
			}
			delete(slots, key)
		}

		infos, err := device.ProbeConnected(opts.MountOpts.Selector)
		if err != nil {
			infos = nil
		}
		present := map[string]device.Info{}
		for _, info := range infos {
			if device.OnBus(info) {
				present[info.Key()] = info
			}
		}

		for key, sl := range slots {
			if _, ok := present[key]; ok || device.OnBus(sl.info) {
				sl.seen = 2
			}
		}

		for key, info := range present {
			sl := slots[key]
			if sl != nil && sl.cmd != nil && sl.cmd.Process != nil && alive(sl.cmd.Process.Pid) {
				continue
			}
			if sl == nil {
				sl = &slot{info: info}
				slots[key] = sl
			}
			sl.info = info
			sl.seen++
			if sl.seen < 2 {
				continue
			}
			fmt.Fprintf(os.Stderr, "androidmtp: %s plugged in — showing the disk\n", key)
			cmd := exec.CommandContext(ctx, bin, buildMountArgs(opts.MountOpts, info)...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "androidmtp: failed to start mount: %v\n", err)
				sl.seen = 0
				continue
			}
			sl.cmd = cmd
			if waitMounted(ctx, 20*time.Second, cmd.Process.Pid) {
				sl.failures = 0
				if !opts.NoFinder {
					for _, st := range status.List() {
						if st.PID == cmd.Process.Pid {
							notify.Finder(st.MountPoint)
						}
					}
				}
				continue
			}
			sl.failures++
			fmt.Fprintf(os.Stderr, "androidmtp: disk did not appear for %s\n", key)
			hidePhone(cmd.Process.Pid)
			_ = cmd.Wait()
			sl.cmd = nil
			sl.seen = 0
			if sl.failures >= 3 && !opts.NoNotify {
				notify.Banner("androidmtp", "Unlock the phone and choose File Transfer")
				sl.failures = 0
			}
		}

		sleep(ctx, opts.Interval)
	}
}

func usbRegistry() (string, bool) {
	out, err := exec.Command("ioreg", "-p", "IOUSB", "-w", "0", "-l").Output()
	if err != nil {
		return "", false
	}
	var keep []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.Contains(line, "USB Serial Number"),
			strings.Contains(line, "idVendor"),
			strings.Contains(line, "idProduct"),
			strings.Contains(line, "USB Product Name"):
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n"), true
}

func buildMountArgs(mo mount.Options, info device.Info) []string {
	args := []string{"mount", "--no-finder"}
	switch {
	case mo.Selector != "":
		args = append(args, "--dev", mo.Selector)
	case info.ID != "":
		args = append(args, "--dev", regexp.QuoteMeta(info.ID))
	default:
		args = append(args, "--dev", fmt.Sprintf("%d", info.Index))
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

func waitMounted(ctx context.Context, d time.Duration, pid int) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) && alive(pid) {
		for _, st := range status.List() {
			if st.PID == pid {
				if ok, _ := mount.IsMounted(st.MountPoint); ok {
					return true
				}
			}
		}
		if ctx.Err() != nil {
			return false
		}
		sleep(ctx, 200*time.Millisecond)
	}
	for _, st := range status.List() {
		if st.PID == pid {
			ok, _ := mount.IsMounted(st.MountPoint)
			return ok
		}
	}
	return false
}

func hidePhone(pid int) {
	before := androidmtpMounts()
	stopMount(pid)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if before == 0 || androidmtpMounts() < before {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	dropExtension()
}

func allOnBus(slots map[string]*slot, bus string) bool {
	for _, sl := range slots {
		if serial := strings.TrimSpace(sl.info.Serial); serial != "" {
			if !strings.Contains(bus, serial) {
				return false
			}
			continue
		}
		vendor := fmt.Sprintf(`"idVendor" = %d`, sl.info.VendorID)
		product := fmt.Sprintf(`"idProduct" = %d`, sl.info.ProductID)
		if !strings.Contains(bus, vendor) || !strings.Contains(bus, product) {
			return false
		}
	}
	return true
}

func mountedAndAlive(slots map[string]*slot) bool {
	if len(slots) == 0 {
		return false
	}
	for _, sl := range slots {
		if sl.cmd == nil || sl.cmd.Process == nil || !alive(sl.cmd.Process.Pid) {
			return false
		}
	}
	return true
}

func androidmtpMounts() int {
	out, err := exec.Command("mount").Output()
	if err != nil {
		return 0
	}
	return strings.Count(string(out), "(androidmtp")
}

func releaseVolume(pid int) {
	stopMount(pid)
	dropExtension()
}

func dropExtension() {
	out, _ := exec.Command("pgrep", "-f", "AndroidMTPFS").Output()
	for _, field := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("mount").Output()
		if err != nil || !strings.Contains(string(out), "(androidmtp") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func stopMount(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && alive(pid) {
		time.Sleep(50 * time.Millisecond)
	}
	if alive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
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
