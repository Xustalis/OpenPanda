# SPDX-License-Identifier: AGPL-3.0-or-later

# PANDA Windows deploy helper: install SSH key + prep layout + start daemon
#
# The key is appended to administrators_authorized_keys, so whatever this
# script reads becomes permanent SSH access for an administrator. A shared
# temp path is not a trust anchor by itself: pass -PubKeyPath with a file only
# Administrators can write, or -PubKey with the key itself, and pin the file
# with -PubKeySha256 when the key travels out of band.
param(
  [string]$PubKeyPath = 'C:\Windows\Temp\panda_pubkey.txt',
  [string]$PubKey = '',
  [string]$PubKeySha256 = ''
)
$ErrorActionPreference = 'Stop'

$dir = 'C:\ProgramData\ssh'
if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
$auth = Join-Path $dir 'administrators_authorized_keys'
if (-not (Test-Path $auth)) { New-Item -ItemType File -Path $auth -Force | Out-Null }

if ($PubKey) {
  $content = $PubKey
} else {
  if (-not (Test-Path -LiteralPath $PubKeyPath)) {
    throw "public key not found: $PubKeyPath"
  }
  if ($PubKeySha256) {
    $actual = (Get-FileHash -LiteralPath $PubKeyPath -Algorithm SHA256).Hash
    if ($actual -ne $PubKeySha256.Replace(':','').ToUpperInvariant()) {
      throw "public key hash mismatch: $actual"
    }
  }
  $content = Get-Content -LiteralPath $PubKeyPath -Raw
}
$content = $content.Trim()
if ($content -match '[\r\n\x00]') {
  throw 'refusing a multi-line public key: administrators_authorized_keys would gain more than the one key'
}
$keyPattern = '^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|sk-ssh-ed25519@openssh.com|sk-ecdsa-sha2-nistp256@openssh.com)\s+[A-Za-z0-9+/=]+(\s+.*)?$'
if ($content -notmatch $keyPattern) {
  throw 'refusing content that is not a single SSH public key (the first field must be a supported key type)'
}

if (Select-String -Path $auth -SimpleMatch $content -Quiet) {
  Write-Output 'key already present'
} else {
  Add-Content -Path $auth -Value $content
  Write-Output 'key added'
}
icacls $auth /inheritance:r /grant 'SYSTEM:F' /grant 'Administrators:F' | Out-Null
Write-Output ('auth file: ' + (Get-Content $auth -Raw).Trim())
