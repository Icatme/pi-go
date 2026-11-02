# Scoped credentials in a completed attempt

Depends on #98 (and the credential constructor from #96). This closes one
internal wiring gap before a public configured module: buffered text/tool delivery
can now obtain credentials from the explicit per-request scope callback rather
than requiring the caller to retain a fixed token in its module.

`runKiroScopedCompletedStream` performs the existing model/limit/name/queue-minimum
preflight, builds and serializes the immutable request body, invokes the credential
source once, verifies exact profile/region scope, then invokes the supplied HTTP
executor once. Unsupported context fails before credentials or transport. Errors
remain fixed and release no stream/tool events. The existing fixed-token entry
points share the same preflight and terminal projection; their contracts remain.

This is **buffered completed delivery**, not realtime text streaming. Whole HTTP
EOF, declared length, response Close, stop metadata and final projection must pass
before a stream is returned. No tool executes here. Agent schema/permission/current
context checks remain required. A later cancellation cannot revoke an already
completed Result, as documented in the existing queue adapter.

The adapter does not cache credentials, retry, refresh, discover local auth or use
a default HTTP client. Requests necessarily contain secret authorization headers;
trusted callbacks can retain them, so this is not zeroization or sandboxing. The
source/executor/body Read/Close must honor context; a blocked callback cannot be
forcibly stopped. Source scope is a caller assertion, not verified account ownership.

Original fake tests exercise fresh credentials on two separate invocations,
text and tool termination, seven preflight rejection cases with zero source/HTTP
calls, seven credential/transport failures with no exposed stream, and request/allowlist isolation when a source mutates
its captured conversation/schema inputs. This wires
locally migrated semantics together; it does not copy upstream tests or prove
private service access is supported. Existing upstream mapping remains in the
protocol/request/history migration documents.

Still absent: public module/config factory, explicit registration integration,
account/login/refresh, real-service validation, and durable raw tool replay. No
network service, real credential, tool side effect, CI or deployment is exercised.
