# Joined offline Kiro component regression candidate

This draft targets #68, not master. It preserves the original branches and joins
these exact commits with real Git merge parents:

| PR | Commit | Role |
| --- | --- | --- |
| #68 | 4859210443fc787755e798b476fdc363fa470b38 | HTTP assembly; includes #62 and #65 |
| #63 | 56875b5c914880e98a4a1e04f7df3f7d515bb677 | TextContent user blocks in tool projection |
| #66 | 906a9c859b3bdb3735841094bebe4743d288e16d | Standalone context pressure observations |
| #67 | 3d448c0c0b843b13e22df4745ebc12b581edc756 | Retry-After parsing; includes #64 classification |

Against #61 a0b6835528cfe5a56c53e0469be854bf872de297, all seven PR
trees had no deleted files and 23 distinct changed paths. Overlapping changed
paths had identical blob SHAs. The union requires no source conflict resolution.
This is a tree-level compatibility check, not a compiler or runtime result.

## Gap addressed

#68's original fake-transport test used string user input and text-only response.
It did not establish compatibility with #63 or #66. The added regression sources
exercise tool history with pointer/value user text blocks through the final
HTTP body, preserve original large numeric JSON arguments and tool result IDs,
then collect synthetic text plus zero/nonzero context observations. Context
pressure stays separate from final token accounting and does not become final
usage metadata. Signed blocks and the exact serialized output limit are checked
through the HTTP constructor. A valid Retry-After hint remains separate from
failure classification and cannot authorize replay after sent/unknown dispatch.

These are three new Go test functions, NOT three passing tests. No implementation
semantics were changed beyond the exact component union.

## Remaining safety boundaries

- No provider registration, real HTTP client, auth attachment, tool execution,
  response status/content-type handler or automatic retry is introduced.
- NewRequest GetBody can enable a future client's 307/308 replay: the future
  transport must disable unsafe redirects/replays independently.
- The response consumer must explicitly handle context_usage parts and never
  assume all non-text parts are tools.
- RawArguments is still nonpersistent; this fixture does not prove an Agent
  persisted-history roundtrip. Do not reconstruct original raw JSON from maps.
- Retry-After is a wait hint, not dispatch proof, replay permission or accounting.
- #69 and later credential/transport work, if present, is not included here.

## Verification

Remote source/blob union inspected; independent static review found no definite
blocker. Its tool identity/result assertion suggestion was applied. The retry
check covers two pure contracts, not an HTTP retry execution chain.
Go compilation, gofmt, focused tests, race and full suite have NOT run: no usable
approved Go toolchain is available. No installation, CI dispatch, network
provider request, merge to master or deployment was performed.

Suggested PowerShell commands for a separately authorized Go-equipped environment:

```powershell
gofmt -w pkg/pigo/provider_kiro_joined_integration_test.go
go test ./pkg/pigo -run '^TestKiroJoined' -count=1
go test ./pkg/pigo -run '^TestKiro' -count=1
go test ./pkg/pigo
```

Keep draft and do not infer merge readiness from conflict-free Git trees.
