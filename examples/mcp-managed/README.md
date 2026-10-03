# Managed MCP and Codemode

This deterministic example starts a real loopback HTTP MCP server and uses the
managed `mcp.Manager`, `agent/toolset` and ordinary Agent Runner. It requires no
model API key, external service, OAuth browser or UI acceptance.

From the `examples` module, compile first and run the executable:

```powershell
$env:GOWORK = 'off'
go build -o ./mcp-managed.exe ./mcp-managed
./mcp-managed.exe
```

The example checks this sequence:

1. The first model request declares `code`, `tool_search` and three virtual
   resource tools. The remote issue schema is not preloaded.
2. `tool_search` selects the allowed deferred issue tool. The next request gets
   its current declaration reconstructed from the managed connection.
3. A script uses `Promise.all` for three issue pages, filters 150 rows in the VM,
   emits three titles and reads a bounded text resource through
   `tools.read_mcp_resource`.
4. Save the small filtered result and a resource reference in the trusted session,
   add a complete second turn, and compact the first conversation window.
5. The hidden delete tool is absent from declarations, discovery and execution.
   Large fixture bodies stay outside model messages. Exactly three tool calls
   and one resource read occur; the application prints one final JSON line.

Expected output:

```json
{"openCount":50,"titles":["Issue 3","Issue 6","Issue 9"],"guide":"Review the first three open issues.","hiddenAbsent":true}
```

The host supplies the identity/authentication epoch, an explicit permission
allowlist, and trusted snapshot-scope provenance. Issue IDs are strings in a
schema that permits them; the example does not coerce numeric IDs beyond the
JavaScript safe-integer range. Calls still use the Agent child lifecycle, and
the sandbox, manager and loopback HTTP server have bounded cleanup. Its owned
HTTP transport closes idle connections after the manager, before server shutdown.
Replace the model fixture with a configured provider to exercise actual script generation;
this local run proves the execution plumbing, not live model behavior.

To keep the session and resume in a **second process**:

```powershell
./mcp-managed.exe -session ./managed-session.jsonl -phase save
./mcp-managed.exe -session ./managed-session.jsonl -phase resume
```

`save` creates a new JSONL v2 file and refuses to overwrite an existing file.
`resume` reopens it with the same host identity/epoch, restores the tool selection
through `RunBinding.TrustedScope`, rebuilds executors from a new MCP connection,
and reads the saved JSON after compaction. It does not fetch the three issue
pages again. It reauthorizes and reads the saved resource URI; the output includes
`"resumed":true`. The tests run both phases in separate compiled test processes.
Without `-session`, the default save phase uses a temporary file.

See the [session contract](../../agent/session/README.md) for JSONL v2's explicit
format break, atomic commits, locks, bounds and interrupted-call handling. A
session ID alone is insufficient provenance. Current hidden/permission rules
still apply, and no credentials or executable closures are saved.
