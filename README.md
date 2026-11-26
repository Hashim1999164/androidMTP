# androidMTP

**Mount Android phones as normal Finder volumes on Apple Silicon.**

Plug in USB → choose **File Transfer (MTP)** → the phone appears as an ejectable drive. Copy, paste, drag-and-drop in Finder or Terminal. No Android File Transfer app. No macFUSE kernel extension. No Reduced Security boot.

```
Phone ──USB MTP──► androidmtp ──cgofuse──► FUSE-T (NFS) ──► Finder /Volumes
```

## One-liner install

```bash
curl -fsSL https://raw.githubusercontent.com/Hashim1999164/androidMTP/main/install.sh | bash
```

## Homebrew

```bash
brew install --cask fuse-t
brew tap hashim1999164/tap
brew install androidmtp
```

## Quick start

```bash
# diagnose environment + detect phone
androidmtp doctor

# list connected MTP devices
androidmtp list

# mount into Finder (defaults to /Volumes/<Model> or ~/Volumes/<Model>)
androidmtp mount

# eject when done
androidmtp eject

# auto-mount whenever a phone is plugged in
androidmtp serve
# or persist across logins:
androidmtp install-agent
```

## Requirements

| | |
|---|---|
| Mac | Apple Silicon (arm64) recommended |
| OS | macOS 13+ (tested on Sequoia / newer) |
| Phone | Android in **File Transfer / MTP** mode |
| Cable | Data-capable USB cable |
| FUSE | [FUSE-T](https://www.fuse-t.org/) via `brew install --cask fuse-t` |

## CLI

| Command | Purpose |
|---------|---------|
| `androidmtp list` | Show connected MTP devices |
| `androidmtp mount [dev]` | Mount device (`0`, ID regex, or first) |
| `androidmtp eject` | Unmount / eject active volume |
| `androidmtp status` | Show active mount |
| `androidmtp serve` | Watch USB and auto-mount |
| `androidmtp install-agent` | LaunchAgent for login auto-mount |
| `androidmtp uninstall-agent` | Remove LaunchAgent |
| `androidmtp doctor` | Environment + device diagnostics |

### Mount flags

```
--dev string          device index or ID regex
--mount path          mount point
--volname name        Finder volume name
--android             Android MTP extensions (default true)
--usb-timeout ms      USB timeout (default 5000)
--debug usb,data,mtp,fuse
```

## How it works

1. **MTP engine** — pure Go MTP over `libusb` (battle-tested protocol stack from [go-mtpfs](https://github.com/hanwen/go-mtpfs)).
2. **Filesystem** — `cgofuse` user-space FS (browse, read, write, mkdir, unlink, rename-in-folder).
3. **Finder volume** — [FUSE-T](https://github.com/macos-fuse-t/fuse-t) bridges FUSE → local NFSv4 so macOS shows a normal ejectable volume **without a kernel extension**.

## MTP limits (honest)

MTP is an object protocol, not a block disk:

| Works | Limited / fails clearly |
|-------|-------------------------|
| Browse, Quick Look, copy in/out | Cross-folder move/rename |
| Create folders, delete files | In-place editor save-over on some devices |
| Rename within the same folder | Deleting non-empty folders in one step |

## Build from source

```bash
brew install go libusb pkg-config
brew install --cask fuse-t
git clone https://github.com/Hashim1999164/androidMTP.git
cd androidMTP
make build
./bin/androidmtp doctor
```

## Contributing

PRs welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). Please run `make build` and `androidmtp doctor` before opening a PR.

## License

MIT — see [LICENSE](LICENSE).

MTP protocol code paths build on [go-mtpfs](https://github.com/hanwen/go-mtpfs) (BSD). FUSE hosting uses [cgofuse](https://github.com/winfsp/cgofuse) (MIT) with a small Darwin loader patch for user-local FUSE-T installs.
