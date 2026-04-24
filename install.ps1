param(
    [string]$Version,
    [string]$BinDir,
    [switch]$Help
)

$ErrorActionPreference = 'Stop'

$owner = if ($env:OWNER) { $env:OWNER } else { 'kunchenguid' }
$repo = if ($env:REPO) { $env:REPO } else { 'ezoss' }
$binary = if ($env:BINARY) { $env:BINARY } else { 'ezoss' }
$apiBase = if ($env:GITHUB_API_BASE) { $env:GITHUB_API_BASE } else { 'https://api.github.com' }
$downloadBase = if ($env:GITHUB_DOWNLOAD_BASE) { $env:GITHUB_DOWNLOAD_BASE } else { 'https://github.com' }

function Resolve-Version {
    param(
        [string]$RequestedVersion,
        [string]$Owner,
        [string]$Repo,
        [string]$ApiBase
    )

    if ($RequestedVersion -ne 'latest') {
        return $RequestedVersion
    }

    $release = Invoke-RestMethod -Uri "$ApiBase/repos/$Owner/$Repo/releases/latest"
    if (-not $release.tag_name) {
        throw "failed to resolve release version for $Owner/$Repo"
    }
    return [string]$release.tag_name
}

if ($Help) {
    @"
Usage: install.ps1 [-Version <tag>] [-BinDir <dir>] [-Help]

Environment overrides:
  OWNER, REPO, BINARY, BIN_DIR, VERSION, GITHUB_API_BASE, GITHUB_DOWNLOAD_BASE,
  EZOSS_SKIP_DAEMON=1   - skip the post-install daemon install and restart
"@
    exit 0
}

if (-not $PSBoundParameters.ContainsKey('Version')) {
    if (Test-Path Env:VERSION) {
        $Version = $env:VERSION
    } else {
        $Version = 'latest'
    }
}

if (-not $PSBoundParameters.ContainsKey('BinDir')) {
    if (Test-Path Env:BIN_DIR) {
        $BinDir = $env:BIN_DIR
    } else {
        $BinDir = Join-Path $HOME 'bin'
    }
}

if ([string]::IsNullOrWhiteSpace($Version)) {
    throw 'version must not be empty'
}

if ([string]::IsNullOrWhiteSpace($BinDir)) {
    throw 'bin dir must not be empty'
}

$archName = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
switch ($archName) {
    'x64' { $arch = 'amd64' }
    'arm64' { $arch = 'arm64' }
    default { throw "unsupported architecture: $archName" }
}

$resolvedVersion = Resolve-Version -RequestedVersion $Version -Owner $owner -Repo $repo -ApiBase $apiBase
$archiveName = "${binary}-${resolvedVersion}-windows-${arch}.zip"
$checksumsName = 'checksums.txt'
$url = "$downloadBase/$owner/$repo/releases/download/$resolvedVersion/$archiveName"
$checksumsUrl = "$downloadBase/$owner/$repo/releases/download/$resolvedVersion/$checksumsName"

$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ([System.Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmpDir | Out-Null

try {
    $archivePath = Join-Path $tmpDir $archiveName
    $checksumsPath = Join-Path $tmpDir $checksumsName

    Invoke-WebRequest -Uri $checksumsUrl -OutFile $checksumsPath
    Invoke-WebRequest -Uri $url -OutFile $archivePath

    $actualChecksum = (Get-FileHash -Algorithm SHA256 -Path $archivePath).Hash.ToLowerInvariant()
    $checksums = Get-Content -Path $checksumsPath
    $expectedChecksum = ($checksums |
        ForEach-Object { $_.Trim() } |
        Where-Object { $_ -match ("\s{2,}" + [regex]::Escape($archiveName) + '$') } |
        Select-Object -First 1 |
        ForEach-Object { ($_ -split '\s+', 2)[0].ToLowerInvariant() })

    if (-not $expectedChecksum) {
        throw "checksum not found for $archiveName in $checksumsName"
    }
    if ($actualChecksum -ne $expectedChecksum) {
        throw "checksum mismatch for ${archiveName}: got $actualChecksum expected $expectedChecksum"
    }

    Expand-Archive -Path $archivePath -DestinationPath $tmpDir -Force

    $binaryPath = Join-Path $tmpDir "${binary}-${resolvedVersion}-windows-${arch}/${binary}.exe"
    New-Item -ItemType Directory -Path $BinDir -Force | Out-Null
    Copy-Item -Path $binaryPath -Destination (Join-Path $BinDir "${binary}.exe") -Force

    Write-Output "installed $binary to $(Join-Path $BinDir "${binary}.exe")"

    # Add BinDir to user PATH if not already there.
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $userPath) { $userPath = '' }
    if (($userPath -split ';') -notcontains $BinDir) {
        $newPath = if ([string]::IsNullOrEmpty($userPath)) { $BinDir } else { "$userPath;$BinDir" }
        [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
        Write-Output "added $BinDir to user PATH; restart your terminal"
    }

    # Best-effort: register the daemon with Task Scheduler and start it.
    if ($env:EZOSS_SKIP_DAEMON -ne '1') {
        $installedBinary = Join-Path $BinDir "${binary}.exe"
        try { & $installedBinary daemon install | Out-Null } catch {}
        try { & $installedBinary daemon restart | Out-Null } catch {}
    }
} finally {
    if (Test-Path $tmpDir) {
        Remove-Item -Path $tmpDir -Recurse -Force
    }
}
