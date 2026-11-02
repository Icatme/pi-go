# Scoped credentials for the internal live-text worker

This increment connects the existing explicit scoped credential source to the
existing incremental text worker. It does not change public registration, select
transport, enable network access, or make the buffered module realtime.

`startKiroScopedTextStream` validates budgets, identities and the no-tools subset,
then serializes the request before calling the credential source once. Region and
profile scope must match. Source errors, cancellation and invalid credentials
return a fixed error without starting a worker. The injected executor remains
mandatory; credentials are neither discovered nor refreshed/cached here.

The shared worker delivers actual provisional text before terminal bytes arrive.
Only a validated terminal, EOF/length checks and successful body close permit a
successful final result. Late failure can invalidate already displayed preview;
it never retries, releases tool events or preserves executable partial evidence.
Callers must consume Events or Close and read Result once. Trusted source,
executor and body operations must cooperate with context; this code cannot
preempt arbitrary callbacks. Headers contain secrets and must not be logged.

## Independently authored scenario mapping

Community baseline: pi-provider-kiro dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba,
`src/oauth.ts` scoped credentials and `test/stream.test.ts` incremental text/error
scenarios. These are behavior-oriented synthetic tests, not copied fixtures or
proof of compatibility with the private service.

- A pipe withholds terminal metadata until the host receives TextDelta: actual
  incremental delivery, one source/executor call and one body close.
- A source mutating its captured conversation cannot change serialized content.
- Invalid input/budgets reject before the source; scope/token/source/cancellation
  failures reject before HTTP.
- Close failure, truncated terminal and unsolicited tool output produce no Done
  or tool lifecycle events and an empty error result, without fallback/retry.

No real service, credential, billing or tool execution is used. The live path
still rejects declared tools. Selecting a future live-text vs buffered-tool mode
must happen before the request, not by replaying or retrying a partial response.
