# Explicit incremental text / completed tools registration

`RegisterKiroIncrementalModule` is a third explicitly named mode, separate from
unchanged buffered and text-only registrations. Its distinct `KiroIncrementalConfig`
has the same request/auth/schema fields as buffered config. `KiroIncrementalOptions`
explicitly opts Auto into injected HTTP and/or copied tenant/session affinity;
zero options keep Auto/SessionID rejected. No existing exported struct layout was
changed. Internal delivery uses named enum values, not ambiguous boolean switches.

Registration installs an API only, not a provider/model catalog, login or default
network client. It snapshots the raw schema catalog and affinity key. Caller-supplied
origin/user agent and scoped credentials retain existing semantics. Tools declared
by the prepared host context must exactly match catalog schemas; extra catalog
entries do not authorize tools. Use Stream/StreamSimple host guards, not raw
GetAPIModule callbacks. RequestIDs may run before input preparation; credentials
only run after request validation/serialization. Callbacks must be context-cooperative.

Leading text is actual provisional streaming. Tool events are released only after
HTTP EOF/length/Close and full protocol/terminal validation, never from raw argument
fragments. Text-after-tools, reasoning/signatures and unsupported payload semantics
remain rejected. ToolCallEnd is completion evidence, NOT permission to execute.
Consumers must await successful final result and the Agent must recheck schema,
permission and current context. Previews cannot be revoked; no late-failure retry
or switch to buffered delivery occurs. Persisted raw tool history is still unsupported.

## Actual Agent synthetic integration

Original tests use production registration, default Agent Auto/nonempty SessionID,
and an injected pipe that withholds the first tool terminal until an Agent text
delta is observed. There are no real service or tool side effects.

- Allow: one local fake tool execution, two model attempts, exact large integer in
  execution and second request's typed JSON, stable scoped conversation ID.
- Permission deny: zero executions, one permission check, two model attempts and
  an error tool result replayed in the proper group.
- Cancel upon observing ToolCallEnd: that lifecycle event is delivered, but zero
  executions and no second request; current cancellation still wins.
- Truncated terminal / late body Close failure: text preview may exist, but no tool
  end, permission check, execution or second request. Failure is not successful
  completion. Deadline expiry is not accepted as proof of these bounded paths.
- Public full dispatch rejects zero-option Auto/session, WebSocket, external API
  key and missing schemas before credentials/transport. Invalid affinity installs
  no module. Existing buffered/live registration regressions remain in the suite.

These extend independently authored upstream text/tool/error scenarios, not copied
pi-provider-kiro test files. Baseline remains dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba.
Endpoint acceptance, identity permission, real billing, account model availability
and production deployment remain unverified.
