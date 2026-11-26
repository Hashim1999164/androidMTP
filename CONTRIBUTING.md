# Contributing to androidMTP

Thanks for helping make Android ↔ macOS file transfer suck less.

## Development setup

```bash
brew install go libusb pkg-config
brew install --cask fuse-t
make build
./bin/androidmtp doctor
```

## Project layout

- `cmd/androidmtp` — CLI entrypoint (cobra)
- `internal/device` — MTP device discovery / open
- `internal/fs` — cgofuse filesystem over MTP
- `internal/mount` — mountpoint + status + eject
- `internal/watch` — USB poll daemon
- `internal/agent` — launchd LaunchAgent
- `internal/doctor` — environment checks
- `third_party/cgofuse` — cgofuse with extra FUSE-T dlopen paths

## Guidelines

1. Keep the CLI the product — no GUI required for core flows.
2. Prefer Apple Silicon + FUSE-T; do not add macFUSE kext requirements.
3. Document MTP limitations honestly in the README when behavior changes.
4. Single-threaded FUSE (`-s`) stays the default; MTP sessions are not concurrency-safe.
5. Never commit secrets, tokens, or personal device serials from logs.

## Pull requests

- Open against `main`
- Include a short “why” in the PR body
- Note devices you tested (`androidmtp list` model string is enough)

## Code of conduct

Be respectful. Harassment or gatekeeping gets you removed from the project.
