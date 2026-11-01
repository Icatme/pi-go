# Kiro offline integration verification branch — NOT usable provider support

This branch joins the existing draft components without changing their source:
- #57 response path at 0cf13d29d189e45ce213fcd9c89f53014e18546b;
- #56 tool request/history path at dfd8fab053a1f46aa0d8d766adef3062829b2f28;
- #53 text request path at d0c4eb0436b02c8595a8752b38d88a00fb62f8dc.

The latter two were joined with normal two-parent commits, not force pushes or
master merges. All common file blobs matched before joining; the resulting tree
was checked against the exact union. Original PR branches and dependencies remain
unchanged. This branch is an integration workbench, not a replacement rollout.

## Cross-component regression source
The new test starts from Context and a raw schema map, builds inert request DTOs,
feeds synthetic binary events through framing/envelope/assembly/metadata/terminal
policy, then explicitly transfers raw tool evidence into a new synthetic history
with a fabricated tool result. It checks original IDs, large-integer spelling,
success/error result status and empty result-carrier text. No tool is executed.
A text-only test connects the text projection and offline response in two turns.

A negative persistence check demonstrates that json serialization omits
ToolCall.RawArguments. Restored tool history is still rejected. It must not be
made to work by reconstructing original bytes from float maps or silently
overriding independently edited Arguments. These tests are explicit fixture
assembly, not a general response-to-provider/agent adapter.

## Boundaries unchanged
No provider registration, full request envelope, endpoint, client identity,
credentials, model discovery, retries, network calls or service authorization.
No automatic signed reasoning replay, unsupported-event acceptance or tool
schema/permission bypass. The independent #58–60 reliability PRs are not included.
Official service-use restrictions and all strict-subset gaps still apply.

## Verification status and next steps
Conflict checks and exact-tree/readback checks performed. **Go compilation,
gofmt, unit tests, race and fuzz have NOT run** because an approved usable Go
toolchain is unavailable. Test source and a merged file tree are not a successful
integration result, compatible runtime or formal completed delivery.

On this integration branch with an approved project-compatible Go toolchain:
1. Format the changed Go files and inspect the diff.
2. Run go test ./pkg/pigo -run TestKiroOfflineIntegration -count=1.
3. Run go test ./pkg/pigo -run TestKiro -count=1.
4. Run go test -race ./pkg/pigo, then go test ./....
5. Review remaining protocol and service-use gates before any production decision.

Do not merge this branch as evidence that Kiro works. Its only purpose is to
make the previously split offline components jointly reviewable and testable.
