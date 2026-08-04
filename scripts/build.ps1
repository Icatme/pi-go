param(
    [string]$OutputDir = "bin",
    [string]$GOOS = "",
    [string]$GOARCH = "",
    [switch]$SkipTest,
    [switch]$Release
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$outputRoot = Join-Path $repoRoot $OutputDir

$originalGOOS = $env:GOOS
$originalGOARCH = $env:GOARCH

function Get-BinaryName {
    param(
        [string]$TargetGOOS
    )

    if ($TargetGOOS -eq "windows" -or [string]::IsNullOrWhiteSpace($TargetGOOS)) {
        return "pigo.exe"
    }
    return "pigo"
}

try {
    Set-Location $repoRoot

    if (-not $SkipTest) {
        Write-Host "==> Running tests"
        & (Join-Path $PSScriptRoot "test.ps1")
    }

    if (-not [string]::IsNullOrWhiteSpace($GOOS)) {
        $env:GOOS = $GOOS
    }
    if (-not [string]::IsNullOrWhiteSpace($GOARCH)) {
        $env:GOARCH = $GOARCH
    }

    $targetGOOS = $env:GOOS
    $binaryName = Get-BinaryName -TargetGOOS $targetGOOS

    New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null
    $outputPath = Join-Path $outputRoot $binaryName

    $buildArgs = @(
        "build"
        "-trimpath"
    )

    if ($Release) {
        $buildArgs += @("-ldflags", "-s -w")
    }

    $buildArgs += @(
        "-o", $outputPath,
        "./cmd/pigo"
    )

    Write-Host "==> Building $outputPath"
    & go @buildArgs

    Write-Host "Built: $outputPath"
}
finally {
    $env:GOOS = $originalGOOS
    $env:GOARCH = $originalGOARCH
}
