// This external-package fixture intentionally uses the public pre-Kiro layout.
// Run explicitly from the repository root; testdata is not included in ./....
// It must compile after the source-compatibility blocker is resolved.
package compat_test

import "github.com/Icatme/pi-go/agent"

var _ = agent.ToolCall{"id", "original", "lookup", "namespace", nil, nil, "signature"}
