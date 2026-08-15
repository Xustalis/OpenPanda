@echo off
cd /d C:\panda
"C:\panda\panda.exe" --config C:\panda\config.yaml --card C:\panda\capabilities.yaml >> C:\panda\daemon.log 2>&1
