package fstools

import (
	"errors"
	"regexp"
	"regexp/syntax"
)

const maxGrepPatternBytes = 256
const maxGrepRegexInstructions = 4096

func validateGrepPattern(pattern string) (*syntax.Regexp, error) {
	if pattern == "" || len(pattern) > maxGrepPatternBytes {
		return nil, errors.New("unsupported regexp length")
	}
	ast, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, err
	}
	// Bound counted-repeat expansion before Simplify/Compile allocates it. This
	// is deliberately conservative, not a promise about total allocator bytes.
	if regexCost(ast) >= maxGrepRegexInstructions {
		return nil, errors.New("regexp expansion exceeds instruction budget")
	}
	return ast, nil
}

func compileGrepPattern(pattern string, ast *syntax.Regexp) (*regexp.Regexp, error) {
	program, err := syntax.Compile(ast.Simplify())
	if err != nil {
		return nil, err
	}
	if len(program.Inst) > maxGrepRegexInstructions {
		return nil, errors.New("regexp exceeds instruction budget")
	}
	return regexp.Compile(pattern)
}

// regexCost saturates at limit+1. Extra instructions are reserved for capture,
// alternation and nullable repeats; the final program also has fail/match instructions.
func regexCost(re *syntax.Regexp) int {
	add := func(a, b int) int {
		if a > maxGrepRegexInstructions || b > maxGrepRegexInstructions || a > maxGrepRegexInstructions-b {
			return maxGrepRegexInstructions + 1
		}
		return a + b
	}
	multiply := func(a, n int) int {
		if n == 0 {
			return 0
		}
		if n < 0 || a > maxGrepRegexInstructions/n {
			return maxGrepRegexInstructions + 1
		}
		return a * n
	}
	child := func() int {
		if len(re.Sub) != 1 {
			return maxGrepRegexInstructions + 1
		}
		return regexCost(re.Sub[0])
	}
	switch re.Op {
	case syntax.OpNoMatch, syntax.OpEmptyMatch, syntax.OpCharClass, syntax.OpAnyCharNotNL, syntax.OpAnyChar, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return 1
	case syntax.OpLiteral:
		return max(1, len(re.Rune))
	case syntax.OpCapture:
		return add(child(), 2)
	case syntax.OpStar, syntax.OpPlus, syntax.OpQuest:
		return add(child(), 2)
	case syntax.OpConcat, syntax.OpAlternate:
		total := 1
		for _, sub := range re.Sub {
			total = add(total, regexCost(sub))
			if re.Op == syntax.OpAlternate {
				total = add(total, 1)
			}
		}
		return total
	case syntax.OpRepeat:
		copies := re.Max
		if copies < 0 {
			copies = re.Min + 1
		}
		return add(multiply(add(child(), 2), copies), 2)
	default:
		return maxGrepRegexInstructions + 1
	}
}
