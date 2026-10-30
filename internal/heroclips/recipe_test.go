package heroclips

import (
	"strings"
	"testing"
)

func TestNewRecipeClampsAndRoundsTheWidth(t *testing.T) {
	cases := []struct {
		name  string
		width int
		want  int
	}{
		{name: "unset takes the default", width: 0, want: DefaultWidth},
		{name: "negative takes the default", width: -100, want: DefaultWidth},
		{name: "below the floor is raised", width: 1, want: MinWidth},
		{name: "above the ceiling is lowered", width: 99999, want: MaxWidth},
		{name: "odd is rounded down", width: 1281, want: 1280},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewRecipe(tc.width).Width; got != tc.want {
				t.Fatalf("NewRecipe(%d).Width = %d, want %d", tc.width, got, tc.want)
			}
		})
	}
}

// The recipe string is compared against what a stored clip was made with, so it
// is a contract: it must not drift, and it must be usable in a key.
func TestRecipeStringIdentifiesTheRecipe(t *testing.T) {
	recipe := NewRecipe(DefaultWidth)

	if got, want := recipe.String(), "v1-w1280-crf28-a128k"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}

	if strings.ContainsAny(recipe.String(), "/:") {
		t.Fatalf("recipe %q must be usable in a key", recipe.String())
	}

	for _, width := range []int{MinWidth, MaxWidth} {
		if NewRecipe(width).String() == recipe.String() {
			t.Fatalf("width %d must not identify as the default recipe", width)
		}
	}
}
