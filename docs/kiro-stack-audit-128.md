# Kiro integration audit: frozen PR #128 snapshot

Audit date: 2026-11-02 UTC. **Not ready to merge or release:** one confirmed
external Go source-compatibility blocker remains. This report does not approve a
breaking release, merge any PR, or certify real Kiro service compatibility.

## Reproducible scope and inventory

- Remote master: `afaebd607e1f6ed0758fc5cf830215d304b063e5` (v0.17.3 / #49).
- Audited remote [#128](https://github.com/Icatme/pi-go/pull/128) HEAD:
  `53b7536837aa2d80ab54e79382b1e94547b8c6a2`.
- Exact audited tree: `64e1ede546fa7490a5b927e9648b0fadd77bd815`; local test
  commit `b7ae76e726efa085d31ce7a0f334664b9dd8fce1` has that same tree.
- Remote ancestry: ahead 145 commits / behind 0 versus the above master;
  23 merge commits. Local same-tree reconstruction is ahead 144 / behind 0,
  with 22 merge commits; only tree equality is used to transfer local file
  statistics and test evidence.
- 220 changed paths, **22,723 additions / 17 deletions** from local Git numstat
  over the verified identical tree. GitHub compare omits line statistics for its
  final 23 paths; summing its partial file statistics undercounts the change.
- 75 documentation paths (+4,741), 60 production Go paths (+5,482/-11),
  82 Go test paths (+11,971/-6), 3 fixture/other paths (+529).
- 320 added top-level `Test` declarations, not 320 total table cases.
- No changes to go.mod, go.sum, .github, examples or cmd in this snapshot.

All 79 PRs #50–128 were open, draft and unmerged when checked. Mergeability was
true against each PR's own base; that is not whole-stack acceptance. Of their
current HEADs, 73 are ancestors of #128. #64 and #67 have reconstructed commit
identities, but all three changed files from each are byte-identical in #128.
Thus 75 PRs are represented: #50–57, #61–72, #74–128.

Not included: independent master-based #58 (codemode JSON), #59 (dynamic
invocation key comparability), #60 (terminal error evidence), and design-only
#73 (`docs/kiro-host-adapter-plan.md`, superseded by later implementation docs).
Do not claim these four were merged or that #64/#67 fixes are missing merely
because their current commit identities are absent.

#128 itself targets #127 (`feat/kiro-public-desktop-refresh`) and changes five
paths. Merging #128 into that base would **not** integrate the stack into master.

## Actual validation and gaps

Cloud Linux amd64, official Go 1.26.2, existing cached dependencies,
GOTOOLCHAIN=local, GOPROXY=off, GOSUMDB=off, live-test flags disabled:

- `go test -mod=readonly -count=1 -race ./...`: passed across the root module;
  pkg/pigo 30.668s. This run was uncached.
- `go vet -mod=readonly ./...`: passed in the root module.
- Independent reviewer ran focused race tests in agent and pkg/pigo covering
  raw evidence, context diagnostics, API preflight/registry concurrency and Kiro
  host contracts: passed. Other independent reviews were static, not execution.
- Root `go list -mod=readonly -m all`: blocked by uncached transitive module
  metadata (including tidwall/pretty v1.2.0), not a root test failure.
- Separate examples module full test/vet: blocked by unavailable offline
  dependencies including Mattermost public v0.2.1, x/net v0.46.0 and x/sync
  v0.23.0. examples/switch-chat passed; the remaining examples were not verified.
- No dependency downloads, Windows/macOS/ARM runtime matrix, release-package
  acceptance, real credential/service calls or deployments in this audit.
- Workflows are manual workflow_dispatch; zero checks is **not** green CI.

## Confirmed release blocker: public ToolCall layout

`agent.ToolCall` gained private `providerEvidence` before its seven formerly
exported fields. This breaks previously legal external unkeyed literals, even
though JSON fields stayed unchanged. An actual external compile probe was run:

```go
package compat_test
import "github.com/Icatme/pi-go/agent"
var _ = agent.ToolCall{"id", "original", "lookup", "namespace", nil, nil, "signature"}
```

The same `go test -mod=readonly /tmp/kiro_toolcall_compat_test.go` passed against
an archive of the pinned master and failed against #128 with unexported-field /
too-few-values errors. Root-module tests cannot establish external source
compatibility. APIModule layout preservation does not resolve this separate case.

The current required path is to preserve the public layout. Alternatives below
are design analysis, not authorization to accept the break:

1. A versioned breaking release with keyed-literal migration would be a distinct
   product decision requiring new explicit authorization. It is **not an accepted
   resolution for this task**; migration documentation alone does not fix this
   blocker.
2. Preserve ToolCall's public layout with a separately designed, message-owned,
   immutable raw-evidence capsule. This must first prove message/clone lifecycle,
   serialization and logging behavior, edit invalidation and session isolation.
   An existing Metadata slot avoids a global map but introduces visible metadata
   or custom JSON behavior; whole-message copies can cross sessions. Empty or
   reused SessionID is not a sufficient owner identity. Standalone ToolCall clones
   would no longer carry evidence. No safe implementation is claimed here.

Do not replace this known source break with a global identity cache, ID-only
lookup, raw-to-float-to-JSON reconstruction, silently serialized secret evidence,
or a capsule that can attach another session's evidence. Reverting the evidence
bridge also removes the tested Kiro raw-argument multi-turn guarantee; it is not
an equivalent compatibility fix.

## Remaining upstream/service differences

Community baseline remains pi-provider-kiro v0.12.3 / dd93f2e (main checked at
06:43 UTC). It is not a service authorization or a complete official protocol.

- Buffered and explicit incremental adapters, tool release after final validation,
  catalog and caller-owned refresh paths have fake/offline test evidence, not real
  account acceptance. Injected executors/readers must honor cancellation and
  prevent redirects, retries, logging and credential leakage themselves.
- No automatic login, credential discovery, token persistence, account cache or
  refresh scheduling. Single-attempt refresh failure after executor handoff can
  mean unknown server-side rotation; ExecutorInvoked is not proof of wire delivery
  and never makes retry safe. Omitted IDC refresh-token retention is opt-in, not a
  promise that the old token is valid. Concurrent refresh ownership is the caller's.
- Strict accepted schemas, fields, token formats and budgets intentionally reject
  some extensions; this is not a generic RFC-complete OAuth client. Desktop and
  IDC routes are explicit; external-IdP refresh has no public sending workflow.
- Reasoning/signature replay, image history, some option/usage/metering semantics,
  and persisted raw-argument tool-session restoration remain limited/unsupported.
- User-selected client/header emulation is explicit configuration, not silently
  enabled identity copying or proof that the service accepts the chosen identity.
- Incremental text is provisional and cannot be retracted after a late failure;
  tools still require successful terminal validation plus host permission checks.

## Minimal integration and Codex review plan

1. Keep drafts unmerged. Resolve ToolCall source compatibility while preserving its public layout first;
   do not describe the stack as merge-ready solely because tests pass.
2. Review the fixed tree above in subsystem slices: wire parser/state machine;
   request/history/raw evidence; HTTP/secret/refresh boundaries; registry and Agent
   lifecycle; public API/layout and documentation. Pay special attention to
   cancellation after tool events, late Close failures, raw numeric precision,
   edited/restored histories, scope assertions and unknown refresh outcomes.
3. Inspect #58/#59/#60 separately. Do not blindly replay #64/#67 or require the
   superseded #73 design file as if it were an unmerged production fix.
4. Once blockers are resolved, prepare one reviewed integration branch/PR against
   then-current master, preserving or clearly accounting for ancestry. Reconcile
   any newer master commits and rerun uncached root race/vet plus external-source
   compatibility probes on the exact final tree. Do not mechanically merge all
   79 drafts. No integration-to-master PR or merge was performed by this audit.
5. Resolve examples dependency availability and run its separate module checks;
   define supported platform/release acceptance before claiming those passed.
6. Real-service smoke testing requires a separately authorized account/credential,
   service-entitlement/identity decision and bounded-call plan. Never infer it from
   fake fixtures or run it merely to close a checklist box.
