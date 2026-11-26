#!/bin/bash
# Build and install the Android MTP FSKit driver into ~/Applications.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SDK="$(xcrun --show-sdk-path)"
APP="${HOME}/Applications/AndroidMTP.app"
APPEX="${APP}/Contents/Extensions/AndroidMTPFS.appex"

rm -rf "${APP}"
mkdir -p "${APP}/Contents/MacOS" "${APPEX}/Contents/MacOS"

echo "==> compiling Android MTP file system driver"
swiftc -target arm64-apple-macos27.0 -sdk "${SDK}" -O \
  -framework FSKit -framework ExtensionFoundation -framework Foundation \
  -parse-as-library \
  -o "${APPEX}/Contents/MacOS/AndroidMTPFS" \
  "${ROOT}/driver/AndroidMTPFS.swift"

clang -O2 -o "${APP}/Contents/MacOS/AndroidMTP" "${ROOT}/driver/host.c"
cp "${ROOT}/driver/Info-app.plist" "${APP}/Contents/Info.plist"
cp "${ROOT}/driver/Info-appex.plist" "${APPEX}/Contents/Info.plist"

ID="$(security find-identity -v -p codesigning | sed -n 's/.*"\(Apple Development:[^"]*\)".*/\1/p' | head -1)"
if [[ -z "${ID}" ]]; then
  ID="-"
  echo "==> no Apple Development certificate; signing ad-hoc"
else
  echo "==> signing with ${ID}"
fi

codesign --force --sign "${ID}" \
  --entitlements "${ROOT}/driver/appex.entitlements" \
  --generate-entitlement-der \
  "${APPEX}"
codesign --force --sign "${ID}" \
  --entitlements "${ROOT}/driver/app.entitlements" \
  --generate-entitlement-der \
  "${APP}"

pluginkit -a "${APPEX}" || true
pluginkit -e use -i org.hashim.androidmtp.fsmodule || true
echo "==> installed ${APP}"
echo "    System Settings → General → Login Items & Extensions → By Category → File System Extensions → Android MTP"
