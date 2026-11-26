# androidMTP

**Plug your Android into a Mac. It shows up in Finder. That’s it.**

Apple Silicon · File Transfer (MTP) · ejectable Finder volume · no Android File Transfer app · no macFUSE kext · no Reduced Security.

```
Phone ──USB MTP──► androidmtp (background) ──FUSE-T──► Finder
```

## One-liner (full plug-and-play)

```bash
curl -fsSL https://raw.githubusercontent.com/Hashim1999164/androidMTP/main/install.sh | bash
```

This installs FUSE-T + androidmtp **and enables the background agent**.

Then:

1. Unlock phone  
2. Plug in USB  
3. Choose **File Transfer / MTP**  
4. Finder opens the phone automatically  

## Homebrew

```bash
brew install --cask fuse-t
brew tap hashim1999164/tap
brew install androidmtp
androidmtp enable    # turn on plug-and-play (done for you by install.sh)
```

## Daily use

| Action | Command |
|--------|---------|
| Plug-and-play status | `androidmtp status` |
| Diagnose | `androidmtp doctor` |
| Eject | Finder eject button, or `androidmtp eject` |
| Turn auto-mount off | `androidmtp disable` |
| Turn auto-mount on | `androidmtp enable` |
| Manual mount | `androidmtp mount` |

## Requirements

- Apple Silicon Mac (arm64)
- macOS 13+
- Android phone in **File Transfer / MTP** mode
- Data-capable USB cable
- [FUSE-T](https://www.fuse-t.org/) (`brew install --cask fuse-t`)

## How plug-and-play works

A LaunchAgent runs `androidmtp serve` at login. It watches USB for MTP devices, mounts them via FUSE-T as a Finder volume, opens Finder, and shows a notification. Unplug or eject → it waits for the next phone.

## MTP limits (honest)

| Works | Limited |
|-------|---------|
| Browse, copy in/out, Quick Look | Cross-folder move |
| Create folders, delete files | In-place editor save-over on some phones |
| Rename in the same folder | One-shot delete of non-empty folders |

## Build from source

```bash
brew install go libusb pkg-config
brew install --cask fuse-t
git clone https://github.com/Hashim1999164/androidMTP.git
cd androidMTP
make build
./bin/androidmtp enable
```

## Contributing

PRs welcome — see [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT — see [LICENSE](LICENSE). Builds on [go-mtpfs](https://github.com/hanwen/go-mtpfs) (BSD) and [cgofuse](https://github.com/winfsp/cgofuse) (MIT).
