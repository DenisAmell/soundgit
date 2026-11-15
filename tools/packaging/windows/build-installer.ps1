# Builds the Windows installer of Audio Backuper from an already built plugin.
#
#   tools\packaging\windows\build-installer.ps1 [-BuildDir juce-client\build] [-OutDir dist]
#
# Signing is switched on by the environment:
#   WINDOWS_SIGN_CERT      base64 of a .pfx code-signing certificate
#   WINDOWS_SIGN_PASSWORD  its password
# Without them nothing is signed and the file is named "-unsigned".
param(
    [string]$BuildDir = "juce-client\build",
    [string]$OutDir = "dist"
)
$ErrorActionPreference = "Stop"
$root = Resolve-Path (Join-Path $PSScriptRoot "..\..\..")
$bundle = Join-Path $root "$BuildDir\AudioBackuper_artefacts\Release\VST3\Audio Backuper.vst3"
$helper = Join-Path $bundle "Contents\Resources\audio-backuper-helper.exe"
$plugin = Join-Path $bundle "Contents\x86_64-win\Audio Backuper.vst3"
foreach ($f in @($helper, $plugin)) {
    if (-not (Test-Path $f)) { throw "not found: $f (build the plugin first)" }
}
$version = (& $helper --version).Trim()
Write-Host "version $version"

$out = Join-Path $root $OutDir
New-Item -ItemType Directory -Force $out | Out-Null

$suffix = "-unsigned"
$pfx = $null
$signtool = $null
if ($env:WINDOWS_SIGN_CERT) {
    $signtool = Get-ChildItem "${env:ProgramFiles(x86)}\Windows Kits\10\bin\*\x64\signtool.exe" |
                Sort-Object FullName -Descending | Select-Object -First 1 -ExpandProperty FullName
    if (-not $signtool) { throw "signtool.exe not found" }
    $pfx = Join-Path $env:TEMP "audio-backuper-sign.pfx"
    [IO.File]::WriteAllBytes($pfx, [Convert]::FromBase64String($env:WINDOWS_SIGN_CERT))
    $suffix = ""
}
function Sign([string]$file) {
    if (-not $pfx) { return }
    & $signtool sign /f $pfx /p $env:WINDOWS_SIGN_PASSWORD /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 $file
    if ($LASTEXITCODE -ne 0) { throw "signing failed: $file" }
}
Sign $helper
Sign $plugin

$iscc = "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe"
if (-not (Test-Path $iscc)) {
    Write-Host "Inno Setup not found, installing it"
    choco install innosetup -y --no-progress | Out-Null
}
if (-not (Test-Path $iscc)) { throw "Inno Setup (ISCC.exe) is not available" }

$name = "AudioBackuper-$version-Windows-x64$suffix"
$env:AB_VERSION = $version
$env:AB_VST3_DIR = $bundle
$env:AB_OUTPUT_NAME = $name
$env:AB_OUTPUT_DIR = $out
& $iscc /Q (Join-Path $PSScriptRoot "audio-backuper.iss")
if ($LASTEXITCODE -ne 0) { throw "Inno Setup failed" }

$installer = Join-Path $out "$name.exe"
Sign $installer
if ($pfx) { Remove-Item $pfx -Force }
Write-Host "$installer ($([math]::Round((Get-Item $installer).Length / 1MB, 1)) MB)"
if ($env:GITHUB_OUTPUT) {
    "installer=$installer" | Out-File -Append -Encoding utf8 $env:GITHUB_OUTPUT
    "version=$version" | Out-File -Append -Encoding utf8 $env:GITHUB_OUTPUT
}
