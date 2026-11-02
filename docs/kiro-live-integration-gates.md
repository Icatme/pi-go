# Live integration route is blocked; offline work is not a permission grant

Checked against official public documentation on 2026-11-01. These statements
describe the published product boundary, not legal advice or account-specific
permission. No real account, key, subscription usage or service call was tested.

## Official sources

- [Kiro FAQ](https://kiro.dev/faq/) permits the named native Kiro surfaces,
  ACP-compatible editors and software-development automation, while excluding
  third-party harness use that routes requests outside Kiro's native interfaces.
- [Authentication](https://kiro.dev/docs/getting-started/authentication/) lists
  API-key authentication for CLI CI/headless use. Existence of an API key does
  not establish a public model-runtime API contract for arbitrary clients.
- [CLI headless mode](https://kiro.dev/docs/cli/headless/) documents using the
  official CLI for automation. Running it would consume the user's subscription
  and can execute tools; this work has not launched or installed it.
- [CLI ACP](https://kiro.dev/docs/cli/acp/) documents the official CLI's ACP
  integration using JSON-RPC over stdin/stdout. It is an agent integration, not
  a drop-in raw-model HTTP provider. No ACP process has been started here.

## Consequence for this project

The chosen community provider's direct private-runtime request shapes do not
themselves establish a permitted third-party call route. Our inference from the
official boundary is that this direct subscription-backed integration must not
be activated merely because its binary framing or API-key format is understood.
Do not copy an official CLI origin/user-agent to disguise a different client.

All current Kiro draft code remains unregistered/offline. There is no HTTP
transport, credential-store discovery, refresh login flow, automatic model fetch,
or live request wiring. Protocol fixtures and static review do not authorize
subscription use, validate terms compliance, or prove production compatibility.

Changing to official CLI/ACP is a separate architecture/product decision. The
user selected following pi-provider-kiro; do not silently replace that decision
or resurrect the abandoned ACP draft. A future choice should compare keeping
this work as offline research versus explicitly authorizing a documented native
route, including execution ownership, tool permissions and subscription usage.

## Gates before any actual connection

1. Establish a documented permitted integration route, or obtain provider
   confirmation covering the intended direct client. No identity spoofing.
2. Obtain explicit authorization for any software installation and any execution
   that uses subscription/extra credits; general development permission is not
   that authorization. No user's local machine without an explicit request.
3. Run actual Go formatting, compilation, tests, fuzz/race checks. None has run.
4. Resolve known strict-subset gaps and persisted-history raw-evidence provenance.
5. Review credential handling, account/profile binding, error redaction and tool
   ownership for the chosen route before separately authorized live validation.

The available independent next work is offline verification, consistency review,
and documenting compatibility—not enabling a private runtime transport by default.
