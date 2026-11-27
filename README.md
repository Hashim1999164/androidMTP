# androidMTP — Android File Transfer for Mac

**Plug any Android into a Mac. It shows up in Finder and on the Desktop.**

Open-source MTP for Apple Silicon. USB File Transfer becomes a normal ejectable drive: browse, copy, paste, and eject. No Android File Transfer app. No kernel extension.

Works with any phone in File Transfer mode — Samsung, Pixel, Xiaomi, OnePlus, Motorola, Oppo, and the rest.

---

## Install

```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Hashim1999164/androidMTP/main/install.sh)"
```

One command installs plug-and-play. Then: unlock the phone, plug in USB, choose **File Transfer / MTP**. The phone appears in Finder and on the Desktop.

---

### After install

```bash
androidmtp status          # confirm plug-and-play is on
androidmtp doctor          # diagnose if something fails
androidmtp eject           # or use Finder’s eject button
androidmtp disable         # turn auto-mount off
androidmtp enable          # turn it back on
```

### Homebrew

```bash
brew tap hashim1999164/tap && brew install androidmtp && androidmtp enable
```

---

## How Android file transfer works on Mac

```
Phone ──USB MTP──► androidmtp driver ──Apple FSKit──► Finder and the Desktop
```

macOS has no built-in MTP drive. androidMTP is a file system extension that mounts the phone as a local disk. A helper watches the USB cable: plug in and the disk appears, unplug and it goes away.

The first time, turn on **Android MTP** under System Settings → General → Login Items & Extensions → File System Extensions.

## Requirements

- Apple Silicon Mac · macOS 26 or newer
- Android in **File Transfer / MTP** mode
- Data USB cable

## MTP limits

Works: browse, copy in/out, folders, delete files, rename in-folder.  
Limited: cross-folder moves, some in-place edits, recursive folder delete.

## Build from source

```bash
brew install go libusb pkg-config
git clone https://github.com/Hashim1999164/androidMTP.git && cd androidMTP
make build && bash driver/build.sh && ./bin/androidmtp enable
```

## Contributing

PRs welcome — [CONTRIBUTING.md](CONTRIBUTING.md)

## License

MIT — [LICENSE](LICENSE). Uses [go-mtpfs](https://github.com/hanwen/go-mtpfs) (BSD).
