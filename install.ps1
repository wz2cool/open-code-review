# Install the ocr (Open Code Review) CLI and the ocr_ext companion binary
# from GitHub releases on Windows.
#   irm https://open-codereview.ai/install.ps1 | iex
# Prefer to inspect first:
#   irm https://open-codereview.ai/install.ps1 -OutFile install.ps1
#   notepad install.ps1   # review, then: .\install.ps1
# Env: OCR_INSTALL_DIR (default $env:LOCALAPPDATA\Programs\ocr), OCR_VERSION (default latest),
# OCR_GITHUB_MIRROR (default unset; download the binary through a mirror domain).
# Requires PowerShell 5.1+ or PowerShell 7+.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'Continue'

function Err([string]$Message) {
    [Console]::Error.WriteLine("error: $Message")
    exit 1
}

function Get-OcrArch {
    $arch = $env:PROCESSOR_ARCHITECTURE
    if ([string]::IsNullOrEmpty($arch)) {
        Err 'unable to detect architecture (PROCESSOR_ARCHITECTURE is empty); please set it manually'
    }
    switch -Regex ($arch) {
        '^(AMD64|X64|x86_64)$' { return 'amd64' }
        '^(ARM64|aarch64)$' { return 'arm64' }
        default { Err "unsupported architecture: $arch (only amd64 and arm64 are supported)" }
    }
}

function Resolve-OcrVersion([string]$Repo) {
    $version = $env:OCR_VERSION
    if (-not [string]::IsNullOrWhiteSpace($version)) {
        return $version.Trim()
    }
    try {
        $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -UseBasicParsing
    } catch {
        Err "failed to fetch latest release info from github api"
    }
    if (-not $release.tag_name) {
        Err 'could not resolve latest release tag'
    }
    return [string]$release.tag_name
}

function Get-ChecksumFromFile([string]$ChecksumFile, [string]$AssetName) {
    foreach ($line in Get-Content -LiteralPath $ChecksumFile) {
        if ([string]::IsNullOrWhiteSpace($line)) { continue }
        $parts = $line.Trim() -split '\s+', 2
        if ($parts.Count -eq 2 -and $parts[1] -eq $AssetName) {
            return $parts[0].ToLowerInvariant()
        }
    }
    return $null
}

function Install-OcrBinary([string]$Source, [string]$InstallDir, [string]$BinName) {
    try {
        New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
        $dest = Join-Path $InstallDir $BinName
        Copy-Item -LiteralPath $Source -Destination $dest -Force
    } catch {
        Err "$InstallDir is not writable; set OCR_INSTALL_DIR to a writable path"
    }
}

function Show-PostInstallPathNotice([string]$BinName, [string]$InstallDir) {
    $pathEntries = $env:PATH -split ';' | ForEach-Object { $_.TrimEnd('\') }
    $normalizedInstall = $InstallDir.TrimEnd('\')
    $onPath = $false
    foreach ($entry in $pathEntries) {
        if ($entry -and [string]::Equals($entry, $normalizedInstall, [System.StringComparison]::OrdinalIgnoreCase)) {
            $onPath = $true
            break
        }
    }
    if (-not $onPath) {
        Write-Host "note: $InstallDir is not on your PATH; add it or run $InstallDir\$BinName directly"
        return
    }
    if (-not (Get-Command $BinName -ErrorAction SilentlyContinue)) {
        Write-Host "note: open a new shell so $BinName resolves on PATH"
    }
}

# Ensure TLS 1.2 for Windows PowerShell 5.1 (Invoke-WebRequest / Invoke-RestMethod).
try {
    [Net.ServicePointManager]::SecurityProtocol = `
        [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
    # Ignore if the runtime already negotiates modern TLS.
}

$Repo = 'alibaba/open-code-review'
$Bin = 'ocr.exe'
$AssetPrefix = 'opencodereview'
$ExtBin = 'ocr_ext.exe'
$ExtAssetPrefix = 'ocrext'
$DefaultInstallDir = Join-Path $env:LOCALAPPDATA 'Programs\ocr'
$InstallDir = if (-not [string]::IsNullOrWhiteSpace($env:OCR_INSTALL_DIR)) {
    $env:OCR_INSTALL_DIR.Trim()
} else {
    $DefaultInstallDir
}

$arch = Get-OcrArch
$os = 'windows'
$Version = Resolve-OcrVersion $Repo
$Mirror = if (-not [string]::IsNullOrWhiteSpace($env:OCR_GITHUB_MIRROR)) {
    $env:OCR_GITHUB_MIRROR.Trim() -replace '^https?://' -replace '/$'
} else {
    $null
}
if ($Mirror -and $Mirror -match '\s') {
    Err "OCR_GITHUB_MIRROR contains spaces: '$Mirror'"
}
if ($Mirror) {
    [Console]::Error.WriteLine("warning: downloading from unofficial GitHub mirror `"$Mirror`" (checksum integrity is not guaranteed)")
    $base = "https://$Mirror/github.com/$Repo/releases/download/$Version"
} else {
    $base = "https://github.com/$Repo/releases/download/$Version"
}

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("ocr-install-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

# Get-OcrAsset downloads one release asset and verifies it against the
# already fetched sha256sum.txt. Returns the verified path, or $null when the
# asset is absent (releases older than ocr_ext carry no companion asset).
# A checksum mismatch is always fatal.
function Get-OcrAsset([string]$AssetPrefix) {
    $asset = "$AssetPrefix-$os-$arch.exe"
    $assetPath = Join-Path $tmp $asset

    Write-Host "downloading $asset..."
    try {
        Invoke-WebRequest -Uri "$base/$asset" -OutFile $assetPath -UseBasicParsing -TimeoutSec 1800
    } catch {
        return $null
    }

    $want = Get-ChecksumFromFile $sumPath $asset
    if ([string]::IsNullOrEmpty($want)) {
        return $null
    }
    $got = (Get-FileHash -LiteralPath $assetPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($got -ne $want) {
        Err "checksum mismatch for $asset (got $got, want $want)"
    }
    return $assetPath
}

try {
    $sumPath = Join-Path $tmp 'sha256sum.txt'

    try {
        Invoke-WebRequest -Uri "$base/sha256sum.txt" -OutFile $sumPath -UseBasicParsing -TimeoutSec 15
    } catch {
        Err 'sha256sum.txt download failed'
    }

    # Fetch and verify both assets before installing either, so a failed run
    # cannot leave the two binaries on different versions.
    $primaryPath = Get-OcrAsset $AssetPrefix
    if ([string]::IsNullOrEmpty($primaryPath)) {
        Err "download failed: $AssetPrefix-$os-$arch.exe"
    }
    $extPath = Get-OcrAsset $ExtAssetPrefix
    $installExt = -not [string]::IsNullOrEmpty($extPath)
    if (-not $installExt) {
        Write-Host "note: $ExtBin not found in this release; only $Bin is updated"
    }

    Install-OcrBinary $primaryPath $InstallDir $Bin
    Write-Host "installed $Bin $Version -> $InstallDir\$Bin"
    if ($installExt) {
        Install-OcrBinary $extPath $InstallDir $ExtBin
        Write-Host "installed $ExtBin $Version -> $InstallDir\$ExtBin"
    }

    Show-PostInstallPathNotice $Bin $InstallDir
} finally {
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
