; AirMock Windows installer.
;
; Expects to be built from this same directory (build/windows) with two
; files already placed alongside it by the release pipeline:
;   airmock.exe   - console binary (used by "airmock" on PATH / CLI use)
;   airmockw.exe  - windowsgui binary (used by the Start Menu shortcut, no
;                   console window flashes when launched)
;
; Build with:  makensis airmock.nsi
;
; Only stock NSIS (the "nsis" apt/choco package) is required — no
; third-party plugins, no macro-library string helpers whose exact syntax
; would need a real Windows/makensis run to verify. PATH editing sticks to
; core instructions only (ReadRegStr/WriteRegExpandStr/StrCpy/SendMessage):
; install unconditionally appends $INSTDIR (a harmless duplicate entry if
; ever reinstalled over itself without uninstalling first); uninstall
; intentionally leaves PATH untouched rather than risk a broken substring
; removal — a stale PATH entry pointing at a now-missing folder is a
; cosmetic no-op, not a functional problem.

!include "MUI2.nsh"
!include "WinMessages.nsh"

!ifndef VERSION
  !define VERSION "0.0.0"
!endif

!define ENV_HKLM 'HKLM "SYSTEM\CurrentControlSet\Control\Session Manager\Environment"'

Name "AirMock"
OutFile "AirMockSetup-${VERSION}.exe"
InstallDir "$PROGRAMFILES64\AirMock"
InstallDirRegKey HKLM "Software\AirMock" "InstallDir"
RequestExecutionLevel admin

!define MUI_ICON "airmock.ico"
!define MUI_UNICON "airmock.ico"
!define MUI_ABORTWARNING

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "$INSTDIR\airmockw.exe"
; Explicit even though a bare launch now defaults to "serve" on its own
; (see cmd/airmock/main.go) — don't rely solely on that fallback here.
!define MUI_FINISHPAGE_RUN_PARAMETERS "serve"
!define MUI_FINISHPAGE_RUN_TEXT "Launch AirMock now"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

Section "AirMock" SecMain
  SectionIn RO
  SetOutPath "$INSTDIR"

  File "airmock.exe"
  File "airmockw.exe"
  File "airmock.ico"

  WriteRegStr HKLM "Software\AirMock" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\AirMock" \
    "DisplayName" "AirMock"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\AirMock" \
    "UninstallString" "$INSTDIR\Uninstall.exe"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\AirMock" \
    "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\AirMock" \
    "Publisher" "AirMock"
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\AirMock" \
    "NoModify" 1
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\AirMock" \
    "NoRepair" 1

  WriteUninstaller "$INSTDIR\Uninstall.exe"

  CreateDirectory "$SMPROGRAMS\AirMock"
  ; "serve" is explicit here too, same reasoning as MUI_FINISHPAGE_RUN_PARAMETERS above.
  CreateShortcut "$SMPROGRAMS\AirMock\AirMock.lnk" "$INSTDIR\airmockw.exe" "serve" "$INSTDIR\airmock.ico" 0
  CreateShortcut "$SMPROGRAMS\AirMock\Uninstall AirMock.lnk" "$INSTDIR\Uninstall.exe"
SectionEnd

Section "Add to PATH" SecPath
  ; Appends $INSTDIR to the machine PATH so "airmock serve" works from any
  ; shell without a bundled runtime or separate PATH-setup step.
  ReadRegStr $0 ${ENV_HKLM} "Path"
  StrCpy $0 "$0;$INSTDIR"
  WriteRegExpandStr ${ENV_HKLM} "Path" "$0"
  SendMessage ${HWND_BROADCAST} ${WM_WININICHANGE} 0 "STR:Environment" /TIMEOUT=5000
SectionEnd

Function un.onInit
  MessageBox MB_ICONQUESTION|MB_YESNO|MB_DEFBUTTON2 \
    "Remove AirMock and its Start Menu shortcuts? Your mock data in %USERPROFILE%\.airmock is left untouched." \
    IDYES +2
  Abort
FunctionEnd

Section "Uninstall"
  Delete "$INSTDIR\airmock.exe"
  Delete "$INSTDIR\airmockw.exe"
  Delete "$INSTDIR\airmock.ico"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\AirMock\AirMock.lnk"
  Delete "$SMPROGRAMS\AirMock\Uninstall AirMock.lnk"
  RMDir "$SMPROGRAMS\AirMock"

  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\AirMock"
  DeleteRegKey HKLM "Software\AirMock"
SectionEnd
