# Local Docker build script for Windows
# This script prepares the build context for Docker with the go-audible dependency

$ErrorActionPreference = "Stop"

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$BuildReleaseVersion = ""
try {
    $BuildReleaseVersion = ((git -C $ScriptDir tag --sort=-version:refname | Select-Object -First 1) -replace '^v', '').Trim()
} catch {
    $BuildReleaseVersion = ""
}
if ([string]::IsNullOrWhiteSpace($BuildReleaseVersion)) {
    $BuildReleaseVersion = "0.0.0"
}

$BuildCommitRef = ""
try {
    $BuildCommitRef = (git -C $ScriptDir rev-parse --short=12 HEAD 2>$null).Trim()
} catch {
    $BuildCommitRef = ""
}

$BuildTimestamp = (Get-Date).ToUniversalTime().ToString("yyyyMMddHHmmss")
$BuildChannel = "dev"
$BuildDir = New-Item -ItemType Directory -Path (Join-Path $env:TEMP "docker-build-$(Get-Random)")

Write-Host "Preparing Docker build context in $BuildDir..." -ForegroundColor Cyan

# Copy audplexus
Write-Host "Copying audplexus..." -ForegroundColor Cyan
Copy-Item -Recurse -Path $ScriptDir -Destination (Join-Path $BuildDir "audplexus")

# Copy or clone go-audible
$GoAudiblePath = Join-Path (Split-Path -Parent $ScriptDir) "go-audible"
if (Test-Path $GoAudiblePath) {
    Write-Host "Copying local go-audible from ../go-audible..." -ForegroundColor Cyan
    Copy-Item -Recurse -Path $GoAudiblePath -Destination (Join-Path $BuildDir "go-audible")
} else {
    Write-Host "Cloning go-audible from GitHub..." -ForegroundColor Cyan
    Push-Location $BuildDir
    git clone https://github.com/mstrhakr/go-audible.git go-audible
    Pop-Location
}

# Build the Docker image
Write-Host "Building Docker image..." -ForegroundColor Cyan
Push-Location $BuildDir
docker build --build-arg BUILD_RELEASE_VERSION=$BuildReleaseVersion --build-arg BUILD_COMMIT_REF=$BuildCommitRef --build-arg BUILD_TIMESTAMP=$BuildTimestamp --build-arg BUILD_CHANNEL=$BuildChannel -f audplexus/Dockerfile -t audplexus:local .
Pop-Location

Write-Host "Cleaning up build context..." -ForegroundColor Cyan
Remove-Item -Recurse -Force $BuildDir

Write-Host ""
Write-Host "✅ Docker image built successfully as 'audplexus:local'" -ForegroundColor Green
if ([string]::IsNullOrWhiteSpace($BuildCommitRef)) {
    Write-Host "Version stamp: $BuildReleaseVersion.$BuildTimestamp-dev" -ForegroundColor Green
} else {
    Write-Host "Version stamp: $BuildReleaseVersion.$BuildCommitRef-dev" -ForegroundColor Green
}
Write-Host ""
Write-Host "To run:" -ForegroundColor Cyan
Write-Host "  docker run -d -p 8080:8080 -v ${PWD}/config:/config -v ${PWD}/audiobooks:/audiobooks audplexus:local"

