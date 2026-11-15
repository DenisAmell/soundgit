-- Builds the Reaper test corpus for SoundGit.
--
-- Run in a fresh REAPER instance (see tools/reaper/README.md). The script
-- builds one project step by step and saves a copy after every step, so
-- consecutive files differ by exactly one kind of edit. The last steps
-- probe how identifiers behave on duplicate, rename, move and "save as".
--
-- Output directory: environment variable SOUNDGIT_CORPUS_OUT.

local out = os.getenv("SOUNDGIT_CORPUS_OUT")
if not out or out == "" then
  reaper.ShowConsoleMsg("SOUNDGIT_CORPUS_OUT is not set\n")
  return
end

local logf = assert(io.open(out .. "/make-corpus.log", "w"))
local function log(msg)
  logf:write(msg .. "\n")
  logf:flush()
end

local function fail(err)
  log("ERROR: " .. tostring(err))
  log(debug.traceback())
end

-- A short mono 16-bit sine, so items have real audio to point at.
local function write_wav(path, freq, seconds)
  local rate, n = 44100, math.floor(44100 * seconds)
  local f = assert(io.open(path, "wb"))
  f:write("RIFF", string.pack("<I4", 36 + n * 2), "WAVE")
  f:write("fmt ", string.pack("<I4I2I2I4I4I2I2", 16, 1, 1, rate, rate * 2, 2, 16))
  f:write("data", string.pack("<I4", n * 2))
  local buf = {}
  for i = 0, n - 1 do
    buf[#buf + 1] = string.pack("<i2", math.floor(12000 * math.sin(2 * math.pi * freq * i / rate)))
  end
  f:write(table.concat(buf))
  f:close()
end

local scale
local step = 0
local function save(name)
  step = step + 1
  local file = string.format("%s/%02d-%s.rpp", out, step, name)
  -- 8: a real "Save as". Saving a copy instead (0) writes absolute media paths.
  reaper.Main_SaveProjectEx(0, file, 8)
  log("saved " .. file)
end

local function track(idx, name)
  reaper.InsertTrackAtIndex(idx, true)
  local tr = reaper.GetTrack(0, idx)
  reaper.GetSetMediaTrackInfo_String(tr, "P_NAME", name, true)
  return tr
end

local function only_track(tr)
  reaper.SetOnlyTrackSelected(tr)
end

local function audio_item(tr, file, pos)
  local item = reaper.AddMediaItemToTrack(tr)
  local take = reaper.AddTakeToMediaItem(item)
  local src = reaper.PCM_Source_CreateFromFile(file)
  reaper.SetMediaItemTake_Source(take, src)
  local len = reaper.GetMediaSourceLength(src)
  reaper.SetMediaItemInfo_Value(item, "D_POSITION", pos)
  reaper.SetMediaItemInfo_Value(item, "D_LENGTH", len)
  reaper.GetSetMediaItemTakeInfo_String(take, "P_NAME", file:match("[^/]+$"), true)
  return item
end

local function main()
  log("REAPER " .. reaper.GetAppVersion())
  reaper.RecursiveCreateDirectory(out .. "/Media", 0)
  write_wav(out .. "/Media/sine 440.wav", 440, 1.0)
  write_wav(out .. "/Media/sine 660.wav", 660, 0.5)

  save("empty")

  local bass = track(0, "Bass")
  save("track")

  reaper.SetMediaTrackInfo_Value(bass, "D_VOL", 0.5)
  reaper.SetMediaTrackInfo_Value(bass, "D_PAN", -0.25)
  save("track-volume-pan")

  reaper.SetMediaTrackInfo_Value(bass, "B_MUTE", 1)
  reaper.SetMediaTrackInfo_Value(bass, "B_PHASE", 1)
  save("track-mute-phase")
  reaper.SetMediaTrackInfo_Value(bass, "B_MUTE", 0)

  local drums = track(1, "Drums bus")
  local kick = track(2, "Kick \"in\"")
  local snare = track(3, "Snare 'top' \"x\"")
  reaper.SetMediaTrackInfo_Value(drums, "I_FOLDERDEPTH", 1)
  reaper.SetMediaTrackInfo_Value(snare, "I_FOLDERDEPTH", -1)
  save("folders")

  local item = audio_item(kick, out .. "/Media/sine 440.wav", 0)
  save("audio-item")

  reaper.SetMediaItemInfo_Value(item, "D_POSITION", 1.5)
  reaper.SetMediaItemInfo_Value(item, "D_FADEINLEN", 0.05)
  reaper.SetMediaItemInfo_Value(item, "D_FADEOUTLEN", 0.1)
  save("audio-item-moved")

  local take2 = reaper.AddTakeToMediaItem(item)
  reaper.SetMediaItemTake_Source(take2, reaper.PCM_Source_CreateFromFile(out .. "/Media/sine 660.wav"))
  reaper.GetSetMediaItemTakeInfo_String(take2, "P_NAME", "second take", true)
  reaper.SetActiveTake(take2)
  save("takes")

  local keys = track(4, "Keys")
  local midi = reaper.CreateNewMIDIItemInProj(keys, 2, 4, false)
  local mtake = reaper.GetActiveTake(midi)
  reaper.MIDI_InsertNote(mtake, false, false, 0, 480, 0, 60, 96, true)
  reaper.MIDI_InsertNote(mtake, false, false, 480, 960, 0, 64, 96, true)
  reaper.MIDI_InsertNote(mtake, false, false, 960, 1920, 0, 67, 80, true)
  reaper.MIDI_Sort(mtake)
  save("midi-item")

  reaper.MIDI_SetNote(mtake, 1, nil, nil, nil, 720, nil, 65, nil, true)
  reaper.MIDI_Sort(mtake)
  save("midi-note-edited")

  local eq = reaper.TrackFX_AddByName(bass, "ReaEQ", false, -1)
  log("ReaEQ index " .. tostring(eq))
  local js = reaper.TrackFX_AddByName(bass, "JS: Volume Adjustment", false, -1)
  if js < 0 then js = reaper.TrackFX_AddByName(bass, "JS: utility/volume", false, -1) end
  log("JS index " .. tostring(js))
  save("fx")

  if eq >= 0 then reaper.TrackFX_SetParamNormalized(bass, eq, 1, 0.8) end
  save("fx-param")

  if js >= 0 then reaper.TrackFX_SetEnabled(bass, js, false) end
  save("fx-bypass")

  if eq >= 0 and js >= 0 then reaper.TrackFX_CopyToTrack(bass, js, bass, eq, true) end
  save("fx-reordered")

  only_track(bass)
  reaper.Main_OnCommand(40406, 0) -- Track: Toggle track volume envelope visible
  local env = reaper.GetTrackEnvelopeByName(bass, "Volume")
  log("volume envelope " .. tostring(env))
  if env then
    -- Envelope values are stored in the envelope's own scale, not as gain.
    scale = function(gain) return reaper.ScaleToEnvelopeMode(reaper.GetEnvelopeScalingMode(env), gain) end
    reaper.DeleteEnvelopePointRange(env, -1, 1e9)
    reaper.InsertEnvelopePoint(env, 0, scale(1), 0, 0, false, true)
    reaper.InsertEnvelopePoint(env, 2, scale(0.5), 0, 0, false, true)
    reaper.InsertEnvelopePoint(env, 4, scale(1), 0, 0, false, true)
    reaper.Envelope_SortPoints(env)
  end
  save("envelope")

  if env then
    reaper.SetEnvelopePoint(env, 1, 2, scale(0.25), 0, 0, false, true)
    reaper.InsertEnvelopePoint(env, 3, scale(0.8), 0, 0, false, true)
    reaper.Envelope_SortPoints(env)
  end
  save("envelope-edited")

  reaper.SetCurrentBPM(0, 128, false)
  save("tempo")

  reaper.SetTempoTimeSigMarker(0, -1, 0, -1, -1, 128, 4, 4, false)
  reaper.SetTempoTimeSigMarker(0, -1, 8, -1, -1, 140, 3, 4, false)
  reaper.UpdateTimeline()
  save("tempo-map")

  reaper.AddProjectMarker2(0, false, 2, 0, "Verse", 1, 0)
  reaper.AddProjectMarker2(0, true, 8, 16, "Chorus `big`", 2, 0)
  save("markers")

  local reverb = track(5, "Reverb")
  reaper.CreateTrackSend(kick, reverb)
  local s = reaper.CreateTrackSend(keys, reverb)
  reaper.SetTrackSendInfo_Value(reverb, -1, s, "D_VOL", 0.5)
  save("sends")

  -- Editor state only: nothing musical changes between these two files.
  reaper.SetEditCurPos(12, true, false)
  reaper.SetOnlyTrackSelected(keys)
  save("view-state-only")

  -- Saving again with no edits at all.
  save("resave")

  -- Identifier behaviour ----------------------------------------------
  only_track(bass)
  reaper.Main_OnCommand(40062, 0) -- Track: Duplicate tracks
  save("guid-duplicate-track")

  reaper.Main_OnCommand(40289, 0) -- Item: Unselect all items
  reaper.SetMediaItemSelected(item, true)
  reaper.Main_OnCommand(41295, 0) -- Item: Duplicate items
  save("guid-duplicate-item")

  reaper.GetSetMediaTrackInfo_String(bass, "P_NAME", "Sub Bass", true)
  only_track(bass)
  reaper.ReorderSelectedTracks(reaper.CountTracks(0), 0) -- move to the end
  save("guid-rename-move")

  -- Copy a track into another project: do identifiers travel with it?
  only_track(bass)
  reaper.Main_OnCommand(40210, 0) -- Track: Copy tracks
  reaper.Main_OnCommand(40859, 0) -- New project tab
  reaper.Main_OnCommand(42398, 0) -- Item: Paste items/tracks
  reaper.RecursiveCreateDirectory(out .. "/other", 0)
  reaper.Main_SaveProjectEx(0, out .. "/other/pasted-track.rpp", 8)
  log("saved other/pasted-track.rpp")

  log("DONE")
end

reaper.PreventUIRefresh(1)
local ok, err = xpcall(main, fail)
reaper.PreventUIRefresh(-1)
if not ok then log("FAILED") end
logf:close()
