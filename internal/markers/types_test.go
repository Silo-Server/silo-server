package markers

import (
	"context"
	"testing"
)

func TestNormalizeModeDefaultsToOnlineAndLocalWithoutChangingExplicitModes(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want Mode
	}{
		{"", ModeBoth},
		{" ", ModeBoth},
		{"off", ModeOff},
		{"local", ModeLocal},
		{"online", ModeOnline},
		{"both", ModeBoth},
		{"invalid", ModeLocal},
	} {
		t.Run("mode_"+tt.raw, func(t *testing.T) {
			if got := NormalizeMode(tt.raw); got != tt.want {
				t.Fatalf("NormalizeMode(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

type fakeProvider struct {
	id     string
	result Result
	err    error
	calls  int
}

func (p *fakeProvider) ID() string { return p.id }

func (p *fakeProvider) FetchMarkers(context.Context, Request) (Result, error) {
	p.calls++
	return p.result, p.err
}
