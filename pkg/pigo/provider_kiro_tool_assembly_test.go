package pigo

import (
	"errors"
	"encoding/json"
	"strings"
	"testing"
)

func kiroTestAssembler(t *testing.T) *kiroToolAssembler {
	t.Helper()
	a, err := newKiroToolAssembler(1024, 4096, 20, 3)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestKiroToolAssemblyExplicitStop(t *testing.T) {
	a := kiroTestAssembler(t)
	for _, payload := range []string{
		`{"name":"lookup","toolUseId":"fixture-1","input":{}}`,
		`{"input":"{\"q\":"}`, `{"input":"\"demo\"}"}`,
	} {
		if call, err := a.accept([]byte(payload)); err != nil || call != nil {
			t.Fatalf("premature completion: %#v, %v", call, err)
		}
	}
	call, err := a.accept([]byte(`{"stop":true}`))
	if err != nil || call == nil || call.ID != "fixture-1" || call.Name != "lookup" || string(call.Arguments) != `{"q":"demo"}` {
		t.Fatalf("assembled call: %#v, %v", call, err)
	}
	if err := a.finish(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.accept([]byte(`{"stop":true}`)); err == nil {
		t.Fatal("accepted input after finish")
	}
}

func TestKiroToolAssemblyObjectAndZeroArgs(t *testing.T) {
	for _, input := range []string{`{}`, `""`, `{"x":9007199254740993}`} {
		a := kiroTestAssembler(t)
		call, err := a.accept([]byte(`{"name":"fixture","toolUseId":"id","input":` + input + `,"stop":true}`))
		want := input
		if input == `""` {
			want = `{}`
		}
		if err != nil || call == nil || string(call.Arguments) != want {
			t.Fatalf("%s: %#v, %v", input, call, err)
		}
	}
}

func TestKiroToolAssemblyRejectsAndSticks(t *testing.T) {
	cases := [][]string{
		{`{"input":"{}"}`}, {`{"stop":true}`}, {`{}`},
		{`{"name":"x","toolUseId":"1","stop":null}`},
		{`{"name":"x","toolUseId":"1","input":[]}`},
		{`{"name":"x","toolUseId":"1","input":null}`},
		{`{"name":"x","toolUseId":"1","unknown":true}`},
		{`{"name":"x","toolUseId":"1","input":"{","stop":true}`},
		{`{"name":"x","toolUseId":"1","input":"[]","stop":true}`},
		{`{"name":"x","toolUseId":"1","input":"{\"a\":1,\"a\":2}","stop":true}`},
		{`{"name":"x","toolUseId":"1","input":{}}`, `{"name":"y","toolUseId":"2"}`},
		{`{"name":"x","toolUseId":"1"}`, `{"name":"y","toolUseId":"1"}`},
		{`{"name":"x","toolUseId":"1","stop":true}`, `{"name":"x","toolUseId":"1"}`},
		{`{"name":"x","toolUseId":"1","toolUseId":"2"}`},
		{`{"name":"\ud800","toolUseId":"1"}`},
		{`{"name":"x","toolUseId":"\udc00"}`},
		{`{"name":"x","toolUseId":"1","input":"\ud800"}`},
	}
	for _, sequence := range cases {
		a := kiroTestAssembler(t)
		for i, input := range sequence {
			_, err := a.accept([]byte(input))
			if (err != nil) != (i == len(sequence)-1) {
				t.Fatalf("sequence %q at %d: %v", sequence, i, err)
			}
		}
		if _, err := a.accept([]byte(`{"name":"fresh","toolUseId":"fresh","stop":true}`)); !errors.Is(err, errKiroToolAssembly) {
			t.Fatal("terminal error reset")
		}
		if !errors.Is(a.finish(), errKiroToolAssembly) {
			t.Fatal("finish ignored terminal error")
		}
	}
}

func TestKiroToolAssemblyBudgetsAndEOF(t *testing.T) {
	a, _ := newKiroToolAssembler(2, 1024, 5, 2)
	if _, err := a.accept([]byte(`{"name":"x","toolUseId":"1","input":"123"}`)); err == nil {
		t.Fatal("argument limit ignored")
	}
	a, _ = newKiroToolAssembler(1024, 2, 5, 2)
	if _, err := a.accept([]byte(`{"name":"x","toolUseId":"1"}`)); err == nil {
		t.Fatal("event byte limit ignored")
	}
	a, _ = newKiroToolAssembler(1024, 1024, 1, 2)
	_, _ = a.accept([]byte(`{"name":"x","toolUseId":"1"}`))
	if _, err := a.accept([]byte(`{"stop":true}`)); err == nil {
		t.Fatal("event count limit ignored")
	}
	a, _ = newKiroToolAssembler(1024, 1024, 5, 1)
	_, _ = a.accept([]byte(`{"name":"x","toolUseId":"1","stop":true}`))
	if _, err := a.accept([]byte(`{"name":"x","toolUseId":"2"}`)); err == nil {
		t.Fatal("tool count limit ignored")
	}
	a = kiroTestAssembler(t)
	_, _ = a.accept([]byte(`{"name":"x","toolUseId":"1","input":"{}"}`))
	if a.finish() == nil {
		t.Fatal("EOF implicitly completed tool without stop")
	}
}

func TestKiroToolAssemblySequentialAndRepeatedIdentity(t *testing.T) {
	a := kiroTestAssembler(t)
	if _, err := a.accept([]byte(`{"name":"x","toolUseId":"1","input":"{"}`)); err != nil {
		t.Fatal(err)
	}
	call, err := a.accept([]byte(`{"name":"x","toolUseId":"1","input":"}","stop":true}`))
	if err != nil || call == nil || string(call.Arguments) != "{}" {
		t.Fatalf("repeated identity: %#v, %v", call, err)
	}
	second, err := a.accept([]byte(`{"name":"x","toolUseId":"2","stop":true}`))
	if err != nil || second == nil || second.ID != "2" || call.ID != "1" {
		t.Fatalf("sequential calls: %#v, %v", second, err)
	}
	if err := a.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestKiroToolAssemblyConfiguration(t *testing.T) {
	for _, limits := range [][4]int{{1, 2, 1, 1}, {2, 1, 1, 1}, {2, 2, 0, 1}, {2, 2, 1, 0}} {
		if _, err := newKiroToolAssembler(limits[0], limits[1], limits[2], limits[3]); err == nil {
			t.Fatalf("invalid limits accepted: %v", limits)
		}
	}
}

func FuzzKiroToolAssembly(f *testing.F) {
	f.Add([]byte(`{"name":"x","toolUseId":"1","input":{}}`), []byte(`{"stop":true}`))
	f.Add([]byte(`{"input":"{}"}`), []byte(`{}`))
	f.Fuzz(func(t *testing.T, first, second []byte) {
		a, err := newKiroToolAssembler(4096, 8192, 2, 2)
		if err != nil {
			t.Fatal(err)
		}
		_, firstErr := a.accept(first)
		_, secondErr := a.accept(second)
		if firstErr != nil && secondErr != firstErr {
			t.Fatal("terminal error changed")
		}
		_ = a.finish()
	})
}

func TestKiroToolAssemblyNativeIDBoundary(t *testing.T) {
 for _, id := range []string{"", "中", "a/b", strings.Repeat("x", 65)} {
  a := kiroTestAssembler(t)
  payload, _ := json.Marshal(map[string]any{"name":"lookup", "toolUseId":id, "input":map[string]any{}, "stop":true})
  if tool, err := a.accept(payload); err == nil || tool != nil { t.Fatalf("accepted unsupported ID %q", id) }
 }
 for _, id := range []string{"aA0_.:-", strings.Repeat("x", 64)} {
  a := kiroTestAssembler(t)
  payload, _ := json.Marshal(map[string]any{"name":"lookup", "toolUseId":id, "input":map[string]any{}, "stop":true})
  tool, err := a.accept(payload)
  if err != nil || tool == nil || tool.ID != id { t.Fatalf("native ID rejected: %q, %v", id, err) }
 }
}
