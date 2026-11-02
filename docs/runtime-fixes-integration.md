# Independent runtime fixes: PRs #58–60

This integration branch is based on master
`afaebd607e1f6ed0758fc5cf830215d304b063e5`. It combines these exact draft heads,
which were open and unmerged when checked on 2026-11-02:

| PR | Head | Scope |
| --- | --- | --- |
| #58 | `0a0cd04fb196302922dee891ee7232455ee66c64` | Reject named-array-property loss and inherited descriptor-value confusion at codemode JSON boundaries |
| #59 | `41ff2771b361c66c5209f1225b083ebacd0e92f4` | Reject dynamically incomparable invocation resource keys without map-hash panic |
| #60 | `fbdb9908576c8bae65a0e998b6d58bd56c22bb86` | Do not revive successful tool intent after a terminal model error event |

The three branches merged locally without conflict; remote integration preserves
all three heads as ancestors. Additional integration changes only format their
Go test sources and clarify the README's non-enumerable **named** property claim.
No Kiro branch or #131 active-history mutation fix is included. This does not
resolve the separate Kiro ToolCall public-source-layout blocker.

## Executed evidence

Cloud Linux amd64, official Go 1.26.2, Node v24.22.1, cached dependencies only,
GOTOOLCHAIN=local, GOPROXY=off, GOSUMDB=off, live flags disabled:

- Node VM serialization suite: 30/30 passed.
- Same suite in the repository's pinned QuickJS WASM: 30/30 passed. The Node
  adapter is a trusted-fixture harness, not a hostile sandbox/security claim.
- Uncached `go test -mod=readonly -race ./agent ./codemode -count=1`: passed;
  agent 2.031s, codemode 18.926s. These exercise the real Go/wazero host boundary.
- Baseline counterchecks against an archive of the above unchanged master:
  #60's new test failed with one fake tool execution and two turns after a terminal
  error; #59's new test failed with `hash of unhashable type: []int`. The combined
  implementation passes these same regressions. No external tools were executed.
- Independent static review found no integration blocker; the README precision
  issue was corrected. The reviewer did not independently execute tests.

Final root-module `go test -mod=readonly -race ./...` passed (agent 2.077s,
codemode 20.066s, pigo 29.043s; remaining packages passed).
`go vet -mod=readonly ./...`, gofmt and diff whitespace checks also passed.
Separate examples modules, other operating systems and
live provider/service behavior are outside this integration's validation scope.
There were no dependency downloads, paid jobs, CI dispatches, master merges,
release tags or deployments.
