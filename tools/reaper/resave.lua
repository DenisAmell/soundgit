-- Saves the project REAPER was started with under a new name, so a file
-- written by SoundGit can be compared with what REAPER makes of it.
-- Output file: environment variable SOUNDGIT_RESAVE_OUT.
local out = os.getenv("SOUNDGIT_RESAVE_OUT")
if out and out ~= "" then
  reaper.Main_SaveProjectEx(0, out, 8)
  local f = io.open(out .. ".done", "w")
  if f then f:close() end
end
