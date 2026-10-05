; 同目录升级旧原生客户端时移除旧卸载项，避免 Windows「应用」出现两份。
; 只清理 Creght 的旧安装记录和已被 resources/bin 替代的程序；不碰 ~/.shuttle 用户数据。
!macro customInstall
  ReadRegStr $R0 HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\Shuttle" "InstallLocation"
  ReadRegStr $R1 HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\Shuttle" "Publisher"
  ${If} $R0 == $INSTDIR
  ${AndIf} $R1 == "Creght"
    DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\Shuttle"
    Delete "$INSTDIR\Uninstall.exe"
    Delete "$INSTDIR\bin\shuttle.exe"
    Delete "$INSTDIR\bin\creght.exe"
    RMDir "$INSTDIR\bin"
  ${EndIf}

  ; 改名 Annulo（appId 从 com.creght.shuttle 换成 dev.annulo.app，docs/annulo-plan.md 第 6 步）：Windows 会把它当成另一个程序，
  ; 老的 Electron 版 Shuttle 还留在「应用」里。它的卸载项键名是 electron-builder 按老 appId 算的 GUID；有就静默卸掉。
  ; 用户数据在 ~/.shuttle（已迁到 ~/.annulo），老的卸载程序不碰它。
  ReadRegStr $R2 HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\ab291174-67e4-5400-bdc3-39d3927c88bb" "QuietUninstallString"
  ${If} $R2 == ""
    ReadRegStr $R2 HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\ab291174-67e4-5400-bdc3-39d3927c88bb" "UninstallString"
    ${If} $R2 != ""
      StrCpy $R2 "$R2 /S"
    ${EndIf}
  ${EndIf}
  ${If} $R2 != ""
    ExecWait $R2
  ${EndIf}
!macroend
