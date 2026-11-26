#!/usr/bin/env bash
# androidmtp one-liner installer for Apple Silicon macOS
# curl -fsSL https://raw.githubusercontent.com/Hashim1999164/androidMTP/main/install.sh | bash
set -euo pipefail

echo "==> androidmtp installer"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "error: androidmtp requires macOS" >&2
  exit 1
fi

if [[ "$(uname -m)" != "arm64" ]]; then
  echo "warn: designed for Apple Silicon (arm64); continuing anyway"
fi

if ! command -v brew >/dev/null 2>&1; then
  echo "==> installing Homebrew"
  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
  if [[ -x /opt/homebrew/bin/brew ]]; then
    eval "$(/opt/homebrew/bin/brew shellenv)"
  fi
fi

echo "==> installing FUSE-T (kext-less FUSE for Finder volumes)"
brew install --cask fuse-t

echo "==> installing libusb"
brew install libusb

echo "==> tapping hashim1999164/tap"
brew tap hashim1999164/tap

echo "==> installing androidmtp"
brew install androidmtp

echo
echo "==> done"
echo "Next steps:"
echo "  1. Unlock your Android phone and choose File Transfer / MTP"
echo "  2. androidmtp doctor"
echo "  3. androidmtp mount"
echo "  4. Optional auto-mount at login: androidmtp install-agent"
echo
androidmtp doctor || true
