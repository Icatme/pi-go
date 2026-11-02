# Upstream request/client/auth test scenarios: independent Go migration

Upstream reference: mikeyobrien/pi-provider-kiro commit
`dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba` (v0.12.3).
This PR is based on the actually validated #75 integration, not an uncompiled
scratch assembly. Only new Go tests and this mapping are added; production code
is unchanged. No upstream TypeScript implementation/test code or fixture corpus
is copied. Scenarios were independently expressed with new fake strings and
synthetic response bytes. License provenance remains a separate tracked issue.

## Mapping and honest scope

| Upstream source scenario | New Go test / assertion | Scope |
| --- | --- | --- |
| stream.test.ts L273–298: runtime POST/auth/profile | ExplicitProfileRegionAndAuthentication: 3 credential kinds, exact POST endpoint, no management X-Amz-Target, explicit profile/conversation/model/origin, no token in body/URL | Request portion migrated. Upstream percentage-to-token estimate assertions deliberately NOT ported. |
| stream.test.ts L709–727: supplied profile without discovery | Same test: chosen profile retained, exactly one injected runtime execution | Explicit path covered; no management discovery implementation or CLI lookup claimed. |
| stream.test.ts L729–748: profile absence prevents inference | PreflightFailuresNeverInvokeExecutor/profile_missing | Local missing-config rejection adaptation; NOT a profile-discovery/fallback test. |
| stream.test.ts L838–905: non-default/profile runtime region | ExplicitProfileRegionAndAuthentication uses eu-central-1 with a matching fake profile | Explicit region construction only. Automatic ARN/SSO/URL region inference remains unimplemented. |
| stream.test.ts L5403–5415: stable conversation ID | StableConversationAndCallerSelectedIdentity: same explicit ID across two distinct requests, distinct content retained | Explicit session input behavior covered; no automatic ID generation/lifetime policy claimed. |
| oauth.test.ts L284–305: API key validation and wire token type | Invalid key preflight plus successful explicit API_KEY request case | Syntax and runtime auth-header subset only; NOT login/GetProfile/token-store/refresh coverage. |
| token-type.test.ts L35–41: external vs ordinary bearer headers | 3-kind request test plus UnsignedAudienceDoesNotSelectCredentialKind | Header values covered. Intentional difference: kind is explicit; unsigned JWT aud cannot auto-select it. |
| stream.test.ts L3171–3205: caller cancellation while awaiting headers | HeaderWaitCancellationNeverRetriesOrRefreshes: context reaches fake executor, cancellation clears result, exactly one call and no error-text echo | No real wait, timeout timer/listener or credential refresh behavior claimed. |

Source links:
- https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/stream.test.ts#L273-L298
- https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/stream.test.ts#L709-L905
- https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/stream.test.ts#L5403-L5415
- https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/oauth.test.ts#L284-L305
- https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/token-type.test.ts#L35-L41
- https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/test/stream.test.ts#L3171-L3205

## Additional port-specific contract coverage

The explicit compatibility test exercises injected UA/invocation ID, both UA
headers, SDK request marker, mode and opt-out. These are supplementary assertions
against upstream src/stream.ts L1015–1048, not falsely counted as separate upstream
test cases. KIRO_CLI origin appears only when deliberately selected for the local
fixture; the general request tests use PI_GO_TEST_ONLY. No runtime impersonation
or real service is performed by these tests.

Malformed identity/header/API-key kinds must fail before calling the fake
executor. A mixed-case region is deliberately rejected instead of upstream
normalization; automatic region repair is not claimed. Caller cancellation has
no automatic retry/refresh, unlike some upstream retry policies. No context
percentage is converted to token usage or cost.

## Deliberately deferred suite areas

OAuth login/refresh, external IdP endpoints, CLI credential fallback, management
profile discovery and account binding, inferred/case-normalized regions, actual
session storage, automatic retries/timeouts/echo loops, provider registration
and live acceptance are NOT migrated by this PR. Event/tool/transform scenarios
are separate coordinated PRs; no global completion percentage is invented.
These tests validate the current explicit strict subset, not all plugin behavior.

## Executed verification

Official cloud Go 1.26.2, pinned cached modules, live test flags both disabled.
Focused TestKiroUpstreamRequest tests, all Kiro race tests (1.076s), and full
pkg/pigo regression passed. Independent reviewer reran the focused tests too.
Five top-level tests contain nine subtests.
No actual HTTP/model call, credential access, dependency installation, CI dispatch,
master merge or deployment is performed. The base's full-suite/race evidence is
recorded in docs/kiro-go-validation.md; test-only changes do not imply live support.
