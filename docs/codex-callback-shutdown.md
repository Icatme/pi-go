# Codex CLI callback cleanup

The CLI callback listener keeps its existing two-second graceful shutdown budget so in-flight success and error pages can finish. If `http.Server.Shutdown` returns an error, `Close` now closes the remaining server connections. Previously an idle preconnection or incomplete HTTP header could survive the expired shutdown deadline even after the method returned.

This is related to upstream pi issue 11311, but not the same MCP implementation: pi-go MCP deliberately leaves callback listeners to the host. No OAuth token, browser, credential storage, redirect or retry policy is changed. The existing callback timeout and cancellation behavior remain unchanged. Repeated cleanup is safe.

Closing connections does not forcibly terminate arbitrary noncooperative handler goroutines or hijacked connections. This patch is scoped to the CLI-owned ordinary HTTP callback listener. Local synthetic tests verify idle and incomplete-header sockets, full success/error pages, cancellation, repeated close, and graceful completion of an active response. No real sign-in was performed.
