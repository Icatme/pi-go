package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUnknownToolSuggestsOnlyAllowedNamesAndSupportsIn(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	tool := Tool{Name: "bash", Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
		t.Fatal("unknown lookup admitted a tool call")
		return nil, nil
	}}
	result, err := s.Run(t.Context(), `text("Bash" in tools); text("bash" in tools); typeof tools.Bash`, RunOptions{Tools: []Tool{tool}})
	var script *ScriptError
	if !errors.As(err, &script) || script.Code != "unknown_tool" || len(result.Calls) != 0 || textOutput(result) != "false\ntrue" {
		t.Fatalf("unknown lookup changed probing or admission: %+v %v", result, err)
	}
	for _, want := range []string{"codemode.js:1", "Did you mean tools.bash?", `"name" in tools`, "searchTools"} {
		if !strings.Contains(script.Diagnostic, want) {
			t.Fatalf("missing actionable hint %q: %+v", want, script)
		}
	}
	if strings.Contains(script.Diagnostic, "hidden_secret_tool") {
		t.Fatalf("unavailable tool suggested: %s", script.Diagnostic)
	}
}

func TestHostDiagnosticUsesPrivatePresentationAndNeverRawCause(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	secret := errors.New("SECRET_RAW_CREDENTIAL_AND_PAYLOAD")
	for _, mode := range []string{"plain", "typed", "panic"} {
		t.Run(mode, func(t *testing.T) {
			tool := Tool{Name: "bad", Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
				if mode == "typed" {
					return nil, &CallError{Code: "transport", Message: "public response was unavailable", Err: secret}
				}
				if mode == "panic" {
					panic(secret)
				}
				return nil, secret
			}}
			result, err := s.Run(t.Context(), `try { await tools.bad({}); } catch(e) { e.message="SECRET_MUTATED_MESSAGE"; e.code="SECRET_MUTATED_CODE"; e.stack="SECRET_MUTATED_STACK"; throw e; }`, RunOptions{Tools: []Tool{tool}})
			var script *ScriptError
			if !errors.As(err, &script) || !errors.Is(err, secret) || strings.Contains(script.Diagnostic, "SECRET_") || strings.Contains(script.Code, "SECRET_") || len(result.Calls) != 1 {
				t.Fatalf("host cause leaked or identity changed: %+v err=%v", script, err)
			}
			if mode == "typed" && !strings.Contains(script.Diagnostic, "public response was unavailable") {
				t.Fatalf("explicit host presentation lost: %+v", script)
			}
		})
	}
}

func TestGuestDiagnosticIsBoundedForEscapesAndUnicode(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	for _, body := range []string{`throw Error("😀".repeat(10000))`, `throw Error("\u0000".repeat(10000))`} {
		_, err := s.Run(t.Context(), body, RunOptions{})
		var script *ScriptError
		if !errors.As(err, &script) || script.Diagnostic == "" || len(script.Diagnostic) > 4096 || !utf8.ValidString(script.Diagnostic) || !strings.Contains(script.Diagnostic, "codemode.js:1") {
			t.Fatalf("diagnostic exceeded bridge bounds or lost location: %+v err=%v", script, err)
		}
	}
}

func TestNamespaceDiscoveryFiltersAndDocumentsOnlyAllowedTools(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	echo := func(context.Context, HostCall) (json.RawMessage, error) { return json.RawMessage(`null`), nil }
	tools := []Tool{
		{Name: "github_issues", Namespace: "github", Description: "Read issue", ResultDescription: "Resolves to the MCP result envelope.", Invoke: echo},
		{Name: "gitlab_issues", Namespace: "gitlab", Description: "Read issue", Invoke: echo},
	}
	metadata := []Namespace{{Name: "github", Description: "Source control", Instructions: "Use string issue identifiers."}}
	result, err := s.Run(t.Context(), `text(await searchTools("issue",{namespace:"github",limit:1})); text(await searchTools("issue",{namespace:"missing"})); const ns=await describeNamespace("github"); ns.instructions="changed"; text(ns); text(await describeTool("github_issues"));`, RunOptions{Tools: tools, Namespaces: metadata})
	if err != nil {
		t.Fatal(err)
	}
	output := textOutput(result)
	for _, want := range []string{"github_issues", "Source control", "Use string issue identifiers.", "Resolves to the MCP result envelope.", "[]"} {
		if !strings.Contains(output, want) {
			t.Fatalf("discovery missing %q: %s", want, output)
		}
	}
	if strings.Contains(output, "gitlab_issues") || strings.Contains(output, "changed") {
		t.Fatalf("namespace filter or frozen metadata failed: %s", output)
	}
	for _, values := range [][]Namespace{
		{{Name: "hidden", Instructions: "SECRET_HIDDEN_SERVER"}},
		{metadata[0], metadata[0]},
	} {
		result, err := s.Run(t.Context(), `return 1`, RunOptions{Tools: tools, Namespaces: values})
		var script *ScriptError
		if !errors.As(err, &script) || script.Code != "catalog" || len(result.Calls) != 0 || script.Diagnostic != "" || strings.Contains(err.Error(), "SECRET_") {
			t.Fatalf("unavailable metadata reached the script: %+v err=%v", result, err)
		}
	}
}

func TestSearchOptionsRejectInvalidTypesWithoutEchoingPayload(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	for _, body := range []string{
		`await searchTools("issue",{namespace:12})`,
		`await searchTools("issue",{limit:0})`,
		`await searchTools("issue",{SECRET_INVALID_PAYLOAD:true})`,
	} {
		_, err := s.Run(t.Context(), body, RunOptions{})
		var script *ScriptError
		if !errors.As(err, &script) || script.Code != "arguments" || script.Diagnostic == "" || strings.Contains(script.Diagnostic, "SECRET_INVALID_PAYLOAD") {
			t.Fatalf("invalid search lacked bounded safe guidance: %+v err=%v", script, err)
		}
	}
}

func TestOutputReservationHonorsHeaderAndConfig(t *testing.T) {
	config := DefaultConfig()
	config.MaxOutputBytes = 200
	config.MaxOutputItems = 2
	s := sandboxForTest(t, config)
	result, err := s.Run(t.Context(), "// @options: {\"max_output_tokens\":20}\ntext('x'.repeat(60)); return 'too much';", RunOptions{OutputReserveBytes: 1024})
	var script *ScriptError
	if !errors.As(err, &script) || script.Code != "output_limit" || result.OutputLimitBytes != 80 || result.OutputReservedBytes != 20 || result.OutputLimitItems != 2 || len(result.Outputs) != 1 || len(textOutput(result)) != 60 {
		t.Fatalf("reserved diagnostic widened limits or evicted partial output: %+v err=%v", result, err)
	}
	if !strings.Contains(script.Diagnostic, "filter or summarize") {
		t.Fatalf("output limit lacks a repair hint: %+v", script)
	}
}

func TestStoreLimitExplainsSmallStateWithoutEchoingValue(t *testing.T) {
	config := DefaultConfig()
	config.MaxStoreBytes = 64
	s := sandboxForTest(t, config)
	_, err := s.Run(t.Context(), `store("small", "SECRET_STORE_VALUE".repeat(5))`, RunOptions{})
	var script *ScriptError
	if !errors.As(err, &script) || script.Code != "store_limit" || !strings.Contains(script.Diagnostic, "small IDs or summaries") || strings.Contains(script.Diagnostic, "SECRET_STORE_VALUE") {
		t.Fatalf("store limit lacks safe repair guidance: %+v err=%v", script, err)
	}
}
