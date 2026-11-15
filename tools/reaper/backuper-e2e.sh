#!/bin/bash
# End-to-end check of Audio Backuper in a real host.
#
# Starts a fake cloud, launches a separate REAPER instance with the plugin
# on the master track, and checks that the plugin starts the helper from its
# own bundle, that a render reaches the cloud, and that a render queued when
# the DAW is closed is still uploaded before the helper exits by itself.
#
# Nothing touches the real data directory, keychain or cloud account.
#
# Usage: tools/reaper/backuper-e2e.sh        (after building juce-client)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
REAPER="${SOUNDGIT_REAPER:-/Applications/REAPER.app/Contents/MacOS/REAPER}"
WORK="$(mktemp -d)"
DATA="$WORK/data"; RENDERS="$WORK/renders"
mkdir -p "$RENDERS"
CLOUD_PID=""; REAPER_PID=""

cleanup() {
  [ -n "$REAPER_PID" ] && kill "$REAPER_PID" 2>/dev/null || true
  [ -n "$CLOUD_PID" ] && kill "$CLOUD_PID" 2>/dev/null || true
  rm -rf "$WORK"
}
trap cleanup EXIT
fail() { echo "FAIL: $1"; [ -f "$DATA/helper.log" ] && sed 's/^/  helper.log: /' "$DATA/helper.log"; exit 1; }
nap() { perl -e "select(undef,undef,undef,$1)"; }
# wait_for <seconds> <description> <command...>
wait_for() {
  local limit=$1 what=$2; shift 2
  for _ in $(seq 1 $((limit * 5))); do "$@" >/dev/null 2>&1 && return 0; nap 0.2; done
  fail "timed out waiting for $what"
}
json() { python3 -I -c "import json,sys; d=json.load(sys.stdin); print($1)"; }

(cd "$ROOT/core" && go build -o "$WORK/fakecloud" ./internal/backuper/cloud/cloudtest/fakecloud)
"$WORK/fakecloud" > "$WORK/cloud.txt" &
CLOUD_PID=$!
cloud_ready() { [ "$(wc -l < "$WORK/cloud.txt")" -ge 2 ]; }
wait_for 10 "the fake cloud" cloud_ready
CLOUD_URL="$(sed -n 1p "$WORK/cloud.txt")"; CLOUD_TOKEN="$(sed -n 2p "$WORK/cloud.txt")"
in_cloud() { curl -s "$CLOUD_URL/__files" | json "[f for f in d if f['path']=='$1' and f['md5']=='$2'][0]"; }

export AUDIO_BACKUPER_DATA_DIR="$DATA" AUDIO_BACKUPER_SECRETS=file
export AUDIO_BACKUPER_YANDEX_API_URL="$CLOUD_URL" AUDIO_BACKUPER_IDLE_EXIT_SECONDS=4
export BACKUPER_E2E_LOG="$WORK/reaper.log"

"$REAPER" -newinst -nosplash -new "$ROOT/tools/reaper/backuper-e2e.lua" >/dev/null 2>&1 &
REAPER_PID=$!
wait_for 60 "REAPER to run the script" grep -q DONE "$WORK/reaper.log"
grep -q "fx index -1" "$WORK/reaper.log" && fail "REAPER did not find the plugin (is it built and installed?)"
echo "ok   plugin loaded on the master track: $(sed -n 's/^fx name //p' "$WORK/reaper.log")"

wait_for 20 "the helper started by the plugin" test -s "$DATA/helper.json"
PORT="$(json "d['port']" < "$DATA/helper.json")"; SECRET="$(json "d['secret']" < "$DATA/helper.json")"
HELPER_PID="$(json "d['pid']" < "$DATA/helper.json")"
api() { curl -s -m 5 -H "Authorization: Bearer $SECRET" "$@"; }
status() { api "http://127.0.0.1:$PORT/v1/status?client=e2e${WATCH:+&watch=$WATCH}"; }
post() { api -X POST -d "$2" "http://127.0.0.1:$PORT$1"; }
echo "ok   the plugin started the helper from its bundle (pid $HELPER_PID, parent $(ps -o ppid= -p "$HELPER_PID" | tr -d ' '))"

WATCH=""
wait_for 10 "the plugin to connect to the helper" sh -c "curl -s -m 5 -H 'Authorization: Bearer $SECRET' 'http://127.0.0.1:$PORT/v1/status' | python3 -I -c 'import json,sys; sys.exit(0 if json.load(sys.stdin)[\"clients\"]>=1 else 1)'"
echo "ok   the plugin is connected to the helper"
two() { api "http://127.0.0.1:$PORT/v1/status" | json "d['clients']>=2 or sys.exit(1)"; }
wait_for 15 "both plugin instances to connect" two
HELPERS="$(pgrep -f "audio-backuper-helper --data-dir $DATA" | wc -l | tr -d ' ')"
[ "$HELPERS" = "1" ] || fail "expected one helper for two plugin instances, found $HELPERS"
echo "ok   two plugin instances share one helper"

ACCOUNT="$(post /v1/login/token "{\"provider\":\"yandex\",\"token\":\"$CLOUD_TOKEN\"}" | json "d['accountId']")"
WATCH="$(post /v1/watch "{\"localPath\":\"$RENDERS\",\"accountId\":\"$ACCOUNT\",\"remoteFolder\":\"E2E\",\"recursive\":true}" | json "d['id']")"
status >/dev/null

printf 'first render' > "$RENDERS/mix #1.wav"
MD5_1="$(md5 -q "$RENDERS/mix #1.wav")"
first() { status >/dev/null; in_cloud "E2E/mix #1.wav" "$MD5_1"; }
wait_for 20 "the render in the cloud" first
echo "ok   a new render reached the cloud intact"

# The DAW is closed while a render is still waiting to be uploaded.
post /v1/pause '{"paused":true}' >/dev/null
printf 'rendered just before quitting' > "$RENDERS/last.wav"
MD5_2="$(md5 -q "$RENDERS/last.wav")"
queued() { status | json "d['watches'][0]['pending']==1 or sys.exit(1)"; }
wait_for 20 "the last render to be queued" queued
kill "$REAPER_PID"; wait "$REAPER_PID" 2>/dev/null || true; REAPER_PID=""
kill -0 "$HELPER_PID" 2>/dev/null || fail "the helper died together with REAPER"
post /v1/pause '{"paused":false}' >/dev/null
wait_for 20 "the queued render after REAPER was closed" in_cloud "E2E/last.wav" "$MD5_2"
echo "ok   a render queued when the DAW closed was still uploaded"

gone() { ! kill -0 "$HELPER_PID" 2>/dev/null; }
wait_for 40 "the helper to exit by itself" gone
test ! -e "$DATA/helper.json" || fail "helper.json was left behind"
echo "ok   the helper exited by itself once nobody needed it"
echo "PASS"
