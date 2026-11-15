-- Puts Audio Backuper on the master track and opens its window, the way a
-- user would. Used by backuper-e2e.sh; progress goes to $BACKUPER_E2E_LOG.
local log = io.open(os.getenv("BACKUPER_E2E_LOG") or "/dev/null", "w")
local function say(msg) log:write(msg .. "\n"); log:flush() end

local master = reaper.GetMasterTrack(0)
local fx = reaper.TrackFX_AddByName(master, "Audio Backuper", false, -1)
say("fx index " .. tostring(fx))
if fx >= 0 then
  local _, name = reaper.TrackFX_GetFXName(master, fx, "")
  say("fx name " .. tostring(name))
  reaper.TrackFX_Show(master, fx, 3) -- floating window: exercises the plugin interface
  say("window shown")
end
-- A second instance on an ordinary track: both must share one helper.
reaper.InsertTrackAtIndex(0, true)
local second = reaper.TrackFX_AddByName(reaper.GetTrack(0, 0), "Audio Backuper", false, -1)
say("second instance index " .. tostring(second))
say("DONE")
