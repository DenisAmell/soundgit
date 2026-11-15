# Checks a built installer by using it: silent install, the helper runs,
# an update over a running helper works, silent uninstall leaves no plugin
# behind and does not touch the user's data.
#
#   tools\packaging\windows\verify-installer.ps1 -Installer <path> -Version <x.y.z>
param(
    [Parameter(Mandatory)][string]$Installer,
    [Parameter(Mandatory)][string]$Version
)
$ErrorActionPreference = "Stop"
function Fail([string]$msg) { Write-Host "FAIL: $msg"; exit 1 }
function Install {
    $p = Start-Process $Installer -ArgumentList "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART" -Wait -PassThru
    if ($p.ExitCode -ne 0) { Fail "the installer exited with $($p.ExitCode)" }
}

$bundle = Join-Path $env:CommonProgramFiles "VST3\Audio Backuper.vst3"
$helper = Join-Path $bundle "Contents\Resources\audio-backuper-helper.exe"
$plugin = Join-Path $bundle "Contents\x86_64-win\Audio Backuper.vst3"

Install
foreach ($f in @($helper, $plugin)) { if (-not (Test-Path $f)) { Fail "not installed: $f" } }
$got = (& $helper --version).Trim()
if ($got -ne $Version) { Fail "the installed helper reports $got, expected $Version" }
Write-Host "ok   installed into the standard VST3 folder; the helper runs and reports $Version"

# A helper is busy when the next version is installed. It must not be the
# real one: a scratch data directory and no system secret storage.
$data = Join-Path $env:TEMP "audio-backuper-verify-$PID"
$env:AUDIO_BACKUPER_SECRETS = "file"
& $helper --detach --data-dir $data
$info = Join-Path $data "helper.json"
for ($i = 0; $i -lt 50 -and -not (Test-Path $info); $i++) { Start-Sleep -Milliseconds 200 }
if (-not (Test-Path $info)) { Fail "the installed helper did not start" }
$helperPid = (Get-Content $info | ConvertFrom-Json).pid
if (-not (Get-Process -Id $helperPid -ErrorAction SilentlyContinue)) { Fail "the helper is not running" }
Write-Host "ok   the installed helper starts in the background (pid $helperPid)"

Install
if (Get-Process -Id $helperPid -ErrorAction SilentlyContinue) { Fail "the old helper survived the update" }
if (-not (Test-Path $helper)) { Fail "the helper is missing after the update" }
if (-not (Test-Path (Join-Path $data "helper.log"))) { Fail "the update removed the helper's data" }
Write-Host "ok   an update over a running helper succeeds and keeps its data"

$uninstaller = Get-ChildItem (Join-Path $env:ProgramFiles "Audio Backuper") -Filter "unins*.exe" | Select-Object -First 1
if (-not $uninstaller) { Fail "no uninstaller was registered" }
$p = Start-Process $uninstaller.FullName -ArgumentList "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART" -Wait -PassThru
if ($p.ExitCode -ne 0) { Fail "the uninstaller exited with $($p.ExitCode)" }
for ($i = 0; $i -lt 25 -and (Test-Path $bundle); $i++) { Start-Sleep -Milliseconds 200 }
if (Test-Path $bundle) { Fail "uninstall left the plugin behind" }
if (-not (Test-Path $data)) { Fail "uninstall removed the user's data" }
Remove-Item $data -Recurse -Force
Write-Host "ok   uninstall removes the plugin and leaves the user's data alone"
Write-Host "PASS  $Installer"
