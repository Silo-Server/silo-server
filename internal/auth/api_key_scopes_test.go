package auth

import (
	"slices"
	"testing"
)

func TestNormalizeAPIKeyScopes(t *testing.T) {
	t.Run("nil means unscoped", func(t *testing.T) {
		got, err := NormalizeAPIKeyScopes(nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})

	t.Run("dedupes and sorts", func(t *testing.T) {
		got, err := NormalizeAPIKeyScopes([]string{
			ScopeAdminUsers, ScopeAdminAccessGroupsRead, ScopeAdminUsers,
		})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{ScopeAdminAccessGroupsRead, ScopeAdminUsers}
		if !slices.Equal(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("rejects unknown scope", func(t *testing.T) {
		if _, err := NormalizeAPIKeyScopes([]string{"admin:everything"}); err == nil {
			t.Fatal("expected error for unknown scope")
		}
	})

	t.Run("rejects empty string scope", func(t *testing.T) {
		if _, err := NormalizeAPIKeyScopes([]string{""}); err == nil {
			t.Fatal("expected error for empty scope")
		}
	})
}
