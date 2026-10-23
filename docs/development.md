# Development

[Documentation index](README.md) · [Project overview](../README.md)

Use Go 1.26.2 or newer. The repository has two Go modules: the root library and
`examples/`, whose dependency graph is kept separate. Commands below use
PowerShell and run from the repository root unless noted otherwise.

Follow the [repository working rules](../AGENTS.md). Keep provider-neutral
semantics stable before widening provider behavior, and keep the package focused
on its documented built-in providers. Local-model or generic OpenAI-compatible
support requires an explicitly scoped change.

## Build the CLI

```powershell
$env:GOWORK = "off"
.\scripts\build.ps1
.\bin\pigo.exe --help
```

`build.ps1` runs the shared test script before building. Its options are:

| Option | Behavior |
| --- | --- |
| `-SkipTest` | Build without running tests |
| `-Release` | Add stripped release flags |
| `-OutputDir <path>` | Change the output directory from `bin` |
| `-GOOS linux -GOARCH amd64` | Cross-build `bin/pigo` for Linux amd64 |

Cross-building does not execute or validate the target-platform binary.

## Offline tests

Run the root and examples modules independently through the shared script:

```powershell
$env:GOWORK = "off"
$env:PIGO_LIVE_TEST = "0"
$env:PI_GO_AGENT_LIVE_TEST = "0"
$env:PI_GO_AGENT_EXAMPLE_LIVE = "0"
.\scripts\test.ps1
```

For focused changes, start with the affected package:

```powershell
go test -mod=readonly ./pkg/pigo/... -count=1
go test -mod=readonly ./agent/... -count=1
```

To check dependencies, all packages, and vet directly in both modules:

```powershell
go list -mod=readonly -m all
go test -mod=readonly ./... -count=1
go vet -mod=readonly ./...

Push-Location examples
try {
    go list -mod=readonly -m all
    go test -mod=readonly ./... -count=1
    go vet -mod=readonly ./...
} finally {
    Pop-Location
}
```

Use `gofmt -l` on the Go files you changed and `git diff --check` before committing.
The [CI workflow](../.github/workflows/go-tests.yml) defines the full test matrix;
the [agent testing guide](../agent/docs/testing.md) describes runtime coverage.

## Race checks and coverage

```powershell
go test -mod=readonly -race ./pkg/pigo/... -count=1
go test -mod=readonly -race ./agent/skills -count=1
.\scripts\test.ps1 -Coverage
```

The coverage switch targets `pkg/pigo` and forces a rebuild. For equivalent
direct commands:

```powershell
go test -mod=readonly -a ./pkg/pigo/... -coverprofile=coverage.out -count=1
go tool cover -func coverage.out
```

A forced rebuild avoids reusing stale coverage metadata after package file
moves or splits.

## Live provider tests

Live calls are opt-in and separate from the offline suite. The gates are:

| Environment variable | Test scope |
| --- | --- |
| `PIGO_LIVE_TEST=1` | Core provider tests |
| `PI_GO_AGENT_LIVE_TEST=1` | Agent provider tests |
| `PI_GO_AGENT_EXAMPLE_LIVE=1` | Opt-in example tests |

Enable only the suite you intend to run, and provide its credentials:

```powershell
$env:PIGO_LIVE_TEST = "1"
go test -mod=readonly ./pkg/pigo/... -run Live -count=1
```

Provider credentials include `OPENCODE_API_KEY`, `KIMI_API_KEY`, and
`COMMAND_CODE_API_KEY` (or `COMMANDCODE_API_KEY`). Some core Codex tests use the
ignored `01_auth.json` fixture. See the [provider guide](providers.md#credentials-and-local-files),
[agent live-test guide](../agent/docs/testing.md#live-provider-tests), and each
[example's instructions](../README.md#examples) for its specific setup.
