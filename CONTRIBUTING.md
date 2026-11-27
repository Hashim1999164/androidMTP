# Contributing to androidMTP

Thanks for helping make Android ↔ macOS file transfer suck less.

## Development setup

```bash
brew install go libusb pkg-config
make build
./bin/androidmtp doctor
```

## Project layout

- `cmd/androidmtp` — CLI entrypoint (cobra)
- `internal/device` — MTP device discovery / open
- `internal/fs` — MTP filesystem served to the macOS file system driver
- `internal/mount` — mountpoint + status + eject
- `internal/watch` — USB plug-and-play daemon
- `internal/agent` — launchd LaunchAgent
- `internal/doctor` — environment checks
- `driver` — Apple FSKit file system extension
- `third_party/go-mtpfs` — MTP protocol, widened so every Android in File Transfer is accepted

## Guidelines

1. Keep the CLI the product — no GUI required for core flows.
2. The phone is a local FSKit volume. Do not add a network mount, macFUSE, or FUSE-T.
3. Document MTP limitations honestly in the README when behavior changes.
4. MTP sessions are not concurrency-safe. Keep USB calls on one thread.
5. Never commit secrets, tokens, or personal device serials from logs.

## Pull requests

- Open against `main`
- Include a short “why” in the PR body
- Note devices you tested (`androidmtp list` model string is enough)

## Code of conduct

Be respectful. Harassment or gatekeeping gets you removed from the project.
