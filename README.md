# Android File Transfer for Mac

**Plug any Android into a Mac. It shows up in Finder and on the Desktop. Unplug it and both go away.**

androidMTP is a free, open-source Android File Transfer app for Apple Silicon. USB File Transfer (MTP) becomes a normal Mac disk: browse in Finder, copy, paste, and eject. The same disk sits on the Desktop. No Android File Transfer app. No MacDroid. No kernel extension.

It works with any phone in File Transfer mode — Samsung Galaxy, Google Pixel, Xiaomi, Redmi, POCO, OnePlus, Motorola, Oppo, Vivo, Realme, Infinix, Nothing, Sony Xperia, Huawei, and the rest.

---

## Install Android File Transfer on Mac

```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Hashim1999164/androidMTP/main/install.sh)"
```

One command installs plug-and-play. Then: unlock the phone, plug in USB, choose **File Transfer / MTP**. The phone appears in Finder and on the Desktop. Unplug the cable and the disk leaves both.

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

macOS has no built-in MTP drive, so an Android phone never shows up as storage on its own. androidMTP is a file system extension that mounts the phone as a local disk. A helper watches the USB cable: plug in and the disk appears in Finder and on the Desktop, unplug and it goes away.

The first time, turn on **Android MTP** under System Settings → General → Login Items & Extensions → File System Extensions.

## Transfer files from Android to Mac

1. Unlock the phone.
2. Plug in a data USB cable.
3. Choose **File Transfer**, **MTP**, or **Transfer files**.
4. Open the phone from Finder, or from the disk on the Desktop.
5. Copy and paste photos, videos, and folders like any other drive.
6. Unplug. The Finder sidebar and the Desktop icon both disappear.

## Requirements

- Apple Silicon Mac · macOS 26 or newer
- Android in **File Transfer / MTP** mode
- Data USB cable

## MTP limits

Works: browse, copy in/out, folders, delete files, rename in-folder.  
Limited: cross-folder moves, some in-place edits, recursive folder delete.

## Questions people ask

**Why doesn’t my Android show up on my Mac?**  
The phone has to be in File Transfer mode. Charging-only and photo modes (PTP) are not a file disk. androidMTP mounts MTP as a local Finder volume.

**Does the phone show on the Desktop, or only in Finder?**  
Both. The sidebar and the Desktop icon appear when the cable is connected, and both leave when it is disconnected.

**Is this Android File Transfer for Mac?**  
Yes. It replaces the old Android File Transfer app with a Homebrew install that mounts the phone in Finder.

**Does it work on Apple Silicon?**  
Yes. It is built for Apple Silicon and uses Apple’s file system extension API, not a kernel extension.

**Samsung, Pixel, Xiaomi, OnePlus?**  
Any Android that speaks MTP. The phone is detected by the File Transfer handshake, not by a brand list.

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
