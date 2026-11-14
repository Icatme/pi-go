# Provider body cancellation audit (2026-11-13)

Baseline: pi-go master ae4e17f9022e40b17a1030824ce9a7a075ca610b.
Upstream reference: https://github.com/earendil-works/pi/issues/11211 (body and all four comments read).

## Confirmed locally

A custom RoundTripper can return a body whose blocked Read does not honor request cancellation. Anthropic SSE Result then remains blocked after caller cancellation or TimeoutMs. The standard Go HTTP transport normally handles cancellation; this is not evidence that every production request hangs.

The shared provider transport also buffers non-2xx response bodies before returning to the adapter. Therefore binding cancellation only in the Anthropic adapter leaves stalled error responses blocked. Bind cancellable request contexts at the shared response-body boundary instead. Preserve background-context body identity, close once, stop the callback on normal cleanup, and preserve context errors when a cancelled read unblocks. Anthropic ordinary and hosted non-2xx paths classify interrupted body reads as aborted, not normal HTTP completion.

## Validation

Original code failed caller-cancel and timeout blocked-SSE reproductions. Fixed code passes eight cases: ordinary/hosted Anthropic × HTTP 200/503 × caller-cancel/timeout, plus explicit-close idempotence. Full pkg/pigo tests (16.727s), race (32.710s), and vet pass on Go 1.26.5.

No live provider calls, real credentials, deployment or merge. This does not interrupt a custom RoundTripper blocked before returning headers, or a broken body whose Close does not unblock Read. Existing deferred cleanup handles parsing-error early exits; no drain or retry is introduced.
