; ============================================================================
; opidcs 桌面客户端 · Inno Setup 安装包
;
; 前置构建：
;   cd tools/opidcs-gui
;   npm ci && npm run icon
;   cargo build --release            ; 产出 target/release/opidcs.exe
;   go build -o src-tauri/binaries/opidcs-x86_64-pc-windows-msvc.exe ./cmd/opidcs
;
; 编译：
;   iscc packaging/windows/opidcs.iss
; ============================================================================

#define AppName "opidcs"
#define AppVersion "1.0.0"
#define AppPublisher "OpenIDCS Team"
#define AppURL "https://github.com/OpenIDCSTeam/CloudHost"
#define GuiExe "..\..\tools\opidcs-gui\src-tauri\target\release\opidcs.exe"
#define CliExe "..\..\tools\opidcs-gui\src-tauri\binaries\opidcs-x86_64-pc-windows-msvc.exe"

[Setup]
AppId={{6C1F0B52-9F3A-4B7E-9A21-OPENIDCS0001}
AppName={#AppName}
AppVersion={#AppVersion}
AppPublisher={#AppPublisher}
AppPublisherURL={#AppURL}
AppSupportURL={#AppURL}
AppUpdatesURL={#AppURL}
DefaultDirName={autopf}\OpenIDCS\opidcs
DefaultGroupName=OpenIDCS
DisableProgramGroupPage=yes
OutputDir=..\..\BuildCache\windows
OutputBaseFilename=opidcs-{#AppVersion}-win-x64-setup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=admin
UninstallDisplayIcon={app}\opidcs.exe

[Languages]
Name: "chinesesimp"; MessagesFile: "compiler:Default.isl"

[Tasks]
Name: "desktopicon"; Description: "创建桌面快捷方式"; GroupDescription: "附加任务:"; Flags: unchecked
Name: "addtopath"; Description: "将 opidcs CLI 加入系统 PATH"; GroupDescription: "命令行:"; Flags: checkedonce

[Files]
Source: "{#GuiExe}"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#CliExe}"; DestDir: "{app}"; DestName: "opidcs.exe"; Flags: ignoreversion

[Icons]
Name: "{group}\opidcs"; Filename: "{app}\opidcs.exe"
Name: "{group}\卸载 opidcs"; Filename: "{uninstallexe}"
Name: "{autodesktop}\opidcs"; Filename: "{app}\opidcs.exe"; Tasks: desktopicon

[Registry]
Root: HKLM; Subkey: "SYSTEM\CurrentControlSet\Control\Session Manager\Environment"; \
  ValueType: expandsz; ValueName: "Path"; ValueData: "{olddata};{app}"; \
  Tasks: addtopath; Check: NeedsAddPath('{app}')

[Run]
Filename: "{app}\opidcs.exe"; Description: "启动 opidcs"; Flags: nowait postinstall skipifsilent

[Code]
function NeedsAddPath(Param: string): boolean;
var
  OrigPath: string;
begin
  if not RegQueryStringValue(HKLM, 'SYSTEM\CurrentControlSet\Control\Session Manager\Environment', 'Path', OrigPath) then
  begin
    Result := True;
    exit;
  end;
  Result := Pos(';' + Param + ';', ';' + OrigPath + ';') = 0;
end.

[UninstallDelete]
Type: filesandordirs; Name: "{app}"
