package pigo

import "testing"

func TestKiroToolProjectionTypedNilOutputSchema(t *testing.T) {
	for _, value := range []any{nil, map[string]any(nil), map[string]any{}, map[string]any{"type": "object"}, (*map[string]any)(nil)} {
		ctx, schemas := kiroToolProjectionFixture()
		ctx.Tools[0].OutputSchema = value
		_, err := projectKiroToolConversation(ctx, "fixture-model", schemas, 10, 4, 4096)
		absent := value == nil
		if v, ok := value.(map[string]any); ok && v == nil {
			absent = true
		}
		if (err == nil) != absent {
			t.Fatal("typed-nil absence vs declared schema confused")
		}
	}
}
