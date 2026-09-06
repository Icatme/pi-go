package pigo

import (
	"errors"
	"strings"
	"testing"
)

func TestSSEEventCumulativeLimit(t *testing.T) {
	for _, input := range []string{strings.Repeat("data: xx\n", 22), strings.Repeat("data:\n", 65), "data: " + strings.Repeat("x", 80) + "\n"} {
		called := false
		err := readSSEStreamLimit(strings.NewReader(input), func(string, string) (bool, error) { called = true; return false, nil }, 64)
		if !errors.Is(err, ErrSSEEventTooLarge) || called {
			t.Fatalf("err=%v called=%v", err, called)
		}
	}
}
func TestSSEEventLimitResetsAndStops(t *testing.T) {
	count := 0
	err := readSSEStreamLimit(strings.NewReader(strings.Repeat("event: message\ndata: first\ndata: second\n\n", 100)), func(event, data string) (bool, error) {
		count++
		if event != "message" || data != "first\nsecond" {
			t.Fatalf("event=%q data=%q", event, data)
		}
		return count == 3, nil
	}, 64)
	if err != nil || count != 3 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}
