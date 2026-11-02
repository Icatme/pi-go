# Explicit Auto→HTTP selection for the buffered Kiro module

Depends on #105. AgentDefinition defaults empty Transport to Auto. Transport
selection is provider-specific: the existing OpenAI Codex implementation can try
WebSocket then HTTP, which must NOT be copied into this single-attempt Kiro adapter.

The new controlled entry point is:

```go
err := pigo.RegisterKiroBufferedModuleWithOptions(
    sourceID, config,
    pigo.WithKiroAutoHTTP(),
    pigo.WithKiroSessionAffinity(affinity),
)
```

All network/identity/credential inputs still come from the explicit config. The
Auto option is the caller's local choice to select the only implemented injected
HTTP path; it is not service-use or account authorization.
It does not create a client, probe WebSocket, retry on failure, switch credentials,
or fall back after response bytes. Explicit WebSocket/WebSocketCached still
reject. Delivery remains completed/buffered, not realtime. SSE remains the host
HTTP selector name; the actual wire is AWS binary event-stream.

The original RegisterKiroBufferedModule and RegisterKiroSessionBufferedModule
remain strict and reject Auto. KiroBufferedConfig's exported layout is unchanged.
The new entry with no options behaves like the original strict non-session entry.
SessionID still requires the separate explicit affinity option; APIKey/custom
HTTPClient and all unrelated unsupported options continue to reject.

KiroBufferedOption has private fields and is not an arbitrary callback. Only the
provided option constructors produce valid options. Zero/duplicate/too-many
options reject before registration and all callbacks. Affinity key length/tenant
are checked before copying, so invalid oversized keys are not duplicated; key
material is copied at option construction and again at registration. fmt redacts
the option and JSON has no exported fields. No key generation/persistence or
zeroization guarantee is added. Existing source cleanup and in-flight semantics
remain unchanged.

Three original unit tests cover full/simple selection, old default rejection,
SSE and WebSocket boundaries, executor errors issuing exactly one call, invalid
option sets, secret formatting, option/registered-key isolation, composed affinity,
and unrelated host guards. A public registered Agent test leaves Transport at its
default Auto and succeeds only through explicit option selection. The existing
negative default-Auto Agent test is retained. All executors/credentials are fake.

The caller-supplied executor is trusted and could violate the no-retry/no-redirect
contract itself; this adapter cannot sandbox arbitrary callbacks. No real endpoint,
account, model availability, derived-ID compatibility, CI, merge or deployment is
verified. This is an explicit local selection policy, not a claim of upstream
transport parity or service permission.
