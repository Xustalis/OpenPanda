' PANDA daemon — silent launcher (no console window).
' Usage: point the scheduled task "PANDA" action at:
'     wscript.exe C:\panda\start-panda-hidden.vbs
' The daemon still logs to C:\panda\daemon.log via cmd redirection.
Set sh = CreateObject("WScript.Shell")
sh.CurrentDirectory = "C:\panda"
sh.Run "cmd /c ""C:\panda\panda.exe"" --config C:\panda\config.yaml --card C:\panda\capabilities.yaml >> C:\panda\daemon.log 2>&1", 0, False
