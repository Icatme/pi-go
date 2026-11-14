# DeepSeek non-2xx body cancellation, 2026-11-14

Baseline: master `9a8e190` after the reviewed #223 integration. This is independent of Mistral PR #224 and does not reapply its patch.

The follow-up to upstream [#11211](https://github.com/earendil-works/pi/issues/11211) and [#12070](https://github.com/earendil-works/pi/issues/12070) checks the broader cancellation boundary rather than assuming their provider-specific implementations match Go.

DeepSeek's native adapter discarded the error from reading a non-2xx body. The shared HTTP client already closes a stalled body on cancellation; the adapter then erased that reason and reported a generic upstream error. Explicit cancel and deadline during a 503 body each reproduce this misclassification on the baseline. HTTP 200 control cases pass.

Preserve the read error with `%w`. Completed error bodies keep their existing text and behavior. This adds no retry rule, timeout, fallback, structured HTTP status contract or provider configuration.

The same eight-way fixture also exercises CommandCode (cancel/deadline × 200/503). Its baseline cases pass through its existing shared boundary, so no CommandCode implementation is changed. DeepSeek also has direct original-error identity/one-request/body-close coverage and ordinary broken-body remains an error coverage.

## Verification

Remote Linux Go 1.26.11; offline custom transports only. Full `pkg/pigo` race and vet are recorded in the PR. No full-root suite, real DeepSeek/CommandCode request, paid model, new release, merge or deployment. No claim is made to resolve all upstream first-content/idle timeout or retry requests.
