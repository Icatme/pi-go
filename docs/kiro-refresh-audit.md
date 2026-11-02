# Refresh/public-boundary audit of the #122 development stack

Audit baseline: #122 (`b49a005e`), community pi-provider-kiro v0.12.3 `dd93f2e`,
AWS CreateToken and RFC 6749 section 5.1 read on 2026-11-02. This is an offline
implementation review, not a penetration test or real account/API certification.

| Area | Finding / current boundary |
| --- | --- |
| Expiry anchor | Confirmed defect in #122: successful slow Close shifted expiry later. A reproducing Go test failed before the fix. The follow-up anchors to pre-executor time and rejects backward/expired post-Close observations. |
| Clock compatibility | Successful refresh now calls the same injected clock twice. Invalid first observation prevents dispatch; invalid last observation remains an unknown rotation outcome. Existing exported struct layout is unchanged. |
| Rotation omission | AWS documents refreshToken as optional. The default IDC parser requires rotation and rejects omission. A subsequent explicit successful-omission retention option now exists. This is a deliberate incomplete compatibility subset, not proof a service response is invalid. That option preserves exact request-token provenance and never retains after failed/ambiguous attempts. |
| Unknown response fields | Strict parser rejects extensions; a general RFC OAuth client would ignore unrecognized response parameters. This helper is not a fully general/compliant OAuth response implementation. Do not widen accepted fields without bounded parsing and collision tests. |
| Attempt uncertainty | ExecutorInvoked records only control handoff, not wire send or server receipt. Response loss/parse/Close/ending-clock failure can hide rotation; no automatic retry or fallback to the old token. |
| Secret protection | Options/result formatting and public errors omit secrets. Explicit getters, request bodies and response buffers still contain plaintext; no memory zeroization guarantee or persistence facility. Caller executor owns logging/redirect/retry safety. |
| Scope | Profile/region are caller assertions. Responses are not cryptographic proof of account ownership, client registration, model authorization or profile-region consistency. |
| Actual integration | Public catalog observation, exact service-ID manual runtime mapping and explicit IDC refresh are callable interfaces with fake tests. No CLI discovery, browser/device login, client registration, automatic refresh source or disk store is installed. |
| Other auth families | Desktop/external-IDP response parsing exists as offline strict components, not public endpoint workflows. No arbitrary enterprise URL sender was introduced. |
| Runtime behavior | Explicit buffered/text-live/mixed-live registration and Agent tests remain separate from credential management. No default network/provider/model installation is added by refresh. |
| Validation | Synthetic cloud Go/race tests and independent static review; no real credentials, live requests, service billing, deployment or CI dispatch. |

An independent reviewer confirmed the expiry finding and reviewed its correction
statically; this was not a second test execution. The worklog records actual final-tree test runs. This
review does not claim every upstream test or runtime behavior has been migrated.

Sources: [AWS CreateToken](https://docs.aws.amazon.com/singlesignon/latest/OIDCAPIReference/API_CreateToken.html),
[RFC 6749 section 5.1](https://www.rfc-editor.org/rfc/rfc6749#section-5.1),
[community OAuth flow](https://github.com/mikeyobrien/pi-provider-kiro/blob/dd93f2e884b60cecd85c7fb4a818fb6d64ba3dba/src/oauth.ts#L361-L467).
