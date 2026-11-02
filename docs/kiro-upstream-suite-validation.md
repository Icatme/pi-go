# Combined upstream-scenario migration validation

2026-11-02 UTC. This is a tested integration of three independent, test-only
scenario migrations, not a claim that the entire community plugin suite or a
live provider has been ported. All production code remains unchanged from #75.

## Preserved source branches

Base #75: 207957c34433f6074393d4d4efc394138ec79c91.
Three ordinary two-parent merges retain:

| PR / source commit | Scope | Detailed mapping |
| --- | --- | --- |
| #76 a25e82a5061cea80b6e6190671b99398632ba29b | Event framing, text/tool observations, exceptions and strict protocol differences | [Protocol mapping](kiro-protocol-test-migration.md) |
| #77 c7b752d254a6c35ac9d8db2d25903b0f0e43f4ac | History/transform order, raw argument preservation, empty result carriers and rejection instead of repair | [History mapping](kiro-history-test-migration.md) |
| #78 611fd1cbd2ffb38723bb492e5c00575edeecda1b | Request/profile/region, explicit auth and client identity, preflight/cancellation | [Request/auth mapping](kiro-upstream-request-test-mapping.md) |

Pinned upstream throughout: mikeyobrien/pi-provider-kiro
`dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba` (v0.12.3).
Tests and fake data were independently authored; no upstream implementation,
substantial test text or real-session fixture corpus was copied. Source links
and intentional semantic differences are recorded in the individual mappings.
No assertion of complete upstream license provenance is made.

## Exact tested tree and independent review

Local combined commit: 4a4b872b1d76b43bdefbfad5f03de6efb1fc85a8.
Equivalent remote commit: 22d18f61a8cf41b6466200821be71cec7db690d4.
Exact common tree: 15ead1b638ded81dfa8b9ca8dcd68a3180ce36d3.
Only six new test/document files were added before this report. No conflict or
manual production edit occurred. Independent review verified normal ancestry,
each merged file blob as the exact source union, clean worktree, and no helper
collisions. The reviewer also executed the three migrated groups jointly.
This subsequent report-only change does not modify Go source.

## Actual combined validation

Official cloud Go 1.26.2; pinned already-cached dependencies with GOPROXY=off,
GOSUMDB=off and GOTOOLCHAIN=local. Both PIGO_LIVE_TEST and PI_GO_AGENT_LIVE_TEST
were explicitly 0. No live external Kiro/model API was called. Repository unit
suites may use their normal localhost mock servers; migrated Kiro requests use
only in-process fake executors and synthetic responses.

| Check | Result |
| --- | --- |
| All TestKiro via JSON output | PASS: 123 top-level tests, 215 including subtests, zero failures. |
| All TestKiro shuffled, count=3 | PASS. |
| go test ./... -count=1 -timeout=180s | PASS across all packages; pkg/pigo 15.842s. |
| go test -race ./... -count=1 -timeout=180s | PASS across all packages; pkg/pigo 29.302s. |
| go vet ./... | PASS, no output. |
| gofmt -l pkg/pigo/*kiro*.go | Clean, no output. |
| Independent three-group focused rerun | PASS, 0.013s. |

Relative to #75's 108 top-level / 157 with subtests, these migrations add 15
top-level tests and 58 passing nodes including subtests. These are Go test
counts, NOT a percentage of upstream suite parity, scenario completeness or
coverage of a live service. 32-bit runtime validation remains unavailable as
recorded in the base report; this increment does not change that limitation.

## Explicitly not included or delivered

No OAuth login/refresh/CLI fallback, management discovery, automatic ARN/region
inference, real timeout/redirect/retry loops, token billing estimation from
percentages, text-to-executable-tool fallback, chronology repair, image support,
raw argument persistence/edit reconciliation or signed reasoning replay is
claimed. Some upstream scenarios are translated into deliberate rejection tests
rather than copied as accepted behavior; the individual maps label each case.

No builtin provider registration, host event lifecycle adapter, production
network defaults, GitHub CI dispatch, PR approval, master merge or deployment.
Original #75–78 source branches are untouched. This report covers only the
listed hashes; any later parallel test work requires a new explicit merge and
rerun rather than being silently counted as validated here.
