# Agent history/session mutation during a run

`ReplaceMessages`, `ClearMessages`, and `SetSessionID` keep their existing public
void signatures. While an Agent run is active they now make **no change**. Use
`TryReplaceMessages`, `TryClearMessages`, or `TrySetSessionID` to receive
`ErrAlreadyRunning` and decide explicitly whether to retry after `WaitForIdle`.
Idle behavior is unchanged. These methods do not cancel or queue a mutation.

This is a documented behavior change: previously the setters mutated visible
state during a run, but the in-flight cloned snapshot could overwrite that change
when the run finished. A deterministic fake-model/race probe reproduced old
history returning after Clear/Replace and an old SessionID returning after Set.
The new guard checks under the same Agent mutex as mutation, matching Reset's
existing active-run rejection policy without changing its API.

Model/tool/hook hot updates and AppendMessage are not changed by this narrow fix.
No Engine snapshot-generation rewrite, raw-evidence storage or Kiro behavior is
introduced. In particular this does **not** fix the separate draft Kiro ToolCall
source-layout blocker.

Regression coverage includes active rejection/no partial state changes, old
external method-expression signatures, idle behavior, and mutation after context
cancellation has reached idle. All models are in-process fakes; no service calls.

A synchronous EventAgentEnd listener still runs before the Agent run returns:
Try* rejects there even if State.IsStreaming is already false. Apply the change
after Prompt/Continue returns or from another goroutine after WaitForIdle. Do not
synchronously WaitForIdle inside that listener, which would wait on itself.
