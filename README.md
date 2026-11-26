# androidMTP

**Plug your Android into a Mac. It shows up in Finder.**

Apple Silicon · MTP File Transfer · ejectable drive · no Android File Transfer · no kext

---

## Install (copy → paste → enter)

```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Hashim1999164/androidMTP/main/install.sh)"
```

That one command installs everything and turns on plug-and-play.

Then: unlock phone → plug USB → **File Transfer / MTP** → Finder opens the phone.

---

### After install

```bash
androidmtp status          # confirm plug-and-play is on
androidmtp doctor          # diagnose if something fails
androidmtp eject           # or use Finder’s eject button
androidmtp disable         # turn auto-mount off
androidmtp enable          # turn it back on
```

### Alternative (Homebrew)

```bash
brew install --cask fuse-t && brew tap hashim1999164/tap && brew install androidmtp && androidmtp enable
```

---

## How it works

```
Phone ──USB MTP──► androidmtp driver ──Apple FSKit──► Finder volume
```

The driver is a macOS file system extension (`driver/`). A LaunchAgent watches USB, mounts the phone as a local ejectable volume, and opens Finder. One switch, the first time: System Settings → General → Login Items & Extensions → File System Extensions → **Android MTP**. Until that switch is on, the same local volume is provided through the fuse-t FSKit module.

## Requirements

- Apple Silicon Mac · macOS 26 or newer
- Android in **File Transfer / MTP** mode
- Data USB cable

## MTP limits

Works: browse, copy in/out, folders, delete files, rename in-folder.  
Limited: cross-folder moves, some in-place edits, recursive folder delete.

## Build from source

```bash
brew install go libusb pkg-config && brew install --cask fuse-t
git clone https://github.com/Hashim1999164/androidMTP.git && cd androidMTP
make build && bash driver/build.sh && ./bin/androidmtp enable
```

## Contributing

PRs welcome — [CONTRIBUTING.md](CONTRIBUTING.md)

## License

MIT — [LICENSE](LICENSE). Uses [go-mtpfs](https://github.com/hanwen/go-mtpfs) (BSD) and [cgofuse](https://github.com/winfsp/cgofuse) (MIT).
