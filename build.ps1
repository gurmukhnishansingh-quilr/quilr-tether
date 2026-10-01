# Cross-compile static tether binaries for every supported platform into dist\.
# Needs only a Go toolchain; the binaries need nothing on the target machine.
$ErrorActionPreference = 'Stop'
$version = if ($env:VERSION) { $env:VERSION } else { (git describe --tags --always 2>$null) ?? 'dev' }
New-Item -ItemType Directory -Force dist | Out-Null
$env:CGO_ENABLED = '0'
foreach ($t in 'windows/amd64','windows/arm64','darwin/amd64','darwin/arm64','linux/amd64','linux/arm64') {
    $os, $arch = $t.Split('/')
    $ext = if ($os -eq 'windows') { '.exe' } else { '' }
    $out = "dist/tether-$os-$arch$ext"
    $env:GOOS = $os; $env:GOARCH = $arch
    go build -trimpath -ldflags "-s -w -X main.version=$version" -o $out ./cmd/tether
    if ($LASTEXITCODE -ne 0) { throw "build failed for $t" }
    Write-Host "built $out"
}
Remove-Item Env:GOOS, Env:GOARCH
