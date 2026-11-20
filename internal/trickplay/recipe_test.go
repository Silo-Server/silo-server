package trickplay

import (
	"testing"
)

func TestNewRecipeClampsSettings(t *testing.T) {
	tests := []struct {
		width, interval int
		want            Recipe
	}{
		{300, 10, Recipe{Width: 300, IntervalMS: 10000}},
		{301, 10, Recipe{Width: 300, IntervalMS: 10000}},
		{50, 1, Recipe{Width: 160, IntervalMS: 5000}},
		{4000, 600, Recipe{Width: 640, IntervalMS: 60000}},
	}
	for _, tt := range tests {
		if got := NewRecipe(tt.width, tt.interval); got != tt.want {
			t.Errorf("NewRecipe(%d, %d) = %+v, want %+v", tt.width, tt.interval, got, tt.want)
		}
	}
	if got := NewRecipe(300, 10).String(); got != "v1/w300/i10000/q80" {
		t.Errorf("recipe string %q", got)
	}
}
