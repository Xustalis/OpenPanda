# PANDA Windows deploy helper: install SSH key + prep layout + start daemon
$ErrorActionPreference = 'Stop'

$pub = 'C:\Windows\Temp\panda_pubkey.txt'
$dir = 'C:\ProgramData\ssh'
if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
$auth = Join-Path $dir 'administrators_authorized_keys'
if (-not (Test-Path $auth)) { New-Item -ItemType File -Path $auth -Force | Out-Null }
$content = Get-Content $pub -Raw
if (Select-String -Path $auth -SimpleMatch ($content.Trim()) -Quiet) {
  Write-Output 'key already present'
} else {
  Add-Content -Path $auth -Value $content
  Write-Output 'key added'
}
icacls $auth /inheritance:r /grant 'SYSTEM:F' /grant 'Administrators:F' | Out-Null
Write-Output ('auth file: ' + (Get-Content $auth -Raw).Trim())
