@echo off
rem Manual debug launcher (shows a console window). For silent startup with no
rem window, point the scheduled task at:  wscript.exe C:\panda\start-panda-hidden.vbs
cd /d C:\panda
"C:\panda\panda.exe" --config C:\panda\config.yaml --card C:\panda\capabilities.yaml >> C:\panda\daemon.log 2>&1
