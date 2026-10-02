# Local MCP Codemode example

This executable uses an official SDK in-memory MCP server, one native Go tool,
the QuickJS WASM sandbox, and the existing Agent Runner. It needs no credentials,
network access, Node, or live model. A deterministic `StreamModel` fixture emits
one batch script; replacing `Model` with a configured provider enables generation.

Build and run from `examples` with PowerShell:

```powershell
$env:GOWORK = 'off'
go build -o "$env:TEMP\pi-go-mcp-codemode.exe" ./mcp-codemode
& "$env:TEMP\pi-go-mcp-codemode.exe"
```

The script fetches three pages containing 150 fixture issues with long bodies,
filters open issues inside the VM, and returns the count and three titles. The
model declares only `code`; MCP leaf schemas are discovered inside the sandbox.

The application creates and closes both SDK sessions. Client capabilities are
empty and `MultiRoundTrip.Disabled` is true. The in-memory transport performs no
OAuth or HTTP retries. The adapter calls each tool once and makes no general
at-most-once claim for arbitrary application-owned sessions.

The application and sandbox have explicit five-minute limits. Script header
options may shorten them. Sandbox shutdown uses a separate five-second context and
reports unfinished host calls instead of waiting forever. Permission is checked
after scheduling and again immediately before the SDK tool call.

Only the explicit `list_issues` and `label` leaves are bound. Adding a tool to the
MCP server does not expose it automatically. Child events contain bounded
summaries and no separate child tool messages are added to the model transcript.
Go-side `codemodetool.Report` retains the complete bounded basic call ledger.
