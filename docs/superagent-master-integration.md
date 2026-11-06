# SuperAgent stack and v0.19.1 integration

Historical integration report. On 2026-11-06 the storage boundary changed to the
public `Journal` and `LeasedJournal` interfaces plus the bundled same-host SQLite adapter.
PostgreSQL implementations, dependencies and acceptance tests belong to the host.
The source-preservation and PostgreSQL test statements below describe the original
integration inputs. Current embedding guidance is in the
[SuperAgent README](../superagent/README.md).

2026-11-05. Review-only integration of the unmerged SuperAgent development
stack with current master. This is not a release, deployment, or acceptance of
the individual draft milestones.

## Exact inputs

- Stack top: draft [#183](https://github.com/Icatme/pi-go/pull/183),
  `feat/superagent-m8b-watchdog-refresh`, commit
  `063e3bc75ddc330b77fd4fe25e7a469c46047c9d`, tree
  `7737f6d146340e457bbbfc3d95d880019bcf9391`.
- Master: `7d6669f0c076d2043fa16f4c8ec524e511d8b41c`, including merged
  [#162](https://github.com/Icatme/pi-go/pull/162), GPT-6.1 Terra and v0.19.1.
- Common ancestor: `7be77cb75a9238b3bcc2fcbcb5d28e3b56136b61`.
  The stack has 33 exclusive commits and master has 3, including its merge.

All 26 existing open PRs were still drafts when these inputs were captured.
The dependency spine is:

```text
codex/superagent (5d3402b)
  -> #157 -> #158 -> #159 -> #160 -> #163 -> #164 -> #165 -> #166
  -> #167 -> #168 -> #169 -> #170 -> #171 -> #172 -> #173 -> #174
  -> #175 -> #177 -> #178 -> #179 -> #180 -> #181 -> #182 -> #183
```

Two side branches are source evidence, not additional patches to apply:

- #161 branched from #159 and was adapted into #163. The first two source
  commits retain identical stable patch IDs. The final snapshot optimization
  intentionally differs to reuse the validated terminal snapshot for M2a
  correction evidence; #163 adds composition regressions. See
  [snapshot integration](superagent-m2a-snapshot-integration.md).
- #176 branched from #175. Its five non-worklog source files were copied
  byte-for-byte into #178 on #177, then tested together. See
  [editor/history integration](superagent-security-integration.md).

This integration does not update, close, rebase or merge any of those PRs or
their branches. Its aggregate diff against master includes the entire unmerged
stack, not just the conflict resolution below. Review the original slices for
their design and limitations.

## Combination and preservation

The isolated combination uses the exact remote stack and master commits as
parents. There are no new production-code changes beyond those input trees.

- All 220 paths changed only by the stack and all 5 changed only by master
  retain their exact source blob IDs. This includes the PostgreSQL adapter and
  tests, durable helper/device chain, Watchdog refresh, and Terra provider code.
- The only textual conflict was two append-only tails in the worklog. Keep
  the 206,346-byte common prefix, then the stack's 24,456-byte suffix, then
  master's 937-byte Terra suffix. Each segment remains byte-for-byte intact,
  with no historical text edited, dropped, or duplicated. The integration
  record is this separate document.
- `examples/go.mod` combines master's `github.com/Icatme/pi-go v0.19.1` with
  the stack's `x/sync v0.24.0`, `x/sys v0.49.0` and `x/text v0.43.0`.
  Root module files and both sums retain the stack versions; no dependency
  resolution, upgrade or download is introduced by this integration.
- The Terra release note and package version remain v0.19.1. SuperAgent is
  still draft development code; the existing v0.19.1 tag is not changed and
  does not become a release of this stack.

## Validation contract

Use remote Linux amd64 and the official cached Go 1.26.10 toolchain, with
`GOTOOLCHAIN=local`, `GOPROXY=off`, `GOSUMDB=off`, `GOWORK=off`, and live-model,
example-live and disposable-PostgreSQL test gates disabled. No production or
third-party service is started and no credential is discovered. The exact frozen final tree, command results
and any failures are recorded in the integration PR, not inferred from prior
PR checks.

```powershell
go test -mod=readonly -count=1 ./pkg/pigo -run 'Terra|SamplingMatchesExactModelReasoningConstraints'
go test -mod=readonly -count=1 ./...
go test -mod=readonly -p=1 -race -count=1 ./...
go vet -mod=readonly ./...
go list -mod=readonly -m all
go test -mod=readonly -p=1 -race -count=10 ./superagent/... -run 'Legacy|Migrat|SameChat|Admission|Edited|Takeover|WatchdogRefresh|DurableChain'
# Repeat tests, serialized race, vet and module enumeration from examples/.
```

The repeated regressions cover old journal upgrade, preservation of original
task/provider/delivery identities, independent tasks in the same chat,
admission visibility and editor authority, unknown-effect barriers, and stale
lease/Watchdog authorization and result rejection. Full tests additionally cover
snapshot/cursor forgery, import rollback, durable helper/device subprocess
faults, and exactly one terminal completion for the original operation.

The separate examples module has previously lacked offline metadata needed by
`go list -mod=readonly -m all`. Report that command separately from its
build/test/race/vet outcome; do not treat one as proof of the other or download
dependencies implicitly to hide the limitation.

Independent read-only integration review verified source-blob preservation,
the adapted side-branch patches, module composition and the retained security,
migration and lease contracts. It found no blocking merge defect. Focused
Terra catalog, local HTTP and sampling tests also passed during review.

## Remaining acceptance boundaries

- Real PostgreSQL locking, durability, independent process/host takeover,
  database restart and actual TCP loss have not been tested. Eight real database
  tests remain double-gated and skipped. Offline scripted drivers are not a
  real PostgreSQL acceptance. See [M6b](superagent-m6b.md).
- Real model/platform/device services, production TLS/permissions, host reboot,
  power loss, hostile filesystem races, Windows and macOS remain unverified.
  Loopback HTTP and self-written subprocess fixtures are not real services.
- Terra preserves master's default medium reasoning, explicit off-to-none,
  sampling constraints and 30-minute cache TTL. Its model entry omits native
  mid-conversation-system capability, so it keeps the existing folded-system
  fallback. Generic fallback coverage exists; a Terra-specific dynamic
  system/tool transcript acceptance test and live behavior are not claimed.
- Existing exposed model snapshots are not sanitized by history filtering or
  restart. Read the [upgrade warning](superagent-history-admission.md#upgrade-and-existing-exposure).
  Journal import requires quiesced writers and host-controlled schema/cutover;
  unknown delivery or commit never implies safe retry.
- No user computer, existing credentials, live provider, Codex task or extra
  paid execution, manual CI, remote branch merge, auto-merge or deployment was
  used. Workflows remain manual-dispatch-only; no remote CI pass is claimed.
  PowerShell is unavailable on this Linux host; commands above are portable
  Go invocations, not a claim of PowerShell execution.
