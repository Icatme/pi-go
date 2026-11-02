# Upstream history/transform scenario migration (offline)

This test-only increment is based on integration #75 at 207957c3. It adds
original Go fixtures and assertions against the existing projection and request
encoder, not copied TypeScript or captured session payloads. No production code
or supported protocol surface changes.

Upstream evidence is pinned to pi-provider-kiro v0.12.3,
dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba:

- [transform.test.ts](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/transform.test.ts)
- [history-validator.test.ts](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/history-validator.test.ts)
- [history.test.ts](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/history.test.ts)
- [stream.test.ts empty tool carriers, L1832-2017](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/stream.test.ts#L1832-L2017)

## Traceable scenario map

All new functions are in provider_kiro_history_migration_test.go.

| Upstream scenario | Original Go coverage | Semantic relationship |
|---|---|---|
| convertToolsToKiro/buildHistory; well-formed user/assistant/tool-result pairs | TestKiroHistoryMigrationOriginalArgumentsAndEmptyResultCarrier | Preserve call/result order, original IDs/names, schema, text and statuses through final request bytes. |
| tool-result-only current turn has empty content, no invented carrier prose | Same test plus TestKiroHistoryMigrationEmptyResultRepresentations | Empty success/error results remain empty; nil, empty slice, value/pointer empty text forms. |
| tool arguments retained | OriginalArgumentsAndEmptyResultCarrier | Additional Go-specific evidence: raw bytes before encoding, exact compact JSON after encoding, nested positive/negative integers beyond 2^53, Unicode, empty object. |
| TOOL_USES_AND_RESULTS / orphan results | RejectsRepairInsteadOfInventingHistory: missing_result, orphan_result, results_reordered, leading_orphan | Reject without output; do not drop or invent calls/results. |
| relocateDisplacedToolResults | human_interrupts_group | Explicit difference: reject rather than moving human speech around tool results. |
| addPlaceholderTools / synthetic historical catalogs | missing_historical_catalog, no_catalog | Explicit difference: reject, never invent a schema for a removed tool. |
| normalize tool IDs / malformed pairing | duplicate_call_id, non_native_id | Explicit difference: reject instead of hashing/remapping identities. |
| incomplete/error historical assistants | incomplete_assistant | Reject rather than silently filtering incomplete tool history. |
| normalize/merge adjacent messages | adjacent_humans_not_merged | Explicit difference: preserve chronology by rejecting unsupported alternation. |
| sanitizeSurrogates | unpaired_surrogate | Explicit difference: reject invalid raw identity/argument JSON, not silently rewrite Unicode. |
| reasoning excluded from ordinary text | reasoning_not_flattened | Current Go subset rejects unsupported reasoning history, not flattening or silently discarding it. |
| stripHistoryImages / bounded retained image | image_not_silently_stripped | Images are unsupported here; reject, do not erase to pass a text-only request. |
| system prompt handling | system_not_lowered | Full request encoder rejects instead of silently lowering system precedence. |
| raw argument authority | parsed_and_raw_conflict, missing_raw_evidence, duplicate_argument_key | Additional Go safety contract: no float-map reconstruction or overriding an edited parsed map. |

Every rejection verifies nil wire output and unchanged independently recreated
input/schema. Success verifies the original context/schema remain unchanged.
The serialized argument comparison is exact compact JSON, not a numeric-substring
check, so turning integers into strings cannot satisfy it. These tests do not
claim all upstream history/transform cases are ported: images, repair, dynamic
system catalogs and reasoning replay remain intentionally unsupported.

## Fixture and licensing boundary

Upstream package.json declares MIT, but the inspected tree lacks a LICENSE file
and GitHub reports no detected license. These tests therefore independently
re-express observable scenarios with new synthetic inventory/notes data; no
upstream implementation, substantial test text or real-session helper is copied.
The public source links document provenance of test intent, not production
compatibility, service permission or a claim of verified license completeness.

## Actual validation (2026-11-02)

Official approved Go 1.26.2 in the remote workspace, GOTOOLCHAIN=local and both
PIGO_LIVE_TEST/PI_GO_AGENT_LIVE_TEST=0:

- gofmt on the new test file: completed; final gofmt -l is empty.
- go test ./pkg/pigo -run '^TestKiroHistoryMigration' -count=1: passed.
- go test -race ./pkg/pigo -run '^TestKiroHistoryMigration' -count=1: passed.
- go test ./pkg/pigo -run '^TestKiro' -count=1: passed.
- go test -race ./pkg/pigo -run '^TestKiro' -count=1: passed.

Independent static review found a potential weak wire numeric-substring assertion;
the test now compares exact compact original arguments and checks wire identity
and schema. Re-reviewed with no remaining blocker. The checks above test offline
scenarios only, not live Kiro behavior. Full-repository tests/vet/fuzz were not
rerun for this test-only increment. No real network/model, credentials, paid job,
installation, CI dispatch, tool execution, merge or deployment.
