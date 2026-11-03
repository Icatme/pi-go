# Kiro post-merge convergence review — 2026-11-02 UTC

## What this snapshot is

The current baseline is master
[`5dcf3c4`](https://github.com/Icatme/pi-go/commit/5dcf3c4e8d6dd42c8f44d35ee76dac20b61885d9),
integrated by [#137](https://github.com/Icatme/pi-go/pull/137) for v0.18.0.
The original heads of #131–136 are ancestors of this baseline, including the
authored-history opt-in, restored seven-field ToolCall layout and durable JSONL
tests. Their former stacked PR bases are not evidence of missing master work.
Do not reimplement these changes from the historical branch notes.

The [release notes](releases/v0.18.0.md) and
[migration guide](kiro-agent-authored-compatibility.md) define the current public
contract. Kiro remains explicitly registered and caller-configured, not a default
network provider. Historical test results in the worklog are not a fresh rerun.
No real Kiro service/account validation is established by those results.

At 11:21 UTC a read-only GitHub check still found upstream main at
[dd93f2e](https://github.com/mikeyobrien/pi-provider-kiro/commit/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba),
committed2026-10-29T22:21:36Z, release message v0.12.3. No new main delta relative to
the pinned implementation evidence. The earlier tag-list endpoint was unavailable;
this does not claim every branch/release/tag was inspected.

## Implemented and connected, with limits

| Surface | Actual evidence | Still not established |
| --- | --- | --- |
| Request/auth/client metadata | Scoped explicit credentials, caller origin/UA, bounded wire JSON, explicit auth kinds, fake request tests | Real server acceptance, TLS/client fingerprint parity, permission to impersonate any client |
| Runtime/Agent | Explicit buffered, live-text and mixed-live registrations; Auto/session opt-ins; explicit authored tool history with multi-turn permission/cancel and JSONL restore tests | Default installation, live credentials, production entitlement/performance; strict original-wire replay through Agent persistence |
| Live behavior | Pipe fixtures deliver provisional text before terminal; tools withheld until full validated terminal/HTTP completion | Real-stream scheduling/performance and complete upstream event parity |
| Model catalog | Explicit injected fetch, immutable scope/freshness observations, explicit ID namespace; manual exact service-ID runtime regression | Automatic host model registration, inferred capability/pricing, continuing account access |
| IDC refresh | Explicit public one-shot caller config; opaque result, unknown-rotation error marker; optional successful-omission retention | Device/browser login, client registration, automatic source cache/update/store |
| Desktop refresh | Explicit public one-shot community-observed request; profile matching, omission provenance, conservative clock semantics | Official public API guarantee, actual desktop credentials, real login/refresh validation |
| Refresh-to-runtime wiring | Both public refresh APIs -> caller expiry-check source -> registered provider fake; wrong scope/expiry suppress runtime, no hidden refresh | A production credential manager or same-token concurrent refresh safety |
| Secret tests | Fixed errors/opaque formatting, explicit getters; no refresh/client secret in runtime URL/body/headers; access only Authorization | Secure memory erasure or behavior hidden inside caller callbacks |
| Regression found by audit | Close latency previously extended refresh expiry; failed-old-tree reproducer then fixed conservative pre-dispatch anchor | Accurate server issue time or resilience against dishonest/discontinuous caller clock |

## Explicit remaining gaps

- **No default network executor.** Callers supply a trusted callback prohibiting
  redirects/retries/logging and honoring cancellation. Arbitrary callback behavior
  cannot be forced by these helpers. There is no live end-to-end service proof.
- **Credential generation is caller-owned.** Serialize refresh for a generation
  across consumers/processes, fence late results and treat executor-invoked failure
  as unknown rotation. No persistence or silent CLI/IDE import was introduced.
- **External IdP** has a strict offline response component, not a public arbitrary-
  URL sender. A later implementation needs explicit endpoint trust/allowlist,
  redirect policy and ownership rather than accepting token-controlled URLs.
- **Protocol parity remains incomplete:** reasoning/signature replay, richer image
  history, automatic effort/schema policies, metering credits, some upstream EOF
  heuristics and permissive envelope variants are not silently accepted.
- **Persisted authored tool history is implemented**, including absent versus
  empty arguments and exact json.Number values. It is not original-wire replay.
  Agent callers must explicitly select authored registration; direct strict pigo
  callers retain their evidence requirements. Already rounded values or empty
  edits omitted by older snapshots cannot be reconstructed. Do not add a new raw
  persistence bridge to solve a requirement the authored contract already covers.
- **Compatibility remains scoped.** ToolCall positional literals compile, but its
  changed JSON tag affects anonymous-struct assignment and third-party encoders.
  AgentEvent has a new ModelResponse field; downstream positional literals and
  custom runners need review. See the release and migration notes, not a blanket
  claim that all consumers remain source-compatible.
- **OAuth extensions:** unknown fields and local token-size limits remain a strict
  subset, not generic RFC-compliant extension handling. Optional IDC rotation is
  now explicitly supported, but only on complete validated success.

## Converge before expanding

1. Validate the merged SHA, not the former draft tip. Include the root module,
   separate examples module and explicit `agent/testdata/compat_toolcall` probe
   (which `./...` excludes). Record setup failures separately from test failures.
2. Review each downstream consumer against its own pinned version and custom
   runners/serializers before an upgrade. A local replace probe is not a released
   dependency upgrade or proof that a deployment uses the new code. Keep the
   supported subset explicit; no full upstream parity or production-ready claim.
3. Validate the supplied executor independently in a sandbox (redirect/timeout/
   cancellation/body/log policies) before any separately authorized real-service
   smoke test. That authorization was not inferred from development permission.
4. Only then select a concrete missing feature from the above list. Do not add
   unconnected APIs or automatic long-lived source management merely to grow the
   change list. This review does not authorize another merge, release, deployment,
   default network client, login or billable request.

## Post-merge verification boundary

Read-only PR metadata and local git ancestry confirmed #131–136 on the exact
master baseline above. Source inspection also found the integration's original-
history validation and successful-completion-only context observations; these
must not be overwritten by reusing an earlier branch.

The merged Codemode prelude's offline contract suite passed 30/30 in Node VM
and 30/30 with the repository-pinned QuickJS WASM asset. These runs exercise
synthetic host callbacks, not the Go/wazero bridge, real tools or Kiro service.

The initial Linux Go 1.26.2 offline attempt was blocked at dependency setup.
After explicit authorization, the root and examples modules' locked dependencies
were downloaded through proxy.golang.org, without changing go.mod/go.sum. Tests
then ran with GOPROXY/GOSUMDB off, live flags disabled and GOTOOLCHAIN=local:

- Explicit `agent/testdata/compat_toolcall` probe passed (0.005s).
- Root `go test -mod=readonly -count=1 -race ./...` and `go vet -mod=readonly ./...`
  passed (pigo 41.280s, agent 3.948s, codemode 30.425s).
- Separate examples module race and vet passed (mcp-managed 21.122s).
- `go mod verify` passed in both modules. No new CI run was requested.

These are fresh Linux checks, not a rerun of the historical Windows matrix or
real-service validation. This docs-only follow-up changes no production code.

### Bounded downstream probe

The existing ai-relay source snapshot was checked against its current
`main@e55591b70b53485fe02764727264382a73796e84`: all 220 tracked file blobs
matched. A temporary modfile replaced only pi-go with the merged source above;
the consumer's own go.mod remains pinned to v0.17.3 and was not edited.

With its required existing Go 1.26.10 toolchain and downloads/live/DSN disabled,
focused provider/pigo and relay race tests passed (22.705s and 2.267s), as did
focused vet. A broader
attempt also passed ten available packages, but the complete consumer suite was
blocked at setup by uncached jwt/v5 v5.3.1 and pgx/v5 v5.11.0. This does not prove all
downstream packages build, validate database behavior or perform an upgrade.
No other project's dependencies were downloaded for this probe.

See [refresh audit](kiro-refresh-audit.md),
[caller-owned runtime handoff](kiro-refresh-runtime-handoff.md),
[status chronology](kiro-followup-status.md) and [actual test records](PO_AGENT_WORKLOG.MD).
