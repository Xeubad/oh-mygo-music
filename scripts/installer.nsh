; Keep the existing MyGo Music install path while renaming the app.
; The installer writes the new executable before removing the old one.

!macro customInstall
  Delete "$INSTDIR\MyGo Music.exe"
  Delete "$SMPROGRAMS\MyGo Music.lnk"
  IfFileExists "$DESKTOP\MyGo Music.lnk" 0 no_old_desktop_shortcut
  Delete "$DESKTOP\MyGo Music.lnk"
  Call CreateDesktopShortcut
  no_old_desktop_shortcut:
!macroend

!macro customUnInstall
  Push "$SMPROGRAMS\MyGo Music.lnk"
  Call un.DeleteShortcut
  Push "$DESKTOP\MyGo Music.lnk"
  Call un.DeleteShortcut
!macroend
