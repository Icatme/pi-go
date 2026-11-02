# Caller-owned refresh to runtime handoff regression

This test-only integration connects public IDC and desktop refresh APIs to an
explicit caller-owned Credentials callback and registered buffered provider using
synthetic HTTP responses. No production source manager, login, store or automatic
refresh behavior is added.

The caller obtains a refresh result, keeps the scope and conservative expiry,
then deliberately supplies `result.Credential()` through its own source. The
fixture's source checks expiry before returning it; the registry itself has no
implicit expiry knowledge. The runtime request must carry only the new access
token in Authorization, never old/new refresh token or client secret in body or
headers. Returned access text is also prohibited from the JSON conversation body.

Scenarios for both IDC and desktop:

1. Valid result reaches one runtime request; when caller time reaches expiry, a
   subsequent source rejection produces an error without a second runtime call.
2. Initially expired result is rejected before runtime.
3. A different registered profile rejects the returned credential's scope before
   runtime, rather than silently changing scope.

All scenarios perform one explicitly invoked refresh only. Registering a module
invokes neither source nor runtime nor refresh. Rejecting a source does not trigger
a hidden refresh, retry or alternate credential lookup. Private test helpers construct fixtures and
encode synthetic AWS event-stream frames; behavior under test uses public refresh,
registration and StreamSimple APIs. No external tools execute.

These tests establish component wiring and secret separation, not real token
validity/account access, server endpoint compatibility or a complete session
credential manager. Caller must still serialize token-generation refresh, fence
late results and handle unknown rotation outcomes. No credential persistence or
service use has been authorized by this test.
