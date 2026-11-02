# Kiro event, error and tool scenario migration

Reference: community plugin `mikeyobrien/pi-provider-kiro` at
`dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba`.
This is independently authored Go test code and synthetic fixture data, not a
copy or translation of upstream source text/helpers. No private workspace paths,
URLs from invoke fixtures, credentials, auth soak scripts or live requests are used.

Base: `test/kiro-go-validated` at
`207957c34433f6074393d4d4efc394138ec79c91` (#75). The tested local
baseline has tree `85aa59d4df6e58953839f234399332f6ecd0e956`; the only
subsequent baseline change is docs/kiro-go-validation.md, verified by Git comparison.

## Scenario mapping

| Upstream reference | Go coverage | Deliberate differences |
| --- | --- | --- |
| protocol-regressions.test.ts: repeated content and metadata completion | Repeated Unicode deltas remain separate, concatenate without loss, explicit usage stays intact | Collector parts, not registered host stream events |
| protocol-regressions.test.ts: empty END_TURN and refusal/length stops | Six terminal cases including empty completion and unsupported future stop | No provider retry layer is invoked |
| event-parser.test.ts: tool start/continuation/stop and object placeholder | Original nested/escaped JSON split at every ASCII position; one-byte wire reader; large integer spelling and tool identity retained | Strict structured tools only; no map reconstruction or EOF auto-flush |
| event-parser.test.ts: exception aliases; errors.test.ts classification | Eight known exception names preserve safe identity, discard staged output/body, classify without sent retry; unknown names fail closed | No arbitrary service message/body text exposed or substring reason extraction |
| tool-use-parser.test.ts: prose tool dialects | Three synthetic dialect examples remain exactly text, never structured tool parts | Intentional NOT IMPLEMENTED: text-to-tool extraction, invented IDs, implicit executable fallbacks |
| event-parser.test.ts: fallback/metering/redaction | Unknown text/usage shapes, raw credit events and redacted-only reasoning are rejected | No fail-open shape routing, silently ignored members or credit-to-token coercion |
| errors.test.ts: REQUEST_BODY_INVALID versus input-size/quota | Exact reason classification stays distinct and never authorizes sent replay | No capacity/credential refresh loops or remote diagnostics |
| helpers/event-stream.ts: missing ordinary event content type | Ordinary event without :content-type is rejected; positive typed control accepted | Explicit compatibility mismatch; no claim original helper bytes are accepted |

These seven new top-level tests supplement the existing framing CRC/truncation,
metadata consistency, tool budgets and context-pressure tests rather than replacing
them. Tool JSON character splits cover ASCII spelling and JSON escapes; Unicode
response wire fragmentation is covered separately. This is not an Agent persisted
history/tool execution or service acceptance test.

## Verification

Cloud official Go 1.26.2; no package install or download for this change. Cached
dependencies only with GOPROXY=off, GOSUMDB=off, GOTOOLCHAIN=local and both live
switches PIGO_LIVE_TEST=0 / PI_GO_AGENT_LIVE_TEST=0. Independent static review found
no blocker; its tool-kind and unknown-exception redaction suggestions were applied.

- gofmt applied to the new Go file.
- Focused TestKiroPort tests passed.
- All TestKiro tests with race passed on the final assertion-strengthened file.
- Full pkg/pigo result is recorded in the PR.
- No GitHub CI, real service request, paid model invocation, master merge or deployment.

PowerShell commands, run from module root with the above environment:

```powershell
go test ./pkg/pigo -run '^TestKiroPort' -count=1
go test -race ./pkg/pigo -run '^TestKiro' -count=1
go test ./pkg/pigo -count=1
```
