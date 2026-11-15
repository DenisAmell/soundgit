#!/bin/bash
# Renders the plugin window in each of its states to PNG files, using the
# real helper against a fake cloud. Usage: tools/reaper/backuper-screens.sh <output dir>
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="${1:?output directory}"; mkdir -p "$OUT"
WORK="$(mktemp -d)"; mkdir -p "$WORK/Bounces"
trap 'kill $CLOUD_PID 2>/dev/null || true; rm -rf "$WORK"' EXIT
(cd "$ROOT/core" && go build -o "$WORK/fakecloud" ./internal/backuper/cloud/cloudtest/fakecloud)
"$WORK/fakecloud" > "$WORK/cloud.txt" & CLOUD_PID=$!
for _ in $(seq 1 50); do [ "$(wc -l < "$WORK/cloud.txt")" -ge 2 ] && break; perl -e 'select(undef,undef,undef,0.1)'; done
export AUDIO_BACKUPER_DATA_DIR="$WORK/data" AUDIO_BACKUPER_SECRETS=file AUDIO_BACKUPER_IDLE_EXIT_SECONDS=3
export AUDIO_BACKUPER_YANDEX_API_URL="$(sed -n 1p "$WORK/cloud.txt")"
export AUDIO_BACKUPER_HELPER="$ROOT/juce-client/build/helper/audio-backuper-helper"
"$ROOT/juce-client/build/AudioBackuperTests_artefacts/Release/AudioBackuperTests" --screenshots "$OUT" "$(sed -n 2p "$WORK/cloud.txt")" "$WORK/Bounces"
