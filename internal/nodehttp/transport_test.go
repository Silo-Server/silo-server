package nodehttp

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestTransportConfigurationKeepsRecipeClassificationAndResetTogether(t *testing.T) {
	Configure(nil)
	t.Cleanup(func() { Configure(nil) })
	if requires, err := RequiresRecipe(t.Context(), "http://legacy.example/prefix"); err != nil || requires {
		t.Fatal("ordinary legacy transport unexpectedly requires a durable recipe", err)
	}
	denied := errors.New("registered authority unavailable")
	classifier := func(_ context.Context, origin string) (bool, error) {
		if origin == "https://registered.example" {
			return true, nil
		}
		return false, denied
	}
	Configure(func(base http.RoundTripper) http.RoundTripper { return base }, classifier)
	if requires, err := RequiresRecipe(t.Context(), "https://registered.example"); err != nil || !requires {
		t.Fatal("registered worker lost durable recipe requirement", err)
	}
	if _, err := RequiresRecipe(t.Context(), "https://edited.example"); !errors.Is(err, denied) {
		t.Fatal("classifier failure fell back to legacy transport", err)
	}
	Configure(func(base http.RoundTripper) http.RoundTripper { return base })
	if requires, err := RequiresRecipe(t.Context(), "https://registered.example"); err != nil || requires {
		t.Fatal("replaced legacy transport retained stale standalone classification", err)
	}
	Configure(nil, classifier)
	if requires, err := RequiresRecipe(t.Context(), "https://registered.example"); err != nil || requires {
		t.Fatal("nil transport retained standalone classification", err)
	}
}
