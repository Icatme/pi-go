# Public registration + agent session validation

Depends on #102. This test-only increment exercises the production
RegisterKiroSessionBufferedModule path with a real default pigo-backed agent
model bridge, nonempty AgentSnapshot.SessionID, tool schema/permission gates,
raw-argument evidence, host option hooks and completed-response queues. Unlike
the older fixture, it does not register its own permissive API callback.

The only test wrapper installs a unique fake provider/model and calls the public
registration function; credentials/executor/wire bytes are synthetic. Cleanup
removes both API and fixture provider. The wrapper only exists in `_test.go` and
is absent from production builds.

Four original test functions cover:
- allowed local fake tool versus CheckToolPermission denial and BeforeToolCall
  blocking: two model turns, correct tool result status, execution counts 1/0/0;
- original integer 9007199254740993 retained through parsed tool execution and
  next-request raw replay, never rounded to 9007199254740992;
- stable derived conversation ID across tool/result turns without raw session on
  wire; same API/provider/profile/key/session across tenant A/B/A yields A != B
  and deterministic A again after explicit unregister/re-register;
- incomplete frame and cancellation after fake HTTP entry never execute a tool
  or initiate an automatic retry;
- the default agent Auto transport is rejected before fake HTTP. For this strict
  subset configure `AgentDefinition.Transport = agent.TransportSSE` explicitly.
  SSE is the host HTTP-stream selector, not Kiro binary event-stream encoding.

The first end-to-end test run used default Auto and correctly failed before HTTP;
fixtures now explicitly select SSE and retain a negative Auto regression. No
production guard was relaxed to make the test pass. An initial test source syntax
error was corrected before runtime checks. No production implementation changes.

Supported opt-in agent use is proven only with this synthetic complete buffered
subset and explicit options. Real endpoint/auth/model availability, derived ID
acceptance, realtime output, richer host options, dynamic tool/system changes and
persisted raw history restoration remain unverified or unsupported. Existing
upstream migration mappings remain; these scenarios independently test local
host integration, not copied upstream code or a live conformance claim.
