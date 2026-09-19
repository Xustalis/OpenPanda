# test-installers.ps1 — exercise scripts/install.ps1 the way a Windows user does.
#
# Why: nothing in CI ever ran the Windows installer. installers.yml only
# *builds* the release archives and verifies their checksums, and the
# cross-platform gate job only compiles and tests Go. So every Windows-only
# branch in install.ps1 — architecture detection, the curl.exe/InvokeWebRequest
# split, Expand-Archive, registry PATH persistence, schtasks registration —
# shipped unexecuted, and a PowerShell parse error would be found by the first
# user rather than by CI.
#
# The release layout is staged locally and addressed through file:// URLs, so
# this needs no published release and no network.
#
# The zip is written by hand with forward-slash entry names rather than with
# Compress-Archive: PowerShell 5.1's Compress-Archive writes backslash entry
# names, which is not what the release (built on Linux with zip/python) looks
# like, and the whole point is to feed the installer a release-shaped archive.
#
# Usage: powershell -ExecutionPolicy Bypass -File scripts\test-installers.ps1

[CmdletBinding()]
param()

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$Root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $Root

$Version = "0.0.0-test"

$nativeArch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
$arch = switch ($nativeArch) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { Write-Host "test-installers: unsupported host arch $nativeArch" -ForegroundColor Red; exit 1 }
}

$Pass = 0
$Fail = 0
function Ok($m)  { $script:Pass++; Write-Host "  ok    $m" -ForegroundColor Green }
function Bad($m) { $script:Fail++; Write-Host "  FAIL  $m" -ForegroundColor Red }

function To-FileUri([string]$Path) {
    # C:\a\b → file:///C:/a/b — curl.exe accepts this form.
    return "file:///" + ($Path -replace '\\', '/')
}

$Work = Join-Path ([System.IO.Path]::GetTempPath()) ("openpanda-installer-test-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $Work | Out-Null

try {
    # ── Stage a release tree ────────────────────────────────────────────────
    $Dist = Join-Path $Work "dist"
    $Stage = Join-Path $Work "stage/openpanda"
    New-Item -ItemType Directory -Path (Join-Path $Stage "bin") -Force | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $Stage "adapters") -Force | Out-Null
    New-Item -ItemType Directory -Path $Dist -Force | Out-Null

    $Archive = "panda-$Version-windows-$arch.zip"

    Write-Host "-> building panda.exe for windows/$arch"
    $ldflags = "-s -w -X github.com/Xustalis/OpenPanda/internal/version.Version=$Version"
    & go build -ldflags $ldflags -o (Join-Path $Stage "bin/panda.exe") ./cmd/panda
    if ($LASTEXITCODE -ne 0) { Write-Host "test-installers: go build failed" -ForegroundColor Red; exit 1 }

    Copy-Item adapters/*.py (Join-Path $Stage "adapters") -Force
    Copy-Item config.example.yaml $Stage -Force
    Get-ChildItem config/capabilities.example-*.yaml -ErrorAction SilentlyContinue |
        ForEach-Object { Copy-Item $_.FullName $Stage -Force }
    Copy-Item LICENSE $Stage -Force

    # ── Zip it with forward-slash entry names ──────────────────────────────
    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zipPath = Join-Path $Dist $Archive
    $stageParent = Split-Path -Parent $Stage
    $zipStream = [System.IO.File]::Open($zipPath, [System.IO.FileMode]::Create)
    try {
        $zip = New-Object System.IO.Compression.ZipArchive($zipStream, [System.IO.Compression.ZipArchiveMode]::Create)
        try {
            foreach ($file in Get-ChildItem -Recurse -File $Stage) {
                $rel = $file.FullName.Substring($stageParent.Length + 1) -replace '\\', '/'
                $entry = $zip.CreateEntry($rel, [System.IO.Compression.CompressionLevel]::Optimal)
                $es = $entry.Open()
                try {
                    $fs = [System.IO.File]::OpenRead($file.FullName)
                    try { $fs.CopyTo($es) } finally { $fs.Dispose() }
                } finally { $es.Dispose() }
            }
        } finally { $zip.Dispose() }
    } finally { $zipStream.Dispose() }

    $hash = (Get-FileHash -Algorithm SHA256 $zipPath).Hash.ToLowerInvariant()
    "$hash  $Archive" | Set-Content -NoNewline (Join-Path $Dist "checksums.txt")
    # The installer also queries this when no version is pinned.
    '{"tag_name": "v' + $Version + '"}' | Set-Content -NoNewline (Join-Path $Dist "latest.json")

    $Base = To-FileUri $Dist
    $Api = "$Base/latest.json"

    # ── 1. Fresh install, resolving "latest" through the API stub ───────────
    Write-Host "-> install (latest)"
    $Prefix = Join-Path $Work "prefix"
    $env:OPENPANDA_RELEASE_BASE = $Base
    $env:OPENPANDA_RELEASE_API = $Api
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $Root "scripts/install.ps1") -Prefix $Prefix -NoService 2>&1 |
        Tee-Object -FilePath (Join-Path $Work "run1.log") | Out-Null
    $code = $LASTEXITCODE
    if ($code -eq 0) { Ok "installer exited 0" } else {
        Bad "installer exited $code"
        Get-Content (Join-Path $Work "run1.log") -Tail 20 | ForEach-Object { Write-Host "        $_" }
    }

    $Exe = Join-Path $Prefix "bin/panda.exe"
    if (Test-Path $Exe) { Ok "installed $Exe" } else { Bad "$Exe missing" }

    if (Test-Path $Exe) {
        $out = & $Exe version 2>&1 | Out-String
        if ($out -match [regex]::Escape($Version)) { Ok "installed binary reports '$($out.Trim())'" }
        else { Bad "binary reports '$($out.Trim())', expected $Version" }
    }

    $adapterCount = @(Get-ChildItem (Join-Path $Prefix "adapters") -ErrorAction SilentlyContinue).Count
    if ($adapterCount -gt 0) { Ok "adapters/ extracted ($adapterCount files)" } else { Bad "adapters/ empty" }

    $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $binDir = Join-Path $Prefix "bin"
    if (($userPath -split ";") -contains $binDir) { Ok "user PATH carries $binDir" }
    else { Bad "user PATH does not carry $binDir" }

    # ── 2. Re-run over an existing install (upgrade path, idempotency) ──────
    Write-Host "-> re-install (idempotency)"
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $Root "scripts/install.ps1") -Prefix $Prefix -NoService 2>&1 |
        Tee-Object -FilePath (Join-Path $Work "run2.log") | Out-Null
    if ($LASTEXITCODE -eq 0) { Ok "second install exited 0" } else {
        Bad "second install exited $LASTEXITCODE"
        Get-Content (Join-Path $Work "run2.log") -Tail 20 | ForEach-Object { Write-Host "        $_" }
    }
    $userPath2 = [Environment]::GetEnvironmentVariable("Path", "User")
    $entries = @($userPath2 -split ";" | Where-Object { $_ -eq $binDir })
    if ($entries.Count -le 1) { Ok "user PATH did not accumulate duplicates" }
    else { Bad "user PATH now lists $binDir $($entries.Count) times" }

    # ── 3. Explicit -Version (skips the API entirely) ───────────────────────
    Write-Host "-> install (pinned version)"
    $PinnedPrefix = Join-Path $Work "prefix-pinned"
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $Root "scripts/install.ps1") `
        -Prefix $PinnedPrefix -Version $Version -NoService 2>&1 |
        Tee-Object -FilePath (Join-Path $Work "run3.log") | Out-Null
    if ($LASTEXITCODE -eq 0) { Ok "pinned install exited 0" } else {
        Bad "pinned install exited $LASTEXITCODE"
        Get-Content (Join-Path $Work "run3.log") -Tail 20 | ForEach-Object { Write-Host "        $_" }
    }

    # ── 4. A wrong checksum must abort the install ──────────────────────────
    # If this path succeeded, the mandatory verification would be decoration.
    Write-Host "-> install (tampered checksum, must refuse)"
    $BadDist = Join-Path $Work "bad"
    New-Item -ItemType Directory -Path $BadDist -Force | Out-Null
    Copy-Item $zipPath (Join-Path $BadDist $Archive) -Force
    ("{0}  {1}" -f ("0" * 64), $Archive) | Set-Content -NoNewline (Join-Path $BadDist "checksums.txt")
    $env:OPENPANDA_RELEASE_BASE = To-FileUri $BadDist
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $Root "scripts/install.ps1") `
        -Prefix (Join-Path $Work "prefix-bad") -Version $Version -NoService 2>&1 |
        Tee-Object -FilePath (Join-Path $Work "run4.log") | Out-Null
    $log4 = Get-Content (Join-Path $Work "run4.log") -Raw
    if ($LASTEXITCODE -ne 0 -and $log4 -match "SHA-256 mismatch") {
        Ok "installer refused the mismatched checksum"
    } elseif ($LASTEXITCODE -eq 0) {
        Bad "installer accepted a mismatched SHA-256"
    } else {
        Bad "installer refused, but not for the checksum"
        Get-Content (Join-Path $Work "run4.log") -Tail 20 | ForEach-Object { Write-Host "        $_" }
    }

    # ── 5. Unknown version must fail cleanly, not half-install ─────────────
    Write-Host "-> install (missing release, must fail)"
    $env:OPENPANDA_RELEASE_BASE = To-FileUri (Join-Path $Work "empty")
    $env:OPENPANDA_RELEASE_API = (To-FileUri (Join-Path $Work "empty")) + "/latest.json"
    & powershell -NoProfile -ExecutionPolicy Bypass -File (Join-Path $Root "scripts/install.ps1") `
        -Prefix (Join-Path $Work "prefix-missing") -Version "9.9.9" -NoService 2>&1 |
        Tee-Object -FilePath (Join-Path $Work "run5.log") | Out-Null
    if ($LASTEXITCODE -ne 0) { Ok "installer failed for a nonexistent release" }
    else { Bad "installer reported success for a nonexistent release" }
    if (-not (Test-Path (Join-Path $Work "prefix-missing/bin/panda.exe"))) {
        Ok "no binary left behind by the failed run"
    } else {
        Bad "a failed run left a binary behind"
    }

    # ── Summary ────────────────────────────────────────────────────────────
    Write-Host ""
    if ($Fail -ne 0) {
        Write-Host "test-installers: FAILED ($Fail failed, $Pass passed)" -ForegroundColor Red
        exit 1
    }
    Write-Host "test-installers: OK ($Pass checks passed on windows/$arch)" -ForegroundColor Green
}
finally {
    # Best effort: the test added $Prefix\bin to the *user* PATH. On a
    # disposable runner that is harmless, but on a developer's machine it must
    # not survive the test.
    try {
        $cur = [Environment]::GetEnvironmentVariable("Path", "User")
        if ($cur) {
            $kept = @($cur -split ";" | Where-Object { $_ -and $_ -notlike "$Work*" })
            if (($kept -join ";") -ne $cur) {
                [Environment]::SetEnvironmentVariable("Path", ($kept -join ";"), "User")
            }
        }
    } catch {}
    Remove-Item -Recurse -Force $Work -ErrorAction SilentlyContinue
}
