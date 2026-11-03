package fstools

import (
	"regexp/syntax"
	"testing"
)

func TestGrepRegexBudgetAndSupportedSyntax(t *testing.T) {
	for _, pattern := range []string{"a", "雪", "a|b|c", "(ab)?c*", "a+", "(?:a?)*", "[a-z]", "[^a]", "(?s:.)", "^a$", "\\bword\\B", "a{0}", "a{0,3}", "a{3,}", "(ab){10}", "(?i)hello", "a{1000}"} {
		ast, err := validateGrepPattern(pattern)
		if err != nil {
			t.Fatalf("%s: %v", pattern, err)
		}
		cost := regexCost(ast)
		prog, err := syntax.Compile(ast.Simplify())
		if err != nil {
			t.Fatal(err)
		}
		if len(prog.Inst) > cost+2 {
			t.Fatalf("%s estimate %d actual %d", pattern, cost, len(prog.Inst))
		}
		if _, err := compileGrepPattern(pattern, ast); err != nil {
			t.Fatalf("%s: %v", pattern, err)
		}
	}
	for _, pattern := range []string{"(", "(?=x)", "(x)\\1", "(abcdefghij){1000}", "((ab){100}){100}"} {
		if _, err := validateGrepPattern(pattern); err == nil {
			t.Fatalf("accepted %q", pattern)
		}
	}
}
