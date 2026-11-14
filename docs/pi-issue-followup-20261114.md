# Pi issue follow-up, 2026-11-14

Baseline fetched from master: `9a8e190d466c1e7a9864743d86d86e7c567b7676` (merged #223). The original #200–222 are superseded and closed; this slice does not reapply them.

## Mistral cancellation during an error response

Reviewing [upstream #12148](https://github.com/earendil-works/pi/issues/12148) and its latest comments distinguished two different problems:

- The reported Mistral strict-tool stall is not reproduced in Go: `mistralToolDeclaration` does not emit `strict`. Do not label this change a fix for that upstream stall.
- A separate Go defect exists: the non-2xx Mistral path discards `io.ReadAll` errors. The shared transport already terminates the body on cancellation, but the adapter converts that cancellation into an ordinary upstream error. A timeout therefore loses its timeout classification.

Preserve and wrap the read error rather than parsing an incomplete error body. Completed non-2xx errors retain their existing message. No new timeout, retry, fallback, schema rewriting or HTTP-status diagnostic contract is introduced.

A cancellation-unaware custom transport covers explicit cancel and timeout × HTTP 200/503. Before this change the two 503 cases fail with ordinary error; afterward all four classify aborted with cancelled/timeout reason. A second test requires the original read-error identity, exactly one request and a closed body. Existing Mistral success and completed-error tests remain.

## Other rechecked issues

- [#11763](https://github.com/earendil-works/pi/issues/11763): Go uses MCP go-sdk v1.8.0, not upstream's TypeScript transport. pi-go sets `MaxRetries: -1`; SDK `handleSSE` also carries the original call context through backoff and resumption GET. Existing SDK `TestStreamableClientResumption_Cancelled` passed race three times, covering initial read/backoff/resumed read and another successful request after cancellation. This is not a new pi-go end-to-end trace or live-service test.
- [#11122](https://github.com/earendil-works/pi/issues/11122): the current Go Code Mode does not contain upstream's `schemaToType`/TypeScript declaration renderer, so the proposed sibling-allOf fix cannot be transplanted. No runtime schema change is justified by that report alone.
- [#10162](https://github.com/earendil-works/pi/issues/10162): provider-specific image-count limits require explicit host/model budgeting. No hard-coded global limit, silent image discard or automatic paid summarization has been added.

## Validation scope

Remote Linux, Go 1.26.11, synthetic local fixtures only. Full `pkg/pigo` race and vet, focused regressions, and whitespace checks are recorded with the PR. This slice does not claim another full-repository race run, live Mistral validation, release, downstream update or deployment.
