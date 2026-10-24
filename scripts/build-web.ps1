# Install deps (npm ci when package-lock.json exists, fallback to --legacy-peer-deps)
if (Test-Path package-lock.json) {
    npm ci
    if ($LASTEXITCODE -ne 0) { npm ci --legacy-peer-deps }
} else {
    npm install --legacy-peer-deps
}
if ($LASTEXITCODE -ne 0) {
    Write-Host "[FAIL] npm install failed"
    exit $LASTEXITCODE
}

# Build Web UI (required by go:embed dist)
npm run build
if ($LASTEXITCODE -ne 0) {
    Write-Host "[FAIL] web build failed"
    exit $LASTEXITCODE
}
Write-Host "[OK] web build succeeded"
exit 0
