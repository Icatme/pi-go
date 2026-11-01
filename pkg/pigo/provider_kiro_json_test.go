package pigo

import (
	"strings"
	"testing"
)

func TestKiroJSONRejectsAmbiguity(t *testing.T) {
	for _, s := range []string{
		`{"a":1,"a":2}`, `{"a":1,"\u0061":2}`,
		`{"nested":{"a":1,"a":2}}`, `{"array":[{"a":1,"a":2}]}`,
		`null`, `[]`, `{} {}`, `{"a":NaN}`, "\u00a0{}", "{\"x\":\"\xff\"}",
		`{"x":"\ud800"}`, `{"x":"\udc00"}`, `{"x":"\ud800\u0061"}`,
		`{"\ud800":1}`, `{"x":"\ud800\ud800"}`,
		`{"x":` + strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64) + "}",
	} {
		if _, err := decodeKiroJSONObject([]byte(s), len(s)); err == nil {
			t.Errorf("accepted invalid/ambiguous object: %q", s)
		}
	}
}

func TestKiroJSONUnicodeEscapes(t *testing.T) {
	for _, s := range []string{`{"x":"\ud83d\ude00"}`, `{"x":"\\ud800"}`, `{"x":"escaped \" quote"}`, `{"x":"中😀"}`} {
		if _, err := decodeKiroJSONObject([]byte(s), len(s)); err != nil {
			t.Errorf("valid Unicode rejected: %s: %v", s, err)
		}
	}
}

func TestKiroJSONPreservesRawNumbersAndNestedObjects(t *testing.T) {
	s := `{"n":9007199254740993,"x":[{"a":1},{"a":2}],"empty":{}}`
	m, err := decodeKiroJSONObject([]byte(s), len(s))
	if err != nil || string(m["n"]) != "9007199254740993" {
		t.Fatalf("raw numeric value lost: %#v, %v", m, err)
	}
	if _, err := decodeKiroJSONObject([]byte(s), len(s)-1); err == nil {
		t.Fatal("byte budget ignored")
	}
}

func FuzzKiroJSONObject(f *testing.F) {
	f.Add([]byte(`{"x":[{"a":1}]}`))
	f.Add([]byte(`{"x":1,"x":2}`))
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = decodeKiroJSONObject(input, 64*1024)
	})
}
