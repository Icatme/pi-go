# Codemode sandbox

`codemode` is independent of Agent, MCP, and model providers. It interprets an
async JavaScript function body using one fresh, embedded QuickJS WASM reactor
per script. No Node, CGO, filesystem, environment, stdin, sockets, module loader
or timer APIs are granted. Only the pinned reactor's reviewed imports are
registered. `date` uses the clock in UTC and randomness uses the host entropy
source; no host timezone/environment is inherited.

```go
sandbox, err := codemode.NewSandbox(ctx, codemode.DefaultConfig())
// Handle err; keep sandbox shared across runs in the same service.
result, err := sandbox.Run(ctx, `return await tools.echo({value: 3})`, codemode.RunOptions{
    Tools: []codemode.Tool{{
        Name: "echo", Description: "Echo a JSON value", Namespace: "example",
        Invoke: func(ctx context.Context, call codemode.HostCall) (json.RawMessage, error) {
            return call.Arguments, nil
        },
    }},
})
// Inspect result even on error: outputs and every accepted basic call survive.
// Close with a bounded context; inspect *codemode.CloseError on timeout.
err = sandbox.Close(closeCtx)
```

The caller owns tools, authorization, schemas, sessions, side effects and I/O
cancellation. It must check authorization immediately before dispatch. Tool
sets and visible schemas are copied per script. `Invoke` receives a
host-assigned ID; returned bytes transfer ownership. No automatic retry occurs.
`RunOptions.Sequential` makes all leaf invocations start in admission order,
including `Promise.all`, and waits for actual host exit before starting the
next. Generic successful host results have remote `not_applicable`; integrations
retain their authoritative protocol facts in their own outcome/ledger.

Scripts get `tools.<name>`, frozen `ALL_TOOLS` name/description summaries,
`searchTools(query, {limit?})` (BM25, default 5, max 50), `describeTool(name)`,
`describeNamespace(name)`, `text`, `image`, `console.log/info/warn/error/debug`,
`exit`, `store` and `load`. Discovery returns Promises; unavailable descriptions
resolve to `undefined`. Unknown tool calls reject. Supported image inputs are
PNG/JPEG/GIF/WebP inline base64 data URLs or `{mimeType,data}` objects; base64,
format headers and declared MIME are checked before output. No remote image
fetch is performed.

`return value` uses the same output budget as text/console/image. Source options
may appear only on the first line, for example:

```javascript
// @options: {"timeout_ms": 30000, "max_output_tokens": 2000}
const results = await Promise.all([tools.echo({value: 1}), tools.echo({value: 2})]);
text(results);
```

Unknown/invalid options fail. Options cannot enlarge an explicit host timeout or
output cap. **Default Timeout is zero:** caller deadlines/cancellation, source
options and explicitly configured host deadlines take the earliest bound.
There is no hidden 30-second cutoff. Supply a deadline for untrusted workloads;
context cancellation interrupts both CPU loops and endless microtasks. Errors
retain `codemode.js` source line numbers.

Default bounds are 64 MiB QuickJS heap, 128 MiB linear memory (2048 pages),
512 KiB native QuickJS stack (positive, at most the artifact's 512 KiB maximum),
4 simultaneous VMs, 16 shared actual host invocations, 1024 accepted calls and
queue slots, 256 KiB source, 64 KiB arguments, 1 MiB result/catalog, 16 MiB
aggregate queued-argument/completion payload, 32 KiB/256 output items, and 8000
estimated output tokens. Token budget is conservatively implemented as at most
four UTF-8 bytes per configured token; no provider tokenizer is claimed.
All accepted calls have bounded basic records; at most 256 error chains and
32 KiB diagnostic text are retained separately. Error queue metadata has fixed
per-field caps and at most 1024 entries, even when payload quota is exhausted.
Opaque objects/allocations owned by a Go tool remain the tool's responsibility.

Nonfinite numbers, BigInt, unsafe integers (outside +/-9007199254740991),
JSON precision loss, unsupported object/accessor values and cycles are
rejected. `ValidateJSON` exposes this boundary to integrations before parsing
into JavaScript; `*JSONError` supplies stable `unsafe_number` classification.
Ordinary decimal JSON such as 0.1 and 19.99 roundtrips; this is not arbitrary
precision arithmetic or a raw-wire preservation guarantee. `*CallError`
carries `code`, optional `reasonCode`, host `callId`, and independent execution
facts into JS. Uncaught correlated errors retain their original Go chain while
it fits the diagnostic budget. Successful tool-declared error envelopes are
ordinary values. Caught/replaced guest errors cannot invent a Go error chain.

Store is isolated to a script unless a caller-owned `*Store` is supplied.
`NewStore(maxBytes)` provides an invocation-local scope: each script snapshots
its revision, successful scripts atomically CAS-commit the private map, and
failed/canceled scripts discard changes. Concurrent revision changes return
`store_conflict`, preserving tool records and never replaying side effects.
`store(key, undefined)` deletes; missing `load` returns undefined. Values are
limited to 256 Ki UTF-16 code units in their serialized JSON, separately from
the default 1 MiB aggregate UTF-8 byte cap (including keys). There are at most
256 keys, each at most 256 UTF-8 bytes. No durable store or cross-identity scope
is inferred; applications must allocate the proper invocation Store.

VM cancellation does not release a host permit whose Invoke has not exited.
Late hosts update only Go records and cannot touch closed guest memory. Close
stops admission, cancels runs/hosts, waits within its supplied context and
reports outstanding calls explicitly. A subsequent Close can finish cleanup
after hosts exit; the sandbox never closes caller-owned MCP sessions or kills
arbitrary Go goroutines.

The artifact, exact source/ABI digest, MIT notices, maintenance/platform checks
and stack rationale are in [internal/assets/SOURCE.md](internal/assets/SOURCE.md).
