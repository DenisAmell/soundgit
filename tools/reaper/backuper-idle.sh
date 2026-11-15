#!/bin/bash
# Measures what the helper costs while it has nothing to do: a bound folder
# with many files, a client polling like an open plugin, no changes.
# Usage: tools/reaper/backuper-idle.sh [files=3000] [seconds=30]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
FILES="${1:-3000}"; SECONDS_TO_MEASURE="${2:-30}"
WORK="$(mktemp -d)"; mkdir -p "$WORK/renders/sub"
CLOUD_PID=""; PORT=""; SECRET=""
nap() { perl -e "select(undef,undef,undef,$1)"; }
json() { python3 -I -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
api() { curl -s -m 5 -H "Authorization: Bearer $SECRET" "$@"; }
cleanup() {
  [ -n "$PORT" ] && api -X POST "http://127.0.0.1:$PORT/v1/shutdown" >/dev/null 2>&1 || true
  [ -n "$CLOUD_PID" ] && kill "$CLOUD_PID" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT

for i in $(seq 1 $((FILES / 2))); do : > "$WORK/renders/take$i.wav"; : > "$WORK/renders/sub/stem$i.wav"; done
(cd "$ROOT/core" && go build -o "$WORK/fakecloud" ./internal/backuper/cloud/cloudtest/fakecloud \
                 && go build -o "$WORK/helper" ./cmd/audio-backuper-helper)
"$WORK/fakecloud" > "$WORK/cloud.txt" & CLOUD_PID=$!
for _ in $(seq 1 50); do [ "$(wc -l < "$WORK/cloud.txt")" -ge 2 ] && break; nap 0.1; done
[ "$(wc -l < "$WORK/cloud.txt")" -ge 2 ] || { echo "the fake cloud did not start"; exit 1; }

export AUDIO_BACKUPER_DATA_DIR="$WORK/data" AUDIO_BACKUPER_SECRETS=file
export AUDIO_BACKUPER_YANDEX_API_URL="$(sed -n 1p "$WORK/cloud.txt")"
"$WORK/helper" --detach
for _ in $(seq 1 50); do [ -s "$WORK/data/helper.json" ] && break; nap 0.1; done
PORT="$(json "d['port']" < "$WORK/data/helper.json")"; SECRET="$(json "d['secret']" < "$WORK/data/helper.json")"
PID="$(json "d['pid']" < "$WORK/data/helper.json")"

ACCOUNT="$(api -X POST -d "{\"provider\":\"yandex\",\"token\":\"$(sed -n 2p "$WORK/cloud.txt")\"}" "http://127.0.0.1:$PORT/v1/login/token" | json "d['accountId']")"
WATCH="$(api -X POST -d "{\"localPath\":\"$WORK/renders\",\"accountId\":\"$ACCOUNT\",\"remoteFolder\":\"Idle\",\"recursive\":true}" "http://127.0.0.1:$PORT/v1/watch" | json "d['id']")"
poll() { api "http://127.0.0.1:$PORT/v1/status?client=idle&watch=$WATCH"; }
poll | json "'watching %d files, %d queued' % (d['watches'][0]['known'], d['watches'][0]['pending'])"

cpu() { ps -o cputime= -p "$PID" | awk -F: '{ s = 0; for (i = 1; i <= NF; i++) s = s * 60 + $i; print s }'; }
nap 3
C0="$(cpu)"; T0="$(date +%s)"
while [ $(( $(date +%s) - T0 )) -lt "$SECONDS_TO_MEASURE" ]; do poll >/dev/null; nap 0.7; done
C1="$(cpu)"; T1="$(date +%s)"
python3 -I -c "
import sys
c, t = float(sys.argv[1]) - float(sys.argv[2]), int(sys.argv[3]) - int(sys.argv[4])
print('CPU: %.2f s over %d s = %.2f%% of one core' % (c, t, 100 * c / t))" "$C1" "$C0" "$T1" "$T0"
echo "memory: $(ps -o rss= -p "$PID" | awk '{printf "%.0f MB", $1/1024}')"
