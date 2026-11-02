# Offline HTTP request assembly

This draft joins #62's request body and #65's client headers without modifying
those source branches or master. It adds `newKiroOfflineHTTPRequest`: pure
construction of a POST `*http.Request`, not a transport/provider execution path.
The inherited #61 integration and all existing strict subset limits remain.

The explicit region is limited to one 63-byte DNS label and syntax-checked and used only in
`https://runtime.<region>.kiro.dev/generateAssistantResponse`. The caller cannot
supply an arbitrary URL, userinfo, port, query, fragment, path or alternate host.
Reference: pinned community plugin dd93f2e `src/endpoints.ts` L44–49 and
`src/stream.ts` L663–666 / L1036–1051. Region syntax is NOT an endpoint availability,
region allowlist, profile consistency or entitlement check. No SSO-to-runtime
mapping or profile inference is performed. Callers select the exact runtime
region explicitly; inconsistent profile/region pairs remain an integration gap.

Body and headers come exclusively from the strict builders; each request owns
its serialized body/header map and retains the supplied context. Nil/already
cancelled context and invalid body/header/region input return no request.
Cancellation is checked again after construction, not promised to preempt JSON
marshaling. Caller inputs must not mutate concurrently while building.
Standard NewRequest's GetBody support is not an automatic retry policy. A future
http.Client can replay a body on 307/308 redirects; its redirect/replay policy
must be constrained separately and is not implemented by this constructor.

## Tests and limits

Tests assemble text/tool fixtures, check the exact host/path/method/content length
and declared UA, mutate input schemas/output maps to check ownership, and reject
URL/header injection and cancelled context. A test-local RoundTripper returns
only synthetic event-stream bytes and never delegates to DefaultTransport or
any client. The returned bytes are explicitly passed to the offline collector.
This tests the fixture composition, NOT a production status/content-type handler,
redirect policy, streaming timeout, retry/accounting logic or registered provider.

Go compilation, gofmt, tests and race have NOT run (no usable approved compiler).
No real RoundTrip, default transport, credentials, service request, installation,
CI dispatch, master merge or deployment has occurred. The test source alone is
not proof that the Go code compiles or that Kiro accepts these requests.

Authentication headers and explicit credential lifecycle are still separate work.
A real send path must define authorized service use, endpoint/account binding,
redaction, safe redirects, status/error classification and bounded response-body
ownership, plus deterministic fake-transport regressions, before live validation.
