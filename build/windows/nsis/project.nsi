; Reviewed per-user installer. App data and OS credentials are never removed.
Unicode true
!include "MUI2.nsh"
!include "x64.nsh"
!include "WinVer.nsh"
!ifndef APP_VERSION
  !error "Use npm run package to supply the checked application version"
!endif
!ifndef APP_BINARY
  !error "A source-bound native executable is required"
!endif
!ifndef OUTPUT_FILE
  !error "A versioned artifact destination is required"
!endif
!ifdef INSTALLER_TEST
  ; Used only by the regression test. Never emitted by npm run package.
  !define APP_REG "Software\Microsoft\Windows\CurrentVersion\Uninstall\WhatsUpInstallerQA"
  !define SHORTCUT_NAME "WhatsUpInstallerQA"
!else
  !define APP_REG "Software\Microsoft\Windows\CurrentVersion\Uninstall\WhatsUp"
  !define SHORTCUT_NAME "WhatsUp"
!endif
Name "WhatsUp ${APP_VERSION}"
OutFile "${OUTPUT_FILE}"
InstallDir "$LOCALAPPDATA\Programs\WhatsUp"
InstallDirRegKey HKCU "${APP_REG}" "InstallLocation"
RequestExecutionLevel user
ManifestDPIAware true
SetCompressor /SOLID lzma
VIProductVersion "${APP_VERSION}.0"
VIAddVersionKey "ProductName" "WhatsUp"
VIAddVersionKey "FileDescription" "WhatsUp installer (not code-signed)"
VIAddVersionKey "FileVersion" "${APP_VERSION}"
VIAddVersionKey "LegalCopyright" "Copyright (c) 2026 bigsk1"
!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "..\..\..\LICENSE"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

!macro CheckNotRunning
  IfFileExists "$INSTDIR\whatsup.exe" 0 not_running
  System::Call 'kernel32::CreateFileW(w "$INSTDIR\whatsup.exe", i 0x40000000, i 0, p 0, i 3, i 0, p 0) p.r0'
  IntCmp $0 -1 running not_running_handle not_running_handle
  running:
    MessageBox MB_OK|MB_ICONEXCLAMATION "Quit WhatsUp from its tray menu before installing, upgrading or uninstalling. Your data will be kept." /SD IDOK
    SetErrorLevel 2
    Abort
  not_running_handle:
    System::Call 'kernel32::CloseHandle(p r0)'
  not_running:
!macroend

Function .onInit
  SetShellVarContext current
  SetRegView 64
  ${IfNot} ${AtLeastWin10}
    MessageBox MB_OK|MB_ICONSTOP "WhatsUp requires Windows 10 or later." /SD IDOK
    SetErrorLevel 3
    Abort
  ${EndIf}
  ${IfNot} ${IsNativeAMD64}
    MessageBox MB_OK|MB_ICONSTOP "This installer requires x64 Windows." /SD IDOK
    SetErrorLevel 3
    Abort
  ${EndIf}
  ; System runtime is shared. Never fetch an unpinned runtime during install.
  ReadRegStr $0 HKLM "Software\WOW6432Node\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ReadRegStr $1 HKCU "Software\Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}" "pv"
  ${If} $0 == "0.0.0.0"
    StrCpy $0 ""
  ${EndIf}
  ${If} $1 == "0.0.0.0"
    StrCpy $1 ""
  ${EndIf}
  ${If} $0 == ""
  ${AndIf} $1 == ""
    MessageBox MB_OK|MB_ICONSTOP "Install the Microsoft Edge WebView2 Evergreen Runtime from microsoft.com, then run this installer again. See WhatsUp's README for the official link." /SD IDOK
    SetErrorLevel 4
    Abort
  ${EndIf}
  !insertmacro CheckNotRunning
FunctionEnd

Section "WhatsUp" Main
  SetShellVarContext current
  SetRegView 64
  !insertmacro CheckNotRunning
  SetOutPath "$INSTDIR"
  SetOverwrite on
  File /oname=whatsup.exe "${APP_BINARY}"
  File /oname=LICENSE.txt "..\..\..\LICENSE"
  File /oname=THIRD_PARTY_NOTICES.txt "..\..\..\THIRD_PARTY_NOTICES.md"
  SetOutPath "$INSTDIR\third_party\licenses"
  File "..\..\..\third_party\licenses\wails-MIT.txt"
  SetOutPath "$INSTDIR\third_party\licenses"
  File "..\..\..\third_party\licenses\tink-Apache-2.0.txt"
  SetOutPath "$INSTDIR\third_party\licenses"
  File "..\..\..\third_party\licenses\phosphor-MIT.txt"
  SetOutPath "$INSTDIR\third_party\licenses"
  File "..\..\..\third_party\licenses\pixelarticons-MIT.txt"
  SetOutPath "$INSTDIR\third_party\licenses"
  File "..\..\..\third_party\licenses\pion-MIT.txt"
  SetOutPath "$INSTDIR\third_party\licenses"
  File "..\..\..\third_party\licenses\pion-transport-xor-BSD-3-Clause.txt"
  SetOutPath "$INSTDIR\third_party\licenses\fonts"
  File "..\..\..\third_party\licenses\fonts\jetbrains-mono-OFL-1.1.txt"
  SetOutPath "$INSTDIR\third_party\licenses\fonts"
  File "..\..\..\third_party\licenses\fonts\nunito-OFL-1.1.txt"
  SetOutPath "$INSTDIR\third_party\licenses\fonts"
  File "..\..\..\third_party\licenses\fonts\orbitron-OFL-1.1.txt"
  SetOutPath "$INSTDIR\third_party\licenses\fonts"
  File "..\..\..\third_party\licenses\fonts\press-start-2p-OFL-1.1.txt"
  SetOutPath "$INSTDIR\third_party\licenses\fonts"
  File "..\..\..\third_party\licenses\fonts\rajdhani-OFL-1.1.txt"
  SetOutPath "$INSTDIR\third_party\licenses\fonts"
  File "..\..\..\third_party\licenses\fonts\vt323-OFL-1.1.txt"
  SetOutPath "$INSTDIR\third_party\tailcat"
  File "..\..\..\third_party\tailcat\LICENSE"
  SetOutPath "$INSTDIR\third_party\ar-writer"
  File "..\..\..\third_party\ar-writer\COPYING"
  SetOutPath "$INSTDIR"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  CreateShortcut "$SMPROGRAMS\${SHORTCUT_NAME}.lnk" "$INSTDIR\whatsup.exe"
  !ifndef INSTALLER_TEST
  Delete "$SMPROGRAMS\WhatsUp (Development).lnk"
  Delete "$DESKTOP\WhatsUp (Development).lnk"
  !endif
  WriteRegStr HKCU "${APP_REG}" "DisplayName" "WhatsUp"
  WriteRegStr HKCU "${APP_REG}" "DisplayVersion" "${APP_VERSION}"
  WriteRegStr HKCU "${APP_REG}" "Publisher" "bigsk1"
  WriteRegStr HKCU "${APP_REG}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${APP_REG}" "DisplayIcon" "$INSTDIR\whatsup.exe,0"
  WriteRegStr HKCU "${APP_REG}" "UninstallString" '$\"$INSTDIR\uninstall.exe$\"'
  WriteRegStr HKCU "${APP_REG}" "QuietUninstallString" '$\"$INSTDIR\uninstall.exe$\" /S'
  WriteRegDWORD HKCU "${APP_REG}" "NoModify" 1
  WriteRegDWORD HKCU "${APP_REG}" "NoRepair" 1
SectionEnd

Function un.onInit
  SetShellVarContext current
  SetRegView 64
  !insertmacro CheckNotRunning
FunctionEnd

Section "Uninstall"
  ; Remove only installer-owned files. Keep profile, WebView, history and keys.
  SetShellVarContext current
  SetRegView 64
  Delete "$INSTDIR\whatsup.exe"
  Delete "$INSTDIR\LICENSE.txt"
  Delete "$INSTDIR\THIRD_PARTY_NOTICES.txt"
  Delete "$INSTDIR\third_party\licenses\wails-MIT.txt"
  Delete "$INSTDIR\third_party\licenses\tink-Apache-2.0.txt"
  Delete "$INSTDIR\third_party\licenses\phosphor-MIT.txt"
  Delete "$INSTDIR\third_party\licenses\pixelarticons-MIT.txt"
  Delete "$INSTDIR\third_party\licenses\pion-MIT.txt"
  Delete "$INSTDIR\third_party\licenses\pion-transport-xor-BSD-3-Clause.txt"
  Delete "$INSTDIR\third_party\licenses\fonts\jetbrains-mono-OFL-1.1.txt"
  Delete "$INSTDIR\third_party\licenses\fonts\nunito-OFL-1.1.txt"
  Delete "$INSTDIR\third_party\licenses\fonts\orbitron-OFL-1.1.txt"
  Delete "$INSTDIR\third_party\licenses\fonts\press-start-2p-OFL-1.1.txt"
  Delete "$INSTDIR\third_party\licenses\fonts\rajdhani-OFL-1.1.txt"
  Delete "$INSTDIR\third_party\licenses\fonts\vt323-OFL-1.1.txt"
  Delete "$INSTDIR\third_party\tailcat\LICENSE"
  Delete "$INSTDIR\third_party\ar-writer\COPYING"
  RMDir "$INSTDIR\third_party\licenses\fonts"
  RMDir "$INSTDIR\third_party\licenses"
  RMDir "$INSTDIR\third_party\tailcat"
  RMDir "$INSTDIR\third_party\ar-writer"
  RMDir "$INSTDIR\third_party"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\${SHORTCUT_NAME}.lnk"
  !ifndef INSTALLER_TEST
  ReadRegStr $0 HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "WhatsUp"
  ${If} $0 == '$\"$INSTDIR\whatsup.exe$\" --autostart'
    DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "WhatsUp"
  ${EndIf}
  !endif
  DeleteRegKey HKCU "${APP_REG}"
SectionEnd
