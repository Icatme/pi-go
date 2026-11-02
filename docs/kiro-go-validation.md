# Executed Go validation of the Kiro integration workbench

Validation date: 2026-11-02 (UTC). This report supersedes the historical
"Go not run" statements retained in earlier component draft documents **for
this explicitly joined source tree only**, not every isolated historical PR.
It is not evidence of a real Kiro account connection or released provider.

## Exact tested integration

Normal merge ancestry includes:
- #70 a5e92e14502d6a89bbfeef90ce2d6189ae8745af;
- #72 33fd44a40956cf1bab6ea84f7d250b141067a9ce;
- #74 40776d4575482834c6520b177a3b89e535cc6aca.

32 Go files were formatted using official Go 1.26.2 gofmt in a separate commit.
An independent reviewer verified both merge trees against their source parents,
and each changed file equals official gofmt applied to its pre-format content.
No production logic repair was needed by the executed tests.

Tested local commit: b84f002d0b788f2978fd8cb8ed376c8d1d24ef6a.
Published equivalent commit: 7f4bdcc5cf44c5f7a758cf5d2c10ff419ae1ad99.
Both have the exact tree: 85aa59d4df6e58953839f234399332f6ecd0e956.
GitHub connector publication preserved each normal merge's additional parent.
Direct git push lacked authentication; no local credential search was performed.
The report-only commit after this snapshot changes no Go source.

## Toolchain provenance and isolation

User explicitly approved installation of the official Go toolchain in the cloud.
Linux/amd64 Go 1.26.2 matches go.mod. Downloaded from the official go.dev download
endpoint, verified against the official release JSON SHA256 before extraction:
`990e6b4bbba816dc3ee129eaeaf4b42f17c2800b88a2166c265ac1a200262282`.
Installed only under the cloud workspace's user-writable toolchains directory;
no user local computer, separate Codex task or additional billed executor used.

Dependency versions remained pinned by go.mod/go.sum; required public modules
were fetched through the standard Go proxy/checksum service. Later checks used
cached dependencies. No module files changed. Live suites were explicitly off:
`PIGO_LIVE_TEST=0`, `PI_GO_AGENT_LIVE_TEST=0`. All Kiro transport fixtures are local
fake executors; no real Kiro/model call, token discovery or account data was used.

## Actual results (not merely written test sources)

| Command | Result |
| --- | --- |
| gofmt -l pkg/pigo/*kiro*.go (after formatting) | Clean, no output. |
| go test ./pkg/pigo -run '^TestKiro' -count=1 | Passed. JSON rerun: 108 top-level tests, 157 including subtests, no failures. |
| go test ./pkg/pigo -count=1 -timeout=180s | Passed, 16.075s. |
| go test -race ./pkg/pigo -run '^TestKiro' -count=1 -timeout=180s | Passed, 1.093s. |
| go test -race ./pkg/pigo -count=1 -timeout=180s | Passed, 29.206s. |
| go test ./... -count=1 -timeout=180s | Passed across all repository packages; live tests disabled. |
| go test -race ./... -count=1 -timeout=180s | Passed across all repository packages; live tests disabled. |
| go vet ./... | Passed, no output. |
| GOARCH=386 CGO_ENABLED=0 go test -c ./pkg/pigo | Compiled successfully to a 32-bit ELF. |
| GOARCH=386 ... go test ./pkg/pigo -run '^TestKiroCompletedText' | Could NOT execute: cloud kernel returned exec format error. NOT a passing 32-bit runtime test. |

Independent reviewer also ran the CompletedText/InjectedAttempt focused tests
successfully without changing the worktree. This does not constitute a GitHub
PR approval. CI was not dispatched; these are cloud-workspace command results.

## Still not verified or delivered

- No live credentials, endpoint/account entitlement, real service response,
  latency/load/fuzz campaign or deployed integration verified.
- No 32-bit runtime execution; no emulator installed to hide that limitation.
- This workbench does not include independent reliability PRs #58–60 or register
  Kiro as a builtin provider. Passing baseline codemode/agent tests here is not
  validation of those separate PR heads.
- Host streaming event adapter, raw-tool persistence/editing, signed reasoning
  replay, explicit account/profile binding and service-use gates remain open.
- Original component branches and master remain unchanged; no merge/deployment.

Next work can now use actual compilation/unit/race evidence for this integration,
rather than treating source review as test success. Keep future upstream scenario
migration and host registration changes independently reviewable and rerun tests.
