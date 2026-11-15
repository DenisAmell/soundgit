#!/bin/bash
# Builds the macOS installer of Audio Backuper and checks it by installing it.
#
#   tools/packaging/macos/build-pkg.sh [output dir]      (default: dist/)
#
# Signing is switched on by the environment:
#   MACOS_APP_IDENTITY        "Developer ID Application: ..."  signs the plugins and the helper
#   MACOS_INSTALLER_IDENTITY  "Developer ID Installer: ..."    signs the installer
#   MACOS_NOTARY_PROFILE      notarytool keychain profile       notarizes and staples
# Without them the plugins are ad-hoc signed and the file is named "-unsigned".
#
#   AUDIO_BACKUPER_SKIP_VERIFY=1   only build; do not install on this machine
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
HERE="$ROOT/tools/packaging/macos"
OUT="${1:-$ROOT/dist}"
BUILD="$ROOT/juce-client/build-release"
mkdir -p "$OUT"

say() { printf '\n== %s\n' "$1"; }
die() { echo "FAIL: $1" >&2; exit 1; }

say "Building the plugin for Intel and Apple Silicon"
CONFIGURE=(-S "$ROOT/juce-client" -B "$BUILD" -DCMAKE_BUILD_TYPE=Release
           "-DCMAKE_OSX_ARCHITECTURES=arm64;x86_64" -DCMAKE_OSX_DEPLOYMENT_TARGET=11.0
           -DAUDIO_BACKUPER_INSTALL=OFF -DAUDIO_BACKUPER_TESTS=OFF)
# Reuse JUCE sources a developer build already downloaded.
[ -f "$ROOT/juce-client/build/_deps/juce-src/CMakeLists.txt" ] \
    && CONFIGURE+=("-DFETCHCONTENT_SOURCE_DIR_JUCE=$ROOT/juce-client/build/_deps/juce-src")
cmake "${CONFIGURE[@]}" > "$BUILD.configure.log" 2>&1 || { tail -20 "$BUILD.configure.log"; die "configure failed"; }
cmake --build "$BUILD" --parallel --target AudioBackuper_AU AudioBackuper_VST3 > "$BUILD.build.log" 2>&1 \
    || { grep -E "error|FAILED" "$BUILD.build.log" | head -20; die "build failed"; }

AU="$BUILD/AudioBackuper_artefacts/Release/AU/Audio Backuper.component"
VST3="$BUILD/AudioBackuper_artefacts/Release/VST3/Audio Backuper.vst3"
HELPER_IN="Contents/Resources/audio-backuper-helper"
[ -d "$AU" ] && [ -d "$VST3" ] || die "the build did not produce both bundles"
VERSION="$("$VST3/$HELPER_IN" --version)"
echo "version $VERSION"

SUFFIX="-unsigned"
if [ -n "${MACOS_APP_IDENTITY:-}" ]; then
    say "Signing with $MACOS_APP_IDENTITY"
    for bundle in "$AU" "$VST3"; do
        # The helper first: signing the bundle seals what is inside it.
        codesign --force --options runtime --timestamp --sign "$MACOS_APP_IDENTITY" "$bundle/$HELPER_IN"
        codesign --force --options runtime --timestamp --sign "$MACOS_APP_IDENTITY" "$bundle"
    done
    [ -n "${MACOS_INSTALLER_IDENTITY:-}" ] && SUFFIX=""
else
    echo "no MACOS_APP_IDENTITY: the plugins stay ad-hoc signed"
fi

say "Packaging"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
component() { # <id> <bundle> <folder under Library/Audio/Plug-Ins>
    local root="$STAGE/root-$1" plist="$STAGE/$1.plist"
    mkdir -p "$root/Library/Audio/Plug-Ins/$3"
    ditto "$2" "$root/Library/Audio/Plug-Ins/$3/$(basename "$2")"
    pkgbuild --analyze --root "$root" "$plist" >/dev/null 2>&1
    # Never "upgrade" some other copy of the bundle found elsewhere on the
    # disk (a build folder, for instance): always install into the plugin
    # folder. And install whatever version is there, so that reinstalling or
    # going back to an older build works.
    /usr/libexec/PlistBuddy -c "Add :0:BundleIsRelocatable bool false" "$plist" 2>/dev/null \
        || /usr/libexec/PlistBuddy -c "Set :0:BundleIsRelocatable false" "$plist"
    /usr/libexec/PlistBuddy -c "Set :0:BundleIsVersionChecked false" "$plist"
    pkgbuild --root "$root" --component-plist "$plist" --identifier "com.rozetki.audiobackuper.$1" \
             --version "$VERSION" --install-location / "$STAGE/$1.pkg" >/dev/null
}
component au "$AU" Components
component vst3 "$VST3" VST3
sed "s/@VERSION@/$VERSION/g" "$HERE/distribution.xml" > "$STAGE/distribution.xml"

PKG="$OUT/AudioBackuper-$VERSION-macOS$SUFFIX.pkg"
SIGN=()
[ -n "${MACOS_INSTALLER_IDENTITY:-}" ] && SIGN=(--sign "$MACOS_INSTALLER_IDENTITY" --timestamp)
productbuild --distribution "$STAGE/distribution.xml" --package-path "$STAGE" ${SIGN[@]+"${SIGN[@]}"} "$PKG" >/dev/null
echo "$PKG ($(du -h "$PKG" | cut -f1 | tr -d ' '))"

if [ -n "${MACOS_NOTARY_PROFILE:-}" ] && [ -z "$SUFFIX" ]; then
    say "Notarizing"
    xcrun notarytool submit "$PKG" --keychain-profile "$MACOS_NOTARY_PROFILE" --wait
    xcrun stapler staple "$PKG"
fi

[ -z "${AUDIO_BACKUPER_SKIP_VERIFY:-}" ] || { echo "verification skipped"; exit 0; }

say "Checking the installer by installing it for the current user"
BASE="$HOME/Library/Audio/Plug-Ins"
IAU="$BASE/Components/Audio Backuper.component"; IVST3="$BASE/VST3/Audio Backuper.vst3"
"$HERE/uninstall.sh" >/dev/null
[ ! -e "$IAU" ] && [ ! -e "$IVST3" ] || die "could not clear a previous installation"
installer -pkg "$PKG" -target CurrentUserHomeDirectory >/dev/null || die "the installer failed"

check_bundle() { # <bundle> <binary inside Contents/MacOS>
    [ -d "$1" ] || die "$1 was not installed"
    codesign --verify --deep --strict "$1" || die "$1: the signature is broken"
    local plist_version; plist_version="$(/usr/libexec/PlistBuddy -c "Print :CFBundleShortVersionString" "$1/Contents/Info.plist")"
    [ "$plist_version" = "$VERSION" ] || die "$1: plugin version $plist_version, helper $VERSION"
    [ "$("$1/$HELPER_IN" --version)" = "$VERSION" ] || die "$1: the helper inside does not run"
    for binary in "$1/Contents/MacOS/$2" "$1/$HELPER_IN"; do
        local archs; archs="$(lipo -archs "$binary")"
        [[ "$archs" == *arm64* && "$archs" == *x86_64* ]] || die "$binary is not universal: $archs"
    done
    echo "ok   $(basename "$1"): signature valid, version $VERSION, Intel + Apple Silicon, helper runs"
}
check_bundle "$IAU" "Audio Backuper"
check_bundle "$IVST3" "Audio Backuper"

# The validation Logic runs. It must not wake the real helper or keychain.
SCRATCH="$(mktemp -d)"
validate() {
    AUDIO_BACKUPER_DATA_DIR="$SCRATCH" AUDIO_BACKUPER_SECRETS=file AUDIO_BACKUPER_IDLE_EXIT_SECONDS=2 \
        auval -v aufx AbkR YrSt > "$SCRATCH/auval.log" 2>&1
    grep -q "AU VALIDATION SUCCEEDED" "$SCRATCH/auval.log"
}
if ! validate; then
    # The system may not have noticed the new component yet.
    killall -9 AudioComponentRegistrar 2>/dev/null || true
    sleep 3
    validate || { tail -15 "$SCRATCH/auval.log"; die "auval does not pass for the installed plugin"; }
fi
echo "ok   Audio Unit validation passes for the installed plugin"

DATA="$HOME/Library/Application Support/AudioBackuper"
had_data=0; [ -d "$DATA" ] && had_data=1
"$HERE/uninstall.sh" >/dev/null
[ ! -e "$IAU" ] && [ ! -e "$IVST3" ] || die "uninstall left the plugin behind"
[ "$had_data" = 0 ] || [ -d "$DATA" ] || die "uninstall removed the user's data"
echo "ok   uninstall removes the plugin and leaves the user's data alone"

# Leave the machine with the plugin installed, as a user would have it.
installer -pkg "$PKG" -target CurrentUserHomeDirectory >/dev/null
sleep 3; rm -rf "$SCRATCH"
echo
echo "PASS  $PKG"
