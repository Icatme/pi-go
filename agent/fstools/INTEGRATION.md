# Compatible filesystem integration

This package is the complete filesystem-tool tree from PR #149
(commit `44283f87076151db7d2e18ca49b67aaadd909e16`), migrated onto the
compatible receipt APIs in #150, #151 and #152. It is a replacement integration,
not an additional branch to merge after the original filesystem stack.

## Review and merge topology

Review foundations in order: #150 (in-process receipts), #151 (session-backed
single direct calls), #152 (explicit nonpersistent model runs), then this
filesystem integration. Do not merge #144: its added public struct fields and
persisted execution fields are absent here. This integration supersedes the
filesystem contents of #139, #140, #141, #143, #145, #146, #147, #148 and #149;
do not subsequently merge those duplicate histories. #142 is independent,
optional MCP documentation. None of these instructions authorizes merging.

The original read/ls/find/grep/preview implementations are unchanged, including
the #141 fix that rejects empty non-EOF directory reads rather than claiming a
complete scan. Of the 21 production Go files in #149, only create.go,
replace.go, edit.go and publication.go changed: writes require capture before
file I/O and report commit immediately after publication. No old incompatible
runtime implementation is copied. The old 80 tests and two examples remain;
assertions now inspect actual receipt handles, with additional real model,
Codemode, direct-session and legacy-entry integration tests.

## Compatibility and scope

Existing public struct layouts, old Invoke, old execution JSON and old session
records remain those of the compatible foundations. No LocalEffect field is
added. New host receipts are not transported in model-visible result JSON or JS
error metadata. A missing committed fact is unknown, never proof of no effect.
Write factories require an explicitly supported receipt-aware entry point;
old raw/Agent paths refuse them instead of silently discarding commit facts.
Read-only tools continue to work through ordinary entry points.

Runner.RunWithReceipts is in-process only, with no journal. Session receipt
bindings support single direct calls, not persistent model loops. Checkpoint
and legacy journal execution do not support these writes. Session intent/final
records fence the old runner; old readers can browse them, but cannot safely
resume the new workflow. Receipts do not atomically commit external filesystem
changes and session storage. A crash or lost acknowledgement can leave an
interrupted, uncertain call requiring host investigation.

Capture is not idempotency, a retry barrier, CAS or automatic write safety.
Models and scripts may call again after an error. Hosts must choose their own
policy before enabling writes; a committed receipt must not be interpreted as
permission to retry. Existing last-writer-wins, metadata, trusted-directory,
Linux-only and non-sandbox boundaries are documented in README.md.
