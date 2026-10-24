# Cross-compile Windows amd64 binary (Go embeds internal/server/dist built earlier)
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'

$version = (git describe --tags --always 2>$null)
if (-not $version) { $version = 'dev' }
Write-Host "Version: $version"

New-Item -ItemType Directory -Force dist | Out-Null
$out = 'dist/go-magic-windows-amd64.exe'
$ldflags = "-s -w -X main.Version=$version"
go build -ldflags $ldflags -o $out ./cmd/magic
if ($LASTEXITCODE -ne 0) {
    Write-Host "[FAIL] go build failed"
    exit $LASTEXITCODE
}
Write-Host "[OK] built $out"
exit 0
