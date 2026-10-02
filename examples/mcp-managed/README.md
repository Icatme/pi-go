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
4. The hidden delete tool is absent from declarations, discovery and execution.
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
