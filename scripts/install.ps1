# SPDX-License-Identifier: AGPL-3.0-or-later

# OpenPanda one-click installer (Windows).
#
# Usage (PowerShell 5.1+):
#   Set-ExecutionPolicy -Scope Process Bypass
#   irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
# or download and run:
#   powershell -ExecutionPolicy Bypass -File .\install.ps1 -Version <tag> -Yes
#   powershell -ExecutionPolicy Bypass -File .\install.ps1 -Prefix D:\openpanda
#   powershell -ExecutionPolicy Bypass -File .\install.ps1 -Force   # deliberate downgrade
#
# Env:
#   OPENPANDA_VERSION      default version when -Version is not given
#   OPENPANDA_PREFIX       default install dir when -Prefix is not given
#   OPENPANDA_REPO_URL     override the source repository
#   OPENPANDA_RELEASE_API  override the "latest release" API endpoint
#   OPENPANDA_RELEASE_BASE override the release download base URL
#   OPENPANDA_FORCE        "1" behaves like -Force
#
# Installs panda.exe + adapters + voice sidecars into %LOCALAPPDATA%\OpenPanda, adds its bin
# dir to the user PATH (persistent), and when run interactively asks
# whether to register a logon scheduled task that runs `panda daemon` in the
# background. Mirrors the UNIX installer (scripts/install.sh) option for
# option, including the environment-variable defaults and the
# schema-compatibility guard.
#
# Unlike install.sh this script cannot check the release signature
# (checksums.txt.sig is Ed25519; neither .NET Framework nor PowerShell 5.1
# ships a verifier): archives are authenticated by SHA-256 against
# checksums.txt. The self-updater (`panda update apply`) does verify the
# signature before it swaps a binary, so use that path where it matters.

[CmdletBinding()]
param(
    [string]$Version = "",
    [string]$Prefix = "",
    [switch]$Yes,
    [switch]$NoService,
    [switch]$Force
)

$ErrorActionPreference = "Stop"

# Windows PowerShell 5.1 renders a progress bar for every Invoke-WebRequest
# byte unless this is off; on a ~30 MB archive that is the difference between
# seconds and minutes, and it floods the console the script is also trying to
# print into.
$ProgressPreference = "SilentlyContinue"

# Environment defaults, matching scripts/install.sh's OPENPANDA_VERSION /
# OPENPANDA_PREFIX. A parameter always wins over the variable.
if (-not $Version) {
    $Version = if ($env:OPENPANDA_VERSION) { $env:OPENPANDA_VERSION } else { "latest" }
}
if (-not $Prefix -and $env:OPENPANDA_PREFIX) {
    $Prefix = $env:OPENPANDA_PREFIX
}
if (-not $Force -and $env:OPENPANDA_FORCE -eq "1") {
    $Force = [switch]$true
}

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

# Windows curl builds link against Schannel, which validates certificate
# revocation by default and hard-fails with CRYPT_E_NO_REVOCATION_CHECK
# (0x80092012) when the CRL/OCSP endpoints cannot be queried:
#
#   curl: (35) schannel: next InitializeSecurityContext failed:
#   CRYPT_E_NO_REVOCATION_CHECK (0x80092012) - the revocation function was
#   unable to check for revocation.
#
# That is a strict-policy outcome, not a bad certificate: the chain, hostname
# and expiry all verify, only the "is it revoked?" lookup has nowhere to go.
# It is routine on networks that block or intercept CDN traffic.
# --ssl-no-revoke skips exactly that lookup and nothing else. The transport is
# not this installer's trust anchor anyway: every archive is authenticated by
# SHA-256 against checksums.txt, so a tampered download still fails.
#
# The flag only exists on Schannel builds (an OpenSSL-linked curl, e.g. some
# Git for Windows installs, rejects it), so probe once rather than assume.
# Every string this script prints stays ASCII on purpose: Windows PowerShell
# 5.1 decodes a BOM-less script with the system ANSI code page, so non-ASCII in
# a message would reach the console as mojibake.
$script:CurlTlsArgs = @()
if ($script:CurlExe) {
    & $script:CurlExe --ssl-no-revoke --version 2>$null | Out-Null
    if ($LASTEXITCODE -eq 0) { $script:CurlTlsArgs = @("--ssl-no-revoke") }
}

# Curl's exit codes are the only structured signal it gives; without a mapping
# the installer tells the operator "exited 35", which is not actionable.
function ConvertFrom-CurlExit([int]$Code) {
    switch ($Code) {
        6  { "host not resolvable (DNS)" }
        7  { "connection refused or host unreachable" }
        28 { "timed out" }
        35 { "TLS handshake failed (blocked proxy, or an unreachable certificate revocation endpoint)" }
        56 { "connection reset while receiving" }
        default { "exit code $Code" }
    }
}

# GitHub release assets are served from signed URLs whose query is several
# hundred bytes of token; printing it verbatim buries the reason for the
# failure and floods the console the script is also trying to write into.
function Format-UrlForMessage([string]$Url) {
    $i = $Url.IndexOf("?")
    if ($i -ge 0) { return $Url.Substring(0, $i) + "?..." }
    return $Url
}

function Get-Url([string]$Url, [string]$OutFile) {
    if ($script:CurlExe) {
        # stderr is captured rather than streamed: curl's raw multi-line
        # diagnostic reads like a crash even when the very next line succeeds
        # via the fallback below.
        $errFile = [System.IO.Path]::GetTempFileName()
        try {
            $tls = $script:CurlTlsArgs
            & $script:CurlExe -fsSL @tls --retry 3 --connect-timeout 20 -o $OutFile $Url 2>$errFile
            if ($LASTEXITCODE -eq 0) { return }
            $detail = (Get-Content -Raw $errFile -ErrorAction SilentlyContinue)
        } finally {
            Remove-Item -Force $errFile -ErrorAction SilentlyContinue
        }
        Warn "curl.exe failed for $(Format-UrlForMessage $Url): $(ConvertFrom-CurlExit $LASTEXITCODE) - retrying with Invoke-WebRequest..."
        if ($detail) { Write-Verbose $detail.Trim() }
    }
    Invoke-WebRequest -UseBasicParsing -TimeoutSec 60 -Uri $Url -OutFile $OutFile
}

function Get-Json([string]$Url) {
    if ($script:CurlExe) {
        $tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("openpanda-api-" + [guid]::NewGuid().ToString("N") + ".json")
        $errFile = [System.IO.Path]::GetTempFileName()
        try {
            $tls = $script:CurlTlsArgs
            & $script:CurlExe -fsSL @tls --retry 3 --connect-timeout 20 -o $tmp $Url 2>$errFile
            if ($LASTEXITCODE -eq 0) {
                try { return (Get-Content -Raw $tmp | ConvertFrom-Json) }
                # -Force, not -f: Remove-Item has both -Filter and -Force, so the
                # one-letter form is an ambiguous parameter prefix and fails to
                # bind — which with $ErrorActionPreference = "Stop" set above
                # would abort the install at its first API call.
                finally { Remove-Item -Force $tmp -ErrorAction SilentlyContinue }
            }
            Warn "curl.exe failed for $(Format-UrlForMessage $Url): $(ConvertFrom-CurlExit $LASTEXITCODE) - retrying with Invoke-RestMethod..."
        } finally {
            Remove-Item -Force $tmp -ErrorAction SilentlyContinue
            Remove-Item -Force $errFile -ErrorAction SilentlyContinue
        }
    }
    return (Invoke-RestMethod -UseBasicParsing -TimeoutSec 30 -Uri $Url)
}

$Repo = if ($env:OPENPANDA_REPO_URL) { $env:OPENPANDA_REPO_URL } else { "https://github.com/Xustalis/OpenPanda" }
# The API endpoint has to follow $Repo: overriding the repository to a fork or
# a mirror while the version lookup still queried upstream would resolve a tag
# that the download base does not serve. install.sh derives the same slug.
$RepoSlug = ($Repo -replace '^https?://github\.com/', '').TrimEnd('/')
$Api  = if ($env:OPENPANDA_RELEASE_API) { $env:OPENPANDA_RELEASE_API } else { "https://api.github.com/repos/$RepoSlug/releases/latest" }

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
                $tls = $script:CurlTlsArgs
                $hdr = & $script:CurlExe -fsSI @tls --connect-timeout 20 "$Repo/releases/latest" 2>$null
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
    if (-not $tag) { Fail "Unable to resolve the latest release (network/API issue? Pin it: -Version 0.0.10)" }
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

# Probe a binary for the two numbers the schema guard needs: `schema` (the
# migration ceiling this build supports) and `db_schema` (the configured
# database's current user_version). Both come from `panda version --json`;
# a binary too old to report them, or one that cannot run at all, yields $null.
function Get-SchemaProbe([string]$ExePath) {
    if (-not (Test-Path $ExePath)) { return $null }
    try {
        $raw = & $ExePath version --json 2>$null | Out-String
        if ($LASTEXITCODE -ne 0 -or -not $raw) { return $null }
        return ($raw | ConvertFrom-Json)
    } catch {
        return $null
    }
}

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

    # ── Schema-compatibility guard (mirrors install.sh) ────────────────────
    # A binary whose migration ceiling is below the database's user_version
    # dies at startup ("schema version N is newer than this binary's highest
    # migration"), so installing it strands the data directory. The staged
    # archive is probed *before* it is copied over the prefix, so a refused
    # install leaves the existing one untouched. A release too old to report
    # its ceiling cannot prove it can open an existing database and is refused
    # too. -Force / OPENPANDA_FORCE=1 overrides for deliberate downgrades.
    $newProbe   = Get-SchemaProbe (Join-Path $root "bin\panda.exe")
    $newSchema  = if ($newProbe -and $newProbe.schema) { [int]$newProbe.schema } else { 0 }
    $floor      = if ($newProbe -and $newProbe.db_schema) { [int]$newProbe.db_schema } else { 0 }
    if ($floor -eq 0) {
        # The staged binary could not see the data directory (no config in this
        # session, or a pre-`panda init` host): ask whatever is installed here.
        foreach ($prev in @($Exe, (Join-Path $env:LOCALAPPDATA "OpenPanda\bin\panda.exe"))) {
            $probe = Get-SchemaProbe $prev
            if (-not $probe) { continue }
            if ($probe.db_schema) { $floor = [int]$probe.db_schema }
            elseif ($probe.schema) { $floor = [int]$probe.schema }
            if ($floor -gt 0) { break }
        }
    }
    if (-not $Force -and $floor -gt 0) {
        if ($newSchema -eq 0) {
            Fail "Refusing to install: this release is too old to report its schema ceiling while the data directory is already at schema v$floor - it would fail to start. Pass -Force to install anyway."
        } elseif ($newSchema -lt $floor) {
            Fail "Refusing to install: this release supports at most schema v$newSchema but the data directory is already at v$floor - it would fail to start. Pass -Force to install anyway."
        }
    }

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
if (-not ($cur -split ";" | Where-Object { ([Environment]::ExpandEnvironmentVariables($_)) -eq $BinDir })) {
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
    # A native command's non-zero exit is not an exception: check it
    # explicitly, or a broken binary (missing runtime DLL, AV-quarantined)
    # would still report "Self-check passed".
    if ($LASTEXITCODE -ne 0) {
        Fail "Self-check failed (exit $LASTEXITCODE); run '$Exe version' for details"
    }
    Ok "Self-check passed: $v"
} catch {
    Fail "Self-check failed; run '$Exe version' for details"
}

# Auto-start (logon scheduled task)
# The config check follows the same discovery order the daemon uses
# (config.ResolvePath: OPENPANDA_CONFIG_PATH, then the user config dir, then
# the machine-wide default), not the install prefix — `panda init` writes to
# %APPDATA%\openpanda\config.yaml, and the prefix only ever holds the example
# files.
$userConfig   = Join-Path $env:APPDATA "openpanda\config.yaml"
$systemConfig = Join-Path $env:ProgramData "OpenPanda\config.yaml"
$hasConfig = (Test-Path $userConfig) -or (Test-Path $systemConfig) -or ($env:OPENPANDA_CONFIG_PATH -and (Test-Path $env:OPENPANDA_CONFIG_PATH))

function Register-AutoStart {
    # No --config/--card flags: the daemon auto-discovers the user-level config
    # written by `panda init` (config.ResolvePath order), exactly like the
    # LaunchAgent / systemd units the shell installer writes. Pinning the prefix
    # paths here used to point the task at config files that never exist in the
    # prefix — and because an explicit flag wins over discovery, the daemon
    # silently ran on defaults even after `panda init`.
    # /RL LIMITED runs without elevation; /SC ONLOGON fires at user logon.
    # A scheduled task has no console, so the daemon's stdout/stderr would
    # vanish entirely — the launchd plist's StandardOutPath equivalent is an
    # append redirect. The redirect lives in a generated wrapper .cmd rather
    # than inline in /TR: schtasks re-quotes the /TR string (dropping inner
    # quote pairs), which turns `cmd /c ""exe" args >> "log" 2>&1"` into a
    # single quoted blob cmd cannot parse — a task that registers cleanly and
    # fails at every logon. A wrapper file carries no quoting ambiguity at all.
    $logDir  = Join-Path $env:LOCALAPPDATA "openpanda-data"
    New-Item -ItemType Directory -Force -Path $logDir | Out-Null
    $logFile = Join-Path $logDir "daemon.log"
    $wrapper = Join-Path $BinDir "panda-daemon.cmd"
    Set-Content -Path $wrapper -Encoding ASCII -Value @"
@echo off
rem Generated by install.ps1 - runs the daemon under the logon task with
rem stdout/stderr appended to the data dir's daemon.log (a scheduled task
rem has no console; without this its logs are lost entirely).
"$Exe" daemon >> "$logFile" 2>&1
"@
    $tr = '"' + $wrapper + '"'
    schtasks.exe /Create /TN "OpenPandaNode" /SC ONLOGON /RL LIMITED /TR $tr /F | Out-Null
    # A native command's non-zero exit is not an exception, so
    # $ErrorActionPreference does not notice it: without this check the script
    # printed "Registered logon task" even when schtasks refused (policy-blocked
    # hosts, an existing task owned by another user, a locked-down task folder).
    if ($LASTEXITCODE -ne 0) {
        Warn "schtasks exited $LASTEXITCODE; the logon task was NOT registered."
        Warn "Register it manually: schtasks /Create /TN OpenPandaNode /SC ONLOGON /RL LIMITED /TR '$tr' /F"
        return
    }
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
Write-Host "      panda init      # generate config + capability cards"
Write-Host "      panda repl      # interactive command line"
Write-Host "      panda web       # embedded web console (auto-login)"
Write-Host "Self-check: panda doctor"
Write-Host "Uninstall:  panda uninstall  (whitelist sweep + automatic backup;"
Write-Host "            --purge also removes user data, --backup-only backs up first)"
Write-Host "            Logon task removal: schtasks /Delete /TN OpenPandaNode /F"
Write-Host "See docs/install.md for the full guide."
