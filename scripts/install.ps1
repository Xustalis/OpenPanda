# OpenPanda one-click installer (Windows).
#
# Usage (PowerShell 5.1+):
#   Set-ExecutionPolicy -Scope Process Bypass
#   irm https://raw.githubusercontent.com/Xustalis/OpenPanda/main/scripts/install.ps1 | iex
# or download and run:
#   powershell -ExecutionPolicy Bypass -File .\install.ps1 -Version 0.0.2 -Yes
#
# Installs panda.exe + adapters into %LOCALAPPDATA%\OpenPanda, adds its bin
# dir to the user PATH (persistent), and — when run interactively — asks
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

$Repo = "https://github.com/Xustalis/OpenPanda"
$Api  = "https://api.github.com/repos/Xustalis/OpenPanda/releases/latest"

# ── Arch detection ──────────────────────────────────────────────────────────
$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { "amd64" }
    "ARM64" { "arm64" }
    default { "amd64" }
}
if ($arch -ne "amd64") {
    Warn "当前架构 $arch ：仅发布 windows-amd64，将按 amd64 处理（若失败请改在 x64 主机安装）。"
    $arch = "amd64"
}

# ── Resolve version ─────────────────────────────────────────────────────────
if ($Version -eq "latest") {
    Info "查询最新发行版…"
    $rel = Invoke-RestMethod -Uri $Api -ErrorAction Stop
    $Version = $rel.tag_name.TrimStart("v")
    Info "最新版本: v$Version"
} else {
    $Version = $Version.TrimStart("v")
}

# ── Install prefix ──────────────────────────────────────────────────────────
if (-not $Prefix) {
    $Prefix = Join-Path $env:LOCALAPPDATA "OpenPanda"
}
$BinDir  = Join-Path $Prefix "bin"
$Exe     = Join-Path $BinDir "panda.exe"
$Archive = "panda-$Version-windows-amd64.zip"
$Base    = "$Repo/releases/download/v$Version"

Info "安装 $Archive 到 $Prefix …"

$work = Join-Path ([System.IO.Path]::GetTempPath()) ("openpanda-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $work | Out-Null
try {
    $zip = Join-Path $work $Archive
    Invoke-WebRequest -Uri "$Base/$Archive" -OutFile $zip

    # SHA-256 verification (non-fatal if checksums.txt is absent)
    $sumPath = Join-Path $work "checksums.txt"
    try {
        Invoke-WebRequest -Uri "$Base/checksums.txt" -OutFile $sumPath
        $wantLine = (Get-Content $sumPath | Where-Object { $_ -match [regex]::Escape($Archive) } | Select-Object -First 1)
        if ($wantLine) {
            $wantHash = ($wantLine -split "\s+")[0]
            $gotHash  = (Get-FileHash -Algorithm SHA256 $zip).Hash
            if ($wantHash -and ($wantHash -ne $gotHash)) {
                Fail "SHA-256 校验失败（期望 $wantHash，得到 $gotHash）"
            }
            Ok "SHA-256 校验通过"
        }
    } catch {
        Warn "未找到 checksums.txt，跳过 SHA-256 校验"
    }

    # Unpack: archive holds a single top-level `openpanda/` directory
    $extract = Join-Path $work "extract"
    New-Item -ItemType Directory -Path $extract | Out-Null
    Expand-Archive -Path $zip -DestinationPath $extract
    $root = Join-Path $extract "openpanda"
    New-Item -ItemType Directory -Path $Prefix -Force | Out-Null
    Copy-Item -Path (Join-Path $root "*") -Destination $Prefix -Recurse -Force

    if (-not (Test-Path $Exe)) {
        Fail "安装包缺少 $Exe"
    }
}
finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

# ── Persistent user PATH ────────────────────────────────────────────────────
$cur = [Environment]::GetEnvironmentVariable("Path", "User")
if (-not ($cur -split ";" | Where-Object { $_ -eq $BinDir })) {
    $new = if ($cur) { "$cur;$BinDir" } else { $BinDir }
    [Environment]::SetEnvironmentVariable("Path", $new, "User")
    $env:Path = "$env:Path;$BinDir"
    Ok "已把 $BinDir 加入用户 PATH"
} else {
    Ok "$BinDir 已在用户 PATH 中"
}

# ── Self-verify ─────────────────────────────────────────────────────────────
try {
    $v = & $Exe version
    Ok "自检通过: $v"
} catch {
    Fail "自检失败：请运行 '$Exe version' 查看原因"
}

# ── Auto-start (logon scheduled task) ───────────────────────────────────────
$hasConfig = Test-Path (Join-Path $Prefix "config.yaml")

function Register-AutoStart {
    $config = Join-Path $Prefix "config.yaml"
    $card   = Join-Path $Prefix "capabilities.yaml"
    # /RL LIMITED runs without elevation; /SC ONLOGON fires at user logon.
    $tr = '"' + $Exe + '" daemon --config "' + $config + '" --card "' + $card + '"'
    schtasks.exe /Create /TN "OpenPandaNode" /SC ONLOGON /RL LIMITED /TR $tr /F | Out-Null
    Ok "已注册登录自启（计划任务 OpenPandaNode）。停用： schtasks /Delete /TN OpenPandaNode /F"
    if (-not $hasConfig) {
        Warn "尚未生成配置：请先运行 'panda init'，否则自启的后台 daemon 会因缺 config.yaml 无法启动。"
    }
}

function Ask-AutoStart {
    $ans = Read-Host "是否注册开机自启服务（后台运行 panda daemon）？[y/N]"
    if ($ans -match "^(y|yes)$") { Register-AutoStart }
    else { Info "跳过开机自启（可稍后手动注册，见 docs/install.md）" }
}

if ($NoService) {
    # skip
} elseif ($Yes) {
    Register-AutoStart
} elseif ([Environment]::UserInteractive) {
    Ask-AutoStart
} else {
    Info "非交互会话：跳过开机自启（可用 -Yes 显式启用）"
}

Write-Host ""
Ok "安装完成"
Write-Host "快速开始:" -ForegroundColor DarkGray
Write-Host "      panda init      # 交互式生成配置与能力卡"
Write-Host "      panda repl      # 进入交互命令行"
Write-Host "      panda web       # 打开内嵌 Web 控制台（自动登录）"
Write-Host "卸载：删除 $Prefix，并在用户环境变量 PATH 中移除 $BinDir"