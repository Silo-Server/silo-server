package telemetry

import "testing"

func TestClientLabelIsBounded(t *testing.T) {
	for i := 0; i < 10000; i++ {
		if got := ClientLabel("private-client-" + string(rune(i))); got != "other" {
			t.Fatalf("arbitrary client created label %q", got)
		}
	}
	for name, want := range map[string]string{"": "none", "Silo Web": "web", "Silo Apple TV": "apple", "Silo iOS": "apple", "Silo Android TV": "android", "Silo Android": "android", "silo web private": "other"} {
		if got := ClientLabel(name); got != want {
			t.Fatalf("client %q => %q, want %q", name, got, want)
		}
	}
}
