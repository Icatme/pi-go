# M4b: batch main and background composition acceptance

This acceptance slice is stacked on draft #173, commit
`b4fcf4276767aadfeedc0bbd05d3c1d35babde3b`, tree
`499ff4009683e4ff8036daf51f7ea922ee951aa9`. It adds tests and corrects the
plan; it does not add a scheduler, constructor, journal format or dependency.

## Inspect before implementing

M4a originally left batch-main integration for later. M6a subsequently added
`OpenMainJournal` with both `MainJournalOptions.BatchRouter` and `Background`.
The current implementation already composes them through the same coordinator.
The first direct public-API integration tests passed without a runtime change.
No failing production behavior was reproduced, so there is no claimed red-to-
green bug fix and no second API for an already supported composition.

Use an explicitly claimed `LeasedJournal`, choose `BatchRouter` instead of
`Router`, set its `BatchOptions`, supply a Worker (optionally
`PiBackgroundWorker`), and opt into the usual `BackgroundOptions` grants.
For example, with host-owned journal/router/worker/backend configuration:

```go
coordinator, err := superagent.OpenMainJournal(ctx, journal, superagent.MainJournalOptions{
    Parallel: 2,
    BatchRouter: router,
    BatchOptions: superagent.MainBatchOptions{MaxItems: 8, MaxInputBytes: 32 << 10},
    Worker: worker,
    Background: &backgroundOptions,
})
```

`ExampleOpenMainJournal` demonstrates the optional same-host SQLite journal;
`ExampleOpenMainBackground` demonstrates explicit backend grants and lifetime.
Existing JSONL constructors remain unchanged. This does not imply a new
combined JSONL constructor or automatic journal migration.

## Direct composition evidence

- Hold the main callback while a registered backend remains active. A fresh
  input is processed by the original Worker with the same task/provider
  session and a distinct delivery ID before the tool exits.
- Queue exactly six mixed inputs: follow-up observation/result, background
  completion observation/result, and two unrelated user inputs. Assert ordered
  unique input IDs and exact reply/delivery/event correlation even when every
  returned decision array is reversed.
- Keep launch arguments and output out of batch inputs and public reports.
  The original Worker receives private completion output and the original
  operation, delivery and native call identities.
- Repeat the mixed backlog after Stop while a cancellation-ignoring backend
  still commits. Stop does not claim exit. The completion stays blocked by
  task review and the public report retains the committed receipt.
- Exercise denied launch grants, coordinator-owned authorization provenance,
  stale capabilities, independent quota and unknown-operation barriers through
  the same public combined constructor. A host note cannot release unknown.
- Run the native Pi protocol test in both single-main and batch-main modes.
  One launch has exactly one native tool response; completion is a later
  user-role input, including after a follow-up was handled by the same Worker.
- With the real same-host SQLite adapter, revoke the old journal lease during
  blocked batch inference. A new owner consumes the preserved inbox once.
  Release the old callback and verify it cannot change the replacement log.
  Backend registry loss preserves original identity and quota without Start
  replay. Only trusted exact-operation observation establishes the late commit
  and exit. Reopen again and verify terminal status/replies/operation identity
  and absence of duplicate work.

## Limits and validation

This closes the stale batch-composition plan item, not all of M4. A Local
backend is still in-process, not a durable remote executor. Cross-process
backend registry loss still requires exact-operation evidence. No live model,
service, provider, device, remote job, production database or model-performance
measurement is involved. The memory-journal tests are API acceptance fixtures,
not a production lease implementation; takeover evidence uses sharedsqlite.

Validation uses cached dependencies and the existing official Go 1.26.10
Linux amd64 toolchain, with provider/example live-test flags disabled:

- `go test -mod=readonly -count=1 ./...`
- `go vet -mod=readonly ./...`
- `go test -mod=readonly -race -count=1 ./...`
- Combined-background and native-protocol tests with race, 20 repetitions.
- Separate `examples` module readonly tests and vet.
- Independent read-only review, gofmt and whitespace checks.

Results are recorded in the PR after completion. PowerShell is unavailable in
this cloud host; no Windows/macOS execution is claimed. No user computer,
Codex or extra paid execution, manual CI, merge or deployment is used.
