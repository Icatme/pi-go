# CommandCode Generate endpoint queries

The dedicated Generate backend now preserves query and fragment components while applying its existing path rule: an explicit `/provider/v1` path suffix is replaced with `/alpha/generate`, and a complete `/alpha/generate` path is retained. Other paths still receive `/alpha/generate`.

Only query/fragment-bearing base URLs enter the new path-component wrapper. Escaped path bytes, raw query ordering/duplicates/trailing slashes, ForceQuery and fragments are retained without cleaning dot segments. No-query inputs and malformed/opaque fallback remain unchanged. This is not new URL validation.

The default endpoint and its existing configuration, discovery endpoints, native Chat/Responses routes, authentication headers and retry policy are not changed. No vendor identity, user-agent or access behavior is modified. Verification uses explicit synthetic keys with localhost Generate requests, not live accounts. This independently follows the URL-construction bug class uncovered while investigating pi2528; that issue was not a CommandCode service incident report.
