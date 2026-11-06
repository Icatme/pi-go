package schedulereval

import "testing"

func TestFixtureShape(t *testing.T) {
	w := Fixture()
	if len(w.Events) != 300 || len(w.Tasks) != 20 {
		t.Fatal("fixture shape changed")
	}
	if err := w.Validate(); err != nil {
		t.Fatal(err)
	}
}
