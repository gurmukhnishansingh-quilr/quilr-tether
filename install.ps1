# Install tether on Windows (no admin rights needed):
#   irm https://raw.githubusercontent.com/gurmukhnishansingh-quilr/quilr-tether/main/install.ps1 | iex
# Options (environment variables):
#   TETHER_VERSION      tag to install, e.g. v0.1.0 (default: latest release)
#   TETHER_INSTALL_DIR  target directory (default: %LOCALAPPDATA%\Programs\tether)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$repo = 'gurmukhnishansingh-quilr/quilr-tether'

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "Unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

$tag = $env:TETHER_VERSION
if (-not $tag) {
    $tag = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name
}
$version = $tag.TrimStart('v')
$file = "tether_${version}_windows_${arch}.zip"
$base = "https://github.com/$repo/releases/download/$tag"

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("tether-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "Downloading tether $tag for windows/$arch..."
    Invoke-WebRequest "$base/$file" -OutFile (Join-Path $tmp $file) -UseBasicParsing
    Invoke-WebRequest "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') -UseBasicParsing

    $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match " $([regex]::Escape($file))$" }
    if (-not $line) { throw "$file is not listed in checksums.txt" }
    $expected = ($line -split '\s+')[0]
    $actual = (Get-FileHash (Join-Path $tmp $file) -Algorithm SHA256).Hash.ToLower()
    if ($expected -ne $actual) { throw "Checksum mismatch for $file" }

    Expand-Archive (Join-Path $tmp $file) -DestinationPath $tmp -Force

    $dir = if ($env:TETHER_INSTALL_DIR) { $env:TETHER_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\tether' }
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    Copy-Item (Join-Path $tmp 'tether.exe') (Join-Path $dir 'tether.exe') -Force
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not (($userPath -split ';') -contains $dir)) {
    [Environment]::SetEnvironmentVariable('Path', ($(if ($userPath) { "$userPath;$dir" } else { $dir })), 'User')
    $env:Path = "$env:Path;$dir"
    Write-Host "Added $dir to your user PATH (open a new terminal to pick it up)."
}
Write-Host "Installed tether $version to $dir\tether.exe"
Write-Host 'Get started: tether profile add <name> --type quilr --region auto'
