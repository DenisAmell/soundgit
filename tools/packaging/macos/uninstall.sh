#!/bin/bash
# Removes the Audio Backuper plugin. The upload queue, settings and the
# access to your cloud are kept, so that reinstalling continues where it
# left off; how to remove those too is printed at the end.
#
# Usage: uninstall.sh            remove the copy installed for the current user
#        sudo uninstall.sh --system   remove the copy installed for all users
set -euo pipefail

if [ "${1:-}" = "--system" ]; then
    BASE="/Library/Audio/Plug-Ins"; VOLUME="/"
else
    BASE="$HOME/Library/Audio/Plug-Ins"; VOLUME="$HOME"
fi

removed=0
for bundle in "$BASE/Components/Audio Backuper.component" "$BASE/VST3/Audio Backuper.vst3"; do
    if [ -e "$bundle" ]; then
        rm -rf "$bundle"
        echo "removed $bundle"
        removed=1
    fi
done
for id in com.rozetki.audiobackuper.au com.rozetki.audiobackuper.vst3; do
    pkgutil --volume "$VOLUME" --forget "$id" >/dev/null 2>&1 || true
done
[ "$removed" = 1 ] || echo "Audio Backuper is not installed in $BASE"

echo
echo "Kept: $HOME/Library/Application Support/AudioBackuper (queue and settings)"
echo "      and the 'AudioBackuper' items in Keychain Access (access to your cloud)."
echo "Delete them by hand to remove every trace."
