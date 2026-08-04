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

function Invoke-ModuleTests {
	param(
		[Parameter(Mandatory = $true)]
		[string]$ModulePath
	)

	Push-Location $ModulePath
	try {
		Write-Host "==> Testing $ModulePath"
		& go test ./...
		if ($LASTEXITCODE -ne 0) {
			throw "go test failed in $ModulePath"
		}
	}
	finally {
		Pop-Location
	}
}

Invoke-ModuleTests -ModulePath $repoRoot
Invoke-ModuleTests -ModulePath (Join-Path $repoRoot "adapters/langgraphgo")
Invoke-ModuleTests -ModulePath (Join-Path $repoRoot "examples")
