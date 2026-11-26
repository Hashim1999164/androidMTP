#!/bin/bash
# Build and install the Android MTP file system driver into ~/Applications.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP="${HOME}/Applications/AndroidMTP.app"
XCODE="${DEVELOPER_DIR:-/Applications/Xcode.app/Contents/Developer}"

if [[ -x "${XCODE}/usr/bin/xcodebuild" ]]; then
  echo "==> building the Android MTP file system driver"
  export DEVELOPER_DIR="${XCODE}"
  DERIVED="${ROOT}/driver/build/DerivedData"
  xcodebuild \
    -project "${ROOT}/driver/AndroidMTP.xcodeproj" \
    -scheme AndroidMTP \
    -configuration Release \
    -destination "platform=macOS" \
    -derivedDataPath "${DERIVED}" \
    -allowProvisioningUpdates \
    -allowProvisioningDeviceRegistration \
    build \
    ARCHS=arm64 \
    ONLY_ACTIVE_ARCH=YES
  BUILT="${DERIVED}/Build/Products/Release/AndroidMTP.app"
  LS="/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
  "$LS" -u "${BUILT}" || true
  rm -rf "${APP}"
  cp -R "${BUILT}" "${APP}"
  "$LS" -f -R -trusted "${APP}" || true
  pluginkit -a "${APP}/Contents/Extensions/AndroidMTPFS.appex" || true
  pluginkit -e use -i org.hashim.androidmtp.fsmodule || true
  echo "==> installed ${APP}"
  echo "    System Settings → General → Login Items & Extensions → By Category → File System Extensions → turn on Android MTP"
  exit 0
fi

echo "==> Xcode is required to sign the file system driver" >&2
exit 1
