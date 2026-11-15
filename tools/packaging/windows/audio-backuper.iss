; Installer of Audio Backuper for Windows. Built by build-installer.ps1,
; which passes the version, the plugin bundle and the output name.

#define AppVersion GetEnv("AB_VERSION")
#define SourceBundle GetEnv("AB_VST3_DIR")
#define OutputName GetEnv("AB_OUTPUT_NAME")
#define OutputFolder GetEnv("AB_OUTPUT_DIR")

[Setup]
AppId={{6D0C8B1E-5A0F-4C57-9A53-2F2E7C1B9A41}
AppName=Audio Backuper
AppVersion={#AppVersion}
AppPublisher=rozetki
; The standard place where every DAW looks for VST3 plugins.
DefaultDirName={commoncf64}\VST3\Audio Backuper.vst3
DisableDirPage=yes
DisableProgramGroupPage=yes
DisableReadyPage=yes
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=admin
OutputDir={#OutputFolder}
OutputBaseFilename={#OutputName}
Compression=lzma2
SolidCompression=yes
; Keep the uninstaller out of the plugin folder: DAWs scan it.
UninstallFilesDir={commonpf64}\Audio Backuper
UninstallDisplayName=Audio Backuper
CloseApplications=no
WizardStyle=modern

[Languages]
Name: "en"; MessagesFile: "compiler:Default.isl"
Name: "ru"; MessagesFile: "compiler:Languages\Russian.isl"

[Files]
Source: "{#SourceBundle}\*"; DestDir: "{app}"; Flags: recursesubdirs createallsubdirs ignoreversion

[UninstallDelete]
Type: filesandordirs; Name: "{app}"

[Code]
// Windows keeps a running program's file locked, so the helper that lives
// inside the plugin has to stop before its file can be replaced or removed.
// Its upload queue is on disk; the plugin starts the helper again when it
// is next loaded.
procedure StopHelper;
var
  ResultCode: Integer;
begin
  Exec(ExpandConstant('{sys}\taskkill.exe'), '/F /IM audio-backuper-helper.exe', '',
       SW_HIDE, ewWaitUntilTerminated, ResultCode);
  Sleep(500);
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
begin
  StopHelper;
  Result := '';
end;

function InitializeUninstall(): Boolean;
begin
  StopHelper;
  Result := True;
end;
