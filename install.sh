#!/usr/bin/env bash
# androidmtp one-liner — full plug-and-play on Apple Silicon macOS
# curl -fsSL https://raw.githubusercontent.com/Hashim1999164/androidMTP/main/install.sh | bash
set -euo pipefail

echo "==> androidmtp plug-and-play installer"

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

if [[ -x /opt/homebrew/bin/brew ]]; then
  eval "$(/opt/homebrew/bin/brew shellenv)"
fi

echo "==> installing FUSE-T (Finder volumes, no kernel extension)"
brew install --cask fuse-t

echo "==> installing libusb"
brew install libusb

echo "==> tapping hashim1999164/tap"
brew tap hashim1999164/tap 2>/dev/null || true
brew trust hashim1999164/tap 2>/dev/null || brew trust --formula hashim1999164/tap/androidmtp 2>/dev/null || true

echo "==> installing androidmtp"
brew install hashim1999164/tap/androidmtp || brew upgrade hashim1999164/tap/androidmtp

echo "==> enabling plug-and-play (LaunchAgent)"
androidmtp enable

echo
echo "============================================"
echo "  androidmtp is plug-and-play ready"
echo "============================================"
echo
echo "  1. Unlock your Android phone"
echo "  2. Plug in USB"
echo "  3. Choose File Transfer / MTP"
echo "  4. Wait a few seconds → Finder opens the phone"
echo
echo "  status:  androidmtp status"
echo "  doctor:  androidmtp doctor"
echo "  disable: androidmtp disable"
echo
androidmtp doctor || true
androidmtp status || true
