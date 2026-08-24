# OpenPanda one-click installer (Windows).
#
# Usage (PowerShell 5.1+):
#   Set-ExecutionPolicy -Scope Process Bypass
#   irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
# or download and run:
#   powershell -ExecutionPolicy Bypass -File .\install.ps1 -Version 0.0.3 -Yes
#
# Installs panda.exe + adapters into %LOCALAPPDATA%\OpenPanda, adds its bin
# dir to the user PATH (persistent), and when run interactively asks
# whether to register a logon scheduled task that runs `panda daemon` in the
# background. Mirrors the UNIX installer (scripts/install.sh).

[CmdletBinding()]
param(
    [string]$Version = "latest",
    [string]$Prefix = "",
    [switch]$Yes,
    [switch]$NoService
)

$ErrorActionPreference = "Stop"

function Info($m)  { Write-Host "[openpanda] $m" -ForegroundColor DarkGray }
function Ok($m)    { Write-Host "OK   $m" -ForegroundColor Green }
function Warn($m)  { Write-Host "WARN $m" -ForegroundColor Yellow }
function Fail($m)  { Write-Host "ERR  $m" -ForegroundColor Red; exit 1 }

$Repo = if ($env:OPENPANDA_REPO_URL) { $env:OPENPANDA_REPO_URL } else { "https://github.com/Xustalis/OpenPanda" }
$Api  = if ($env:OPENPANDA_RELEASE_API) { $env:OPENPANDA_RELEASE_API } else { "https://api.github.com/repos/Xustalis/OpenPanda/releases/latest" }

# Arch detection
$nativeArch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
$arch = switch ($nativeArch) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { Fail "Unsupported Windows architecture: $nativeArch (supported: amd64, arm64)" }
}

# Resolve version
if ($Version -eq "latest") {
    Info "Checking the latest release..."
    $rel = Invoke-RestMethod -Uri $Api -ErrorAction Stop
    $Version = $rel.tag_name.TrimStart("v")
    Info "Latest version: v$Version"
} else {
    $Version = $Version.TrimStart("v")
}

# Install prefix
if (-not $Prefix) {
    $Prefix = Join-Path $env:LOCALAPPDATA "OpenPanda"
}
$BinDir  = Join-Path $Prefix "bin"
$Exe     = Join-Path $BinDir "panda.exe"
$Archive = "panda-$Version-windows-$arch.zip"
$Base    = if ($env:OPENPANDA_RELEASE_BASE) { $env:OPENPANDA_RELEASE_BASE } else { "$Repo/releases/download/v$Version" }

Info "Installing $Archive to $Prefix..."

$work = Join-Path ([System.IO.Path]::GetTempPath()) ("openpanda-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    $zip = Join-Path $work $Archive
    Invoke-WebRequest -Uri "$Base/$Archive" -OutFile $zip

    # SHA-256 verification is mandatory for release installs.
    $sumPath = Join-Path $work "checksums.txt"
    try {
        Invoke-WebRequest -Uri "$Base/checksums.txt" -OutFile $sumPath
        $wantLine = (Get-Content $sumPath | Where-Object { $_ -match ("\s" + [regex]::Escape($Archive) + "$") } | Select-Object -First 1)
        if (-not $wantLine) { Fail "checksums.txt has no entry for $Archive" }
        $wantHash = ($wantLine -split "\s+")[0].ToUpperInvariant()
        $gotHash  = (Get-FileHash -Algorithm SHA256 $zip).Hash.ToUpperInvariant()
        if ($wantHash -ne $gotHash) { Fail "SHA-256 mismatch (want $wantHash, got $gotHash)" }
        Ok "SHA-256 verified"
    } catch {
        Fail "Unable to download or read checksums.txt; refusing an unverified install"
    }

    # Unpack: archive holds a single top-level `openpanda/` directory
    $extract = Join-Path $work "extract"
    New-Item -ItemType Directory -Path $extract | Out-Null
    Expand-Archive -Path $zip -DestinationPath $extract
    $root = Join-Path $extract "openpanda"
    New-Item -ItemType Directory -Path $Prefix -Force | Out-Null
    Copy-Item -Path (Join-Path $root "*") -Destination $Prefix -Recurse -Force

    if (-not (Test-Path $Exe)) {
        Fail "Release archive is missing $Exe"
    }
}
finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

# Persistent user PATH
$cur = [Environment]::GetEnvironmentVariable("Path", "User")
if (-not ($cur -split ";" | Where-Object { $_ -eq $BinDir })) {
    $new = if ($cur) { "$cur;$BinDir" } else { $BinDir }
    [Environment]::SetEnvironmentVariable("Path", $new, "User")
    $env:Path = "$env:Path;$BinDir"
    Ok "Added $BinDir to the user PATH"
} else {
    Ok "$BinDir is already in the user PATH"
}

# Self-verify
try {
    $v = & $Exe version
    Ok "Self-check passed: $v"
} catch {
    Fail "Self-check failed; run '$Exe version' for details"
}

# Auto-start (logon scheduled task)
$hasConfig = Test-Path (Join-Path $Prefix "config.yaml")

function Register-AutoStart {
    $config = Join-Path $Prefix "config.yaml"
    $card   = Join-Path $Prefix "capabilities.yaml"
    # /RL LIMITED runs without elevation; /SC ONLOGON fires at user logon.
    $tr = '"' + $Exe + '" daemon --config "' + $config + '" --card "' + $card + '"'
    schtasks.exe /Create /TN "OpenPandaNode" /SC ONLOGON /RL LIMITED /TR $tr /F | Out-Null
    Ok "Registered logon task OpenPandaNode. Remove with: schtasks /Delete /TN OpenPandaNode /F"
    if (-not $hasConfig) {
        Warn "No config exists yet. Run 'panda init' before starting the daemon."
    }
}

function Ask-AutoStart {
    $ans = Read-Host "Register panda daemon to start at logon? [y/N]"
    if ($ans -match "^(y|yes)$") { Register-AutoStart }
    else { Info "Skipping auto-start (see docs/install.md to enable it later)" }
}

if ($NoService) {
    # skip
} elseif ($Yes) {
    Register-AutoStart
} elseif ([Environment]::UserInteractive) {
    Ask-AutoStart
} else {
    Info "Non-interactive session: skipping auto-start (pass -Yes to enable it)"
}

Write-Host ""
Ok "Installation complete"
Write-Host "Quick start:" -ForegroundColor DarkGray
Write-Host "      panda init"
Write-Host "      panda repl"
Write-Host "      panda web"
Write-Host "Uninstall: remove $Prefix and remove $BinDir from the user PATH"
