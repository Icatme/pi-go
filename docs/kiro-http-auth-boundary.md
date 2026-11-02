# Explicit request credentials and future HTTP boundary

This draft joins #68 request construction with #69 explicit auth headers and
adds only `newKiroOfflineRequestWithToken`. It builds an unsent request from
explicit caller inputs; it does not register or execute a provider. Original
branches and master are unchanged. No actual credential or service is used.

## What the small bridge does

- Takes kind/token as direct arguments, not environment/CLI state or persisted
  configuration. No implicit key fallback, token refresh or JWT authority guess.
- Runs the auth and request validators. Any failure yields nil request and a
  fixed, non-token-bearing error, never a partly constructed sensitive request.
- Adds auth only to the new header map. It does not put tokens in URL, JSON body,
  logs, exceptions or global state. A later plain request does not inherit them.
- Checks cancellation after construction; no claim of interruptible JSON work.

The returned request **contains plaintext Authorization** and can expose it if a
caller dumps/logs it. No generic secrecy guarantee, account authorization or
memory zeroization is provided. Callers own secret lifetime and must not mutate
input objects concurrently. This is an explicit injection seam, not a safe-to-
log request model or a complete credential manager.

## Future transport contract (design, NOT implemented)

1. **Injected executor:** accept a caller-supplied RoundTripper-like boundary,
   with no fallback to http.DefaultClient/DefaultTransport. First regressions
   use local deterministic fake transport only. Constructor alone never sends.
2. **Binding:** resolve the runtime region from explicitly verified profile /
   account context before requesting credentials; reject mismatches. The current
   region syntax check and opaque profile string are insufficient for this.
3. **One attempt:** no automatic retries/refresh/credential substitution until
   billing and uncertain-outcome semantics are defined. Do not treat transport
   EOF as completion, replay tool output or hide a failed attempt in usage totals.
4. **Redirects:** reject 3xx rather than forwarding bearer headers. No arbitrary
   alternate host, downgrade, proxy or caller-supplied URL is admitted implicitly.
   GetBody is not permission to replay a request. Proxy policy must be explicit.
5. **Response ownership:** always close a received body; validate status and
   event-stream content type before decoding, bound error bodies and never echo
   raw errors/headers/tokens. Body+error combinations must not yield success.
6. **Cancellation/timeouts:** caller context controls the actual blocked transport
   and reader, with no leaked goroutines. No partial tools escape after a late
   error/cancel/conflicting terminal event. Synthetic cancellation tests first.
7. **Telemetry:** only allowlisted status/category/attempt identifiers; never dump
   full request/header/body or opaque upstream error text. Tokens stay out of
   error chains, retry decisions and persisted diagnostics.
8. **Provider adapter:** convert verified offline results explicitly, including
   context_usage (not assistant text or token billing), stop/usage and reasoning
   provenance. Raw argument editing/persistence remains a separate unresolved
   contract; don't silently discard parsed maps to force compatibility.

These are design acceptance gates, not claims of existing transport behavior.
Real service use additionally requires explicit service-contract, credential /
account and spending scope; offline client simulation does not establish that.

## Verification

Go regression sources use fake LOCAL_ONLY credentials, check exact auth fields,
absence from URL/body, no cross-call inheritance, and nil requests on invalid
kind, endpoint, nil/cancelled context. Existing #68 fake response tests remain.
Go compiler/gofmt/unit/race tests have NOT run. No software installation, CI,
actual RoundTrip, token lookup, real account call, master merge or deployment.
The next meaningful validation gate remains an approved compatible Go toolchain,
then focused tests, all Kiro tests, race and the repository suite.
