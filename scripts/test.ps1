param(
	[switch]$Coverage,
	[string]$CoverProfile = "coverage.out"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Set-Location $repoRoot

if ($Coverage) {
	$coverProfilePath = (Join-Path $repoRoot $CoverProfile)

	Write-Host "==> Running pkg/pigo coverage with forced rebuild"
	& go test -a ./pkg/pigo/... "-coverprofile=$coverProfilePath" -count=1

	Write-Host "==> Summarizing coverage"
	& go tool cover "-func=$coverProfilePath"
	return
}

go test ./...
