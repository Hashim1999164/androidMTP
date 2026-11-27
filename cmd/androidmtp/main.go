package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Hashim1999164/androidMTP/internal/agent"
	"github.com/Hashim1999164/androidMTP/internal/device"
	"github.com/Hashim1999164/androidMTP/internal/doctor"
	"github.com/Hashim1999164/androidMTP/internal/mount"
	"github.com/Hashim1999164/androidMTP/internal/notify"
	"github.com/Hashim1999164/androidMTP/internal/status"
	"github.com/Hashim1999164/androidMTP/internal/version"
	"github.com/Hashim1999164/androidMTP/internal/watch"
)

func main() {
	root := &cobra.Command{
		Use:   "androidmtp",
		Short: "Plug-and-play Android MTP → Finder volumes on Apple Silicon",
		Long: `androidmtp mounts any Android in USB File Transfer (MTP) mode as an
ejectable Finder volume. No kernel extension.

Plug and play (default after install):
  1. Install once
  2. Plug in Android → choose File Transfer / MTP
  3. Phone appears in Finder automatically

Manual:
  androidmtp mount
  androidmtp eject`,
		Version: version.Version,
	}

	var (
		selector   string
		mountPoint string
		volName    string
		allowOther bool
		androidExt bool
		vfat       bool
		usbTimeout int
		debug      string
		interval   time.Duration
		noNotify   bool
		noFinder   bool
	)

	mountFlags := func(cmd *cobra.Command) {
		cmd.Flags().StringVar(&selector, "dev", "", "device index or ID regex")
		cmd.Flags().StringVar(&mountPoint, "mount", "", "mount point (default: /Volumes/<name> or ~/Volumes/<name>)")
		cmd.Flags().StringVar(&volName, "volname", "", "Finder volume name")
		cmd.Flags().BoolVar(&allowOther, "allow-other", false, "allow other users to access the mount")
		cmd.Flags().BoolVar(&androidExt, "android", true, "use Android MTP extensions when available")
		cmd.Flags().BoolVar(&vfat, "vfat", true, "sanitize names on removable VFAT-like storages")
		cmd.Flags().IntVar(&usbTimeout, "usb-timeout", 5000, "USB timeout in milliseconds")
		cmd.Flags().StringVar(&debug, "debug", "", "comma-separated: usb,data,mtp,fuse")
	}

	opts := func() mount.Options {
		return mount.Options{
			Selector:   selector,
			MountPoint: mountPoint,
			VolumeName: volName,
			AllowOther: allowOther,
			AndroidExt: androidExt,
			VFATMunge:  vfat,
			USBTimeout: usbTimeout,
			Debug:      debug,
		}
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List connected MTP devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			infos, err := device.ListInfos(selector)
			if err != nil {
				return err
			}
			for _, info := range infos {
				fmt.Printf("#%d  %s", info.Index, info.ID)
				if info.Model != "" {
					fmt.Printf("  model=%q", info.Model)
				}
				fmt.Println()
				for _, s := range info.Storages {
					fmt.Printf("     %s  free=%d total=%d\n", s.Description, s.FreeBytes, s.TotalBytes)
				}
			}
			return nil
		},
	}
	listCmd.Flags().StringVar(&selector, "dev", "", "filter by ID regex")

	var mountNoFinder bool
	mountCmd := &cobra.Command{
		Use:   "mount [device]",
		Short: "Mount an MTP device into Finder (manual)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o := opts()
			if len(args) == 1 && o.Selector == "" {
				o.Selector = args[0]
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			errCh := make(chan error, 1)
			go func() { errCh <- mount.Run(o) }()

			if !mountNoFinder {
				go func() {
					for i := 0; i < 40; i++ {
						select {
						case <-ctx.Done():
							return
						default:
						}
						st, _ := status.Read()
						if st != nil {
							if ok, _ := mount.IsMounted(st.MountPoint); ok {
								notify.Finder(st.MountPoint)
								notify.Banner("androidmtp", st.VolumeName+" ready in Finder")
								return
							}
						}
						time.Sleep(200 * time.Millisecond)
					}
				}()
			}

			select {
			case <-ctx.Done():
				_ = mount.Eject()
				return nil
			case err := <-errCh:
				return err
			}
		},
	}
	mountFlags(mountCmd)
	mountCmd.Flags().BoolVar(&mountNoFinder, "no-finder", false, "do not open Finder")

	umountCmd := &cobra.Command{
		Use:     "umount",
		Aliases: []string{"eject", "unmount"},
		Short:   "Eject / unmount the active androidmtp volume",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mount.Eject()
		},
	}

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show active mount + plug-and-play status",
		RunE: func(cmd *cobra.Command, args []string) error {
			as, _ := agent.Status()
			fmt.Printf("plug-and-play: %s\n", as)
			all := status.List()
			if len(all) == 0 {
				fmt.Println("volume: not mounted")
				return nil
			}
			for _, st := range all {
				mounted, _ := mount.IsMounted(st.MountPoint)
				fmt.Printf("volume:  %s\n", st.VolumeName)
				fmt.Printf("path:    %s\n", st.MountPoint)
				fmt.Printf("device:  %s\n", st.DeviceID)
				fmt.Printf("model:   %s\n", st.Model)
				fmt.Printf("pid:     %d\n", st.PID)
				fmt.Printf("since:   %s\n", st.StartedAt)
				fmt.Printf("mounted: %v\n", mounted)
			}
			return nil
		},
	}

	serveCmd := &cobra.Command{
		Use:     "serve",
		Aliases: []string{"daemon"},
		Short:   "Watch USB and auto-mount MTP phones (used by plug-and-play agent)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return watch.Run(ctx, watch.Options{
				Interval:  interval,
				MountOpts: opts(),
				NoNotify:  noNotify,
				NoFinder:  noFinder,
			})
		},
	}
	mountFlags(serveCmd)
	serveCmd.Flags().DurationVar(&interval, "interval", time.Second, "USB poll interval")
	serveCmd.Flags().BoolVar(&noNotify, "no-notify", false, "disable macOS notifications")
	serveCmd.Flags().BoolVar(&noFinder, "no-finder", false, "do not open Finder on mount")

	enableCmd := &cobra.Command{
		Use:     "enable",
		Aliases: []string{"install-agent"},
		Short:   "Enable plug-and-play (auto-mount on USB connect)",
		RunE: func(cmd *cobra.Command, args []string) error {
			bin, _ := os.Executable()
			return agent.Install(bin)
		},
	}
	disableCmd := &cobra.Command{
		Use:     "disable",
		Aliases: []string{"uninstall-agent"},
		Short:   "Disable plug-and-play auto-mount",
		RunE: func(cmd *cobra.Command, args []string) error {
			return agent.Uninstall()
		},
	}

	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the driver, libusb, agent, and MTP connectivity",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(doctor.Check())
		},
	}

	root.AddCommand(listCmd, mountCmd, umountCmd, statusCmd, serveCmd, enableCmd, disableCmd, doctorCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
