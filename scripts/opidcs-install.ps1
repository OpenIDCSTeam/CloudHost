# ============================================================================
# opidcs 一键安装（Windows）
#
#   irm https://raw.githubusercontent.com/OpenIDCSTeam/CloudHost/main/scripts/opidcs-install.ps1 | iex
#   .\opidcs-install.ps1 -Version v1.0.0 -Dir "C:\Tools\opidcs"
#   .\opidcs-install.ps1 -File .\opidcs-windows-amd64.exe
# ============================================================================
param(
    [string]$Version = "",
    [string]$Dir = "$env:LOCALAPPDATA\opidcs\bin",
    [string]$File = ""
)

$ErrorActionPreference = "Stop"
$repo = "OpenIDCSTeam/CloudHost"
$arch = switch ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture) {
    "X64" { "amd64" }
    "Arm64" { "arm64" }
    default { throw "不支持的架构: $_" }
}
$asset = "opidcs-windows-$arch.exe"

if (-not $File) {
    if (-not $Version) {
        Write-Host "==> 查询最新版本"
        $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest" `
            -Headers @{ "User-Agent" = "opidcs-installer" }
        $Version = $release.tag_name
    }
    if (-not $Version) { throw "无法确定版本，请用 -Version 指定" }
    $url = "https://github.com/$repo/releases/download/$Version/$asset"
    $tmp = Join-Path $env:TEMP "opidcs-download.exe"
    Write-Host "==> 下载 $url"
    Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing
} else {
    if (-not (Test-Path $File)) { throw "文件不存在: $File" }
    $tmp = $File
}

New-Item -ItemType Directory -Force -Path $Dir | Out-Null
$target = Join-Path $Dir "opidcs.exe"
Copy-Item -Path $tmp -Destination $target -Force
if ($tmp -ne $File) { Remove-Item $tmp -Force }

Write-Host "==> 已安装到 $target"
& $target version

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$Dir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$Dir", "User")
    Write-Host "==> 已将 $Dir 加入用户 PATH（新终端生效）"
}
