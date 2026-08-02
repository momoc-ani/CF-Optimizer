#ifndef MyVersion
  #define MyVersion "0.1.0"
#endif
#ifndef MyArch
  #define MyArch "amd64"
#endif
#ifndef SourceDir
  #define SourceDir "stage"
#endif
#ifndef OutputDir
  #define OutputDir "dist"
#endif

#if MyArch == "arm64"
  #define AllowedArchitecture "arm64"
#else
  #define AllowedArchitecture "x64compatible"
#endif

[Setup]
AppId={{4F32CB57-870E-4BBA-A75C-0A5E5FEAB48D}
AppName=CF Optimizer
AppVersion={#MyVersion}
AppPublisher=CF Optimizer Contributors
AppPublisherURL=https://github.com/momoc-ani/CF-Optimizer
DefaultDirName={autopf}\CF Optimizer
DefaultGroupName=CF Optimizer
ArchitecturesAllowed={#AllowedArchitecture}
ArchitecturesInstallIn64BitMode={#AllowedArchitecture}
PrivilegesRequired=admin
Compression=lzma2/max
SolidCompression=yes
WizardStyle=modern
SetupIconFile=..\wails\windows\icon.ico
LicenseFile=..\..\LICENSE
UninstallDisplayIcon={app}\cf-optimizer-ui.exe
OutputDir={#OutputDir}
OutputBaseFilename=cf-optimizer-{#MyVersion}-windows-{#MyArch}-setup
VersionInfoVersion={#MyVersion}
CloseApplications=yes
RestartApplications=no

[Files]
Source: "{#SourceDir}\cf-optimizer.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\cf-optimizerd.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\cf-optimizer-ui.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "..\..\LICENSE"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#SourceDir}\MicrosoftEdgeWebview2Setup.exe"; DestDir: "{tmp}"; Flags: deleteafterinstall skipifsourcedoesntexist

[Icons]
Name: "{autoprograms}\CF Optimizer"; Filename: "{app}\cf-optimizer-ui.exe"
Name: "{autodesktop}\CF Optimizer"; Filename: "{app}\cf-optimizer-ui.exe"; Tasks: desktopicon

[Tasks]
Name: "desktopicon"; Description: "Create a desktop shortcut"; GroupDescription: "Additional icons:"

[Run]
Filename: "{tmp}\MicrosoftEdgeWebview2Setup.exe"; Parameters: "/silent /install"; StatusMsg: "Installing Microsoft Edge WebView2 Runtime..."; Flags: waituntilterminated runhidden skipifdoesntexist; Check: not IsWebView2Installed
Filename: "{app}\cf-optimizer.exe"; Parameters: "--config ""{commonappdata}\CF Optimizer\config.yaml"" init"; StatusMsg: "Creating the initial configuration..."; Flags: waituntilterminated runhidden; Check: not FileExists(ExpandConstant('{commonappdata}\CF Optimizer\config.yaml'))
Filename: "{app}\cf-optimizer.exe"; Parameters: "--config ""{commonappdata}\CF Optimizer\config.yaml"" install --daemon ""{app}\cf-optimizerd.exe"""; StatusMsg: "Installing the CF Optimizer service..."; Flags: waituntilterminated runhidden; Check: IsFreshInstall
Filename: "{app}\cf-optimizer.exe"; Parameters: "--config ""{commonappdata}\CF Optimizer\config.yaml"" start"; StatusMsg: "Starting the CF Optimizer service..."; Flags: waituntilterminated runhidden; Check: IsServiceUpgrade
Filename: "{app}\cf-optimizer-ui.exe"; Description: "Launch CF Optimizer"; Flags: nowait postinstall skipifsilent

[Code]
const
  WebViewClientKey = 'Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}';

var
  ServiceExistedBeforeInstall: Boolean;

function IsServiceInstalled: Boolean;
var
  ExitCode: Integer;
begin
  Result := Exec(ExpandConstant('{sys}\sc.exe'), 'query CFOptimizer', '', SW_HIDE,
    ewWaitUntilTerminated, ExitCode) and (ExitCode = 0);
end;

function IsWebView2Installed: Boolean;
var
  Version: String;
begin
  Result := RegQueryStringValue(HKLM32, WebViewClientKey, 'pv', Version) or
    RegQueryStringValue(HKLM64, WebViewClientKey, 'pv', Version) or
    RegQueryStringValue(HKCU, WebViewClientKey, 'pv', Version);
end;

function IsFreshInstall: Boolean;
begin
  Result := not ServiceExistedBeforeInstall;
end;

function IsServiceUpgrade: Boolean;
begin
  Result := ServiceExistedBeforeInstall;
end;

function InitializeSetup: Boolean;
begin
  ServiceExistedBeforeInstall := IsServiceInstalled;
  Result := True;
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  ExitCode: Integer;
begin
  Result := '';
  if ServiceExistedBeforeInstall and FileExists(ExpandConstant('{app}\cf-optimizer.exe')) then
    if not Exec(ExpandConstant('{app}\cf-optimizer.exe'),
      '--config "' + ExpandConstant('{commonappdata}\CF Optimizer\config.yaml') + '" stop',
      '', SW_HIDE, ewWaitUntilTerminated, ExitCode) or (ExitCode <> 0) then
      Result := 'Unable to stop the existing CF Optimizer service.';
end;

procedure CurUninstallStepChanged(CurrentStep: TUninstallStep);
var
  ExitCode: Integer;
  ConfigPath: String;
  CliPath: String;
begin
  if CurrentStep <> usUninstall then
    exit;
  ConfigPath := ExpandConstant('{commonappdata}\CF Optimizer\config.yaml');
  CliPath := ExpandConstant('{app}\cf-optimizer.exe');
  if not FileExists(CliPath) then
    exit;
  if not Exec(CliPath, '--config "' + ConfigPath + '" uninstall', '', SW_HIDE,
    ewWaitUntilTerminated, ExitCode) or (ExitCode <> 0) then
    RaiseException('The CF Optimizer service or managed policy could not be removed. Uninstall was stopped to avoid leaving an unknown network state.');
end;
