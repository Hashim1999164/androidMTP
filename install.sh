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

echo "==> installing FUSE-T (Apple FSKit local volume — not a network drive)"
if ! brew install --cask fuse-t; then
  echo "==> cask needs an admin password; installing the signed FSKit app for this user"
fi
APP="$HOME/Applications/fuse-t.app"
if [[ ! -d "$APP" ]]; then
  PKG="$(ls -t "$HOME/Library/Caches/Homebrew/downloads/"*fuse-t*.pkg 2>/dev/null | head -1 || true)"
  if [[ -n "${PKG}" ]]; then
    rm -rf /tmp/androidmtp-fuset
    pkgutil --expand-full "$PKG" /tmp/androidmtp-fuset
    FOUND="$(find /tmp/androidmtp-fuset -name 'fuse-t.app' -type d | head -1)"
    if [[ -n "${FOUND}" ]]; then
      mkdir -p "$HOME/Applications"
      rm -rf "$APP"
      cp -R "$FOUND" "$APP"
    fi
  fi
fi
if [[ -d "$APP/Contents/Extensions/FskitSrvModule.appex" ]]; then
  pluginkit -a "$APP/Contents/Extensions/FskitSrvModule.appex" || true
  pluginkit -e use -i org.fuset.fskit-srv.module || true
  echo "==> Apple FSKit module enabled (local volume, not Network)"
fi

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
