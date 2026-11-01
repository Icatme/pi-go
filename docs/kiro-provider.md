# Kiro provider: staged community integration

## Current status

This draft adds an **unregistered, internal AWS event-stream framing decoder**
and offline test sources. It does not make Kiro usable in pi-go. No provider
dispatch, model catalog, HTTP requests, authentication, history mapping, event
semantics, or tool execution is enabled. No new dependency is introduced.

The selected community reference is
[pi-provider-kiro](https://github.com/mikeyobrien/pi-provider-kiro), inspected at
v0.12.3 / `dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba` on 2026-11-01.
It is not a native pi-mono provider or an official Kiro API guarantee. Its
package metadata declares MIT, but the inspected repository has no LICENSE
file; this change independently implements the public
[Smithy AWS event-stream specification](https://smithy.io/2.0/aws/amazon-eventstream.html),
not copied community implementation code. Future reuse requires license review.

Kiro's [official pricing FAQ](https://kiro.dev/pricing/) describes restrictions
on third-party harness use bypassing native interfaces. Community protocol
compatibility does not establish permission to use a subscription through it.
Confirm the permitted integration route before enabling live connectivity.

## Decoder contract

- Validate the prelude CRC before trusting lengths; validate the complete
  message CRC before returning headers or payload.
- Require an explicit caller-selected wire-message size budget. Decoded
  maps and strings consume additional memory; this is not a total heap cap. This
  local resource policy is not enforcement of Smithy's service-side size caps.
- Decode all ten header wire types, reject duplicate/invalid UTF-8 header names,
  invalid UTF-8 strings, unknown types, truncated fields and invalid lengths.
- Stop permanently on any read/protocol error; never resynchronize into a later
  frame or replay output after a partially delivered response.
- Preserve raw timestamp milliseconds and UUID bytes without interpreting them.
- One goroutine owns the decoder. The caller owns closing/cancelling the reader.
  Returned byte values belong to that decoded frame, not a shared scratch buffer.
- Framing is not application success. Future callers must validate message/event
  headers and JSON schemas and surface exception/error frames before any output.

## Remaining gates

1. Run formatting, targeted tests and fuzzing with an approved Go toolchain,
   then review and merge this foundation separately from provider registration.
2. Define explicit host-provided credentials, region and profile configuration;
   no scanning HOME, IDE/CLI token stores, SQLite writes or refresh subprocesses.
   Do not silently cross account boundaries or perform automatic model discovery.
3. Add offline request/history/tool mapping and event-to-pi-go state-machine
   fixtures, including cancellation, error events, malformed JSON, tool argument
   fragments, usage accounting and no retry after first visible output.
4. Review allowed endpoint/auth contracts and secret-safe diagnostics. Keep model
   catalogs isolated by account and session. No request/token/body logging.
5. Only then consider opt-in provider registration and separately authorized
   live validation. Never interpret offline decoder tests as service acceptance.

## Validation record

On 2026-11-01 the remote base was master
`afaebd607e1f6ed0758fc5cf830215d304b063e5` (v0.17.3). The available `go`
command was not the Go compiler (`Go: Unknown option: version`); no `gofmt`
was available. **Go compilation, unit tests, fuzzing, race checks and formatting
verification have not run.** No toolchain was installed and no live API or local
user-machine action was performed. The golden frame was generated independently
using Python's standard-library `struct` and `zlib`; this does not validate Go
execution. Keep this PR draft until the checks below pass:

```text
gofmt -w pkg/pigo/provider_kiro_eventstream.go pkg/pigo/provider_kiro_eventstream_test.go
go test ./pkg/pigo -run TestKiroEventStream -count=1
go test ./pkg/pigo -run '^$' -fuzz FuzzKiroEventStreamDecoder -fuzztime=30s
go test -race ./pkg/pigo
go test ./...
```
