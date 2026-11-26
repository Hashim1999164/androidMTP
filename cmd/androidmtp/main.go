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
	"github.com/Hashim1999164/androidMTP/internal/status"
	"github.com/Hashim1999164/androidMTP/internal/version"
	"github.com/Hashim1999164/androidMTP/internal/watch"
)

func main() {
	root := &cobra.Command{
		Use:   "androidmtp",
		Short: "Mount Android MTP devices as Finder volumes on Apple Silicon",
		Long: `androidmtp mounts phones in USB File Transfer (MTP) mode as ejectable
Finder volumes using FUSE-T — no kernel extension, no Android File Transfer app.

Typical flow:
  1. brew install --cask fuse-t && brew install hashim1999164/tap/androidmtp
  2. Unlock phone → File Transfer / MTP
  3. androidmtp mount
  4. Use Finder; androidmtp eject when done`,
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
		sel := selector
		if sel == "" && len(os.Args) > 0 {
			// positional handled per-command
		}
		return mount.Options{
			Selector:   sel,
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

	mountCmd := &cobra.Command{
		Use:   "mount [device]",
		Short: "Mount an MTP device into Finder",
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
		Short: "Show active mount status",
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := status.Read()
			if err != nil {
				return err
			}
			if st == nil {
				fmt.Println("not mounted")
				return nil
			}
			mounted, _ := mount.IsMounted(st.MountPoint)
			fmt.Printf("volume:  %s\n", st.VolumeName)
			fmt.Printf("path:    %s\n", st.MountPoint)
			fmt.Printf("device:  %s\n", st.DeviceID)
			fmt.Printf("model:   %s\n", st.Model)
			fmt.Printf("pid:     %d\n", st.PID)
			fmt.Printf("since:   %s\n", st.StartedAt)
			fmt.Printf("mounted: %v\n", mounted)
			as, _ := agent.Status()
			fmt.Printf("agent:   %s\n", as)
			return nil
		},
	}

	serveCmd := &cobra.Command{
		Use:     "serve",
		Aliases: []string{"daemon"},
		Short:   "Watch for MTP devices and auto-mount them",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return watch.Run(ctx, watch.Options{
				Interval:  interval,
				MountOpts: opts(),
			})
		},
	}
	mountFlags(serveCmd)
	serveCmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "USB poll interval")

	installAgent := &cobra.Command{
		Use:   "install-agent",
		Short: "Install a LaunchAgent to auto-mount at login",
		RunE: func(cmd *cobra.Command, args []string) error {
			bin, _ := os.Executable()
			return agent.Install(bin)
		},
	}
	uninstallAgent := &cobra.Command{
		Use:   "uninstall-agent",
		Short: "Remove the androidmtp LaunchAgent",
		RunE: func(cmd *cobra.Command, args []string) error {
			return agent.Uninstall()
		},
	}

	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose FUSE-T, libusb, and MTP connectivity",
		Run: func(cmd *cobra.Command, args []string) {
			os.Exit(doctor.Check())
		},
	}

	root.AddCommand(listCmd, mountCmd, umountCmd, statusCmd, serveCmd, installAgent, uninstallAgent, doctorCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
