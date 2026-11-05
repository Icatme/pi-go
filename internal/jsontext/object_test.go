package jsontext

import (
	"errors"
	"testing"
)

func TestValidateObject(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		depth int
		want  error
	}{
		{"{}", 1, nil},
		{`{"one":[{"n":1e1000}],"two":{"n":1}}`, 3, nil},
		{`{"calls":[{"path":"a","\u0070ath":"b"}]}`, 3, ErrDuplicateName},
		{`{"a":[{}]}`, 2, ErrValueLimit},
		{`{"a":"\ud800"}`, 1, ErrInvalidUnicode},
		{"{} {}", 1, ErrInvalidJSON},
		{"null", 1, ErrInvalidJSON},
	} {
		if err := ValidateObject([]byte(tc.raw), tc.depth); !errors.Is(err, tc.want) {
			t.Errorf("ValidateObject(%s, %d) = %v, want %v", tc.raw, tc.depth, err, tc.want)
		}
	}
}
