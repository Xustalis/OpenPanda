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

# Windows PowerShell 5.1 may default to TLS 1.0/1.1, which GitHub rejects.
try {
    [Net.ServicePointManager]::SecurityProtocol = `
        [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {}

function Info($m)  { Write-Host "[openpanda] $m" -ForegroundColor DarkGray }
function Ok($m)    { Write-Host "OK   $m" -ForegroundColor Green }
function Warn($m)  { Write-Host "WARN $m" -ForegroundColor Yellow }
function Fail($m)  { Write-Host "ERR  $m" -ForegroundColor Red; exit 1 }

# curl.exe ships with Windows 10 1803+ and is far more reliable than the
# .NET HTTP stack of Windows PowerShell 5.1 (stale proxy settings, TLS
# quirks). Prefer it when present; fall back to the cmdlets otherwise.
$script:CurlExe = $null
try { $script:CurlExe = (Get-Command curl.exe -ErrorAction Stop).Source } catch {}

function Get-Url([string]$Url, [string]$OutFile) {
    if ($script:CurlExe) {
        & $script:CurlExe -fsSL --retry 3 --connect-timeout 20 -o $OutFile $Url
        if ($LASTEXITCODE -eq 0) { return }
        Warn "curl.exe exited $LASTEXITCODE for $Url, retrying with Invoke-WebRequest..."
    }
    Invoke-WebRequest -UseBasicParsing -TimeoutSec 60 -Uri $Url -OutFile $OutFile
}

function Get-Json([string]$Url) {
    if ($script:CurlExe) {
        $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("openpanda-api-" + [guid]::NewGuid().ToString("N") + ".json")
        & $script:CurlExe -fsSL --retry 3 --connect-timeout 20 -o $tmp $Url
        if ($LASTEXITCODE -eq 0) {
            try { return (Get-Content -Raw $tmp | ConvertFrom-Json) }
            finally { Remove-Item -f $tmp -ErrorAction SilentlyContinue }
        }
        Warn "curl.exe exited $LASTEXITCODE for $Url, retrying with Invoke-RestMethod..."
    }
    return (Invoke-RestMethod -UseBasicParsing -TimeoutSec 30 -Uri $Url)
}

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
    $tag = $null
    try { $tag = (Get-Json $Api).tag_name } catch {
        Warn "GitHub API unreachable; falling back to the release redirect..."
    }
    if (-not $tag) {
        # api.github.com is rate-limited per IP (60 req/h); the
        # /releases/latest 302 redirect is not. Try curl.exe first, then raw
        # .NET with the proxy bypassed (works even when the WinINET stack
        # used by Invoke-WebRequest is misconfigured).
        try {
            if ($script:CurlExe) {
                $hdr = & $script:CurlExe -fsSI --connect-timeout 20 "$Repo/releases/latest" 2>$null
                $loc = ($hdr | Where-Object { $_ -match '^[Ll]ocation:' } | Select-Object -First 1)
                if ($loc -match '^[Ll]ocation:\s*(\S+)') { $tag = $Matches[1] }
            }
        } catch {}
        if (-not $tag) {
            try {
                $req = [System.Net.HttpWebRequest]::Create("$Repo/releases/latest")
                $req.AllowAutoRedirect = $false
                $req.Proxy = $null
                $req.Timeout = 20000
                $resp = $req.GetResponse()
                $tag = $resp.Headers["Location"]
                $resp.Close()
            } catch {}
        }
        if ($tag -and $tag -match '/tag/(v?[0-9][^/?#]*)') { $tag = $Matches[1] } else { $tag = $null }
    }
    if (-not $tag) { Fail "Unable to resolve the latest release (network/API issue? Pin it: -Version 0.0.4)" }
    $Version = $tag.TrimStart("v")
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
    Get-Url "$Base/$Archive" $zip

    # SHA-256 verification is mandatory for release installs.
    $sumPath = Join-Path $work "checksums.txt"
    try {
        Get-Url "$Base/checksums.txt" $sumPath
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
