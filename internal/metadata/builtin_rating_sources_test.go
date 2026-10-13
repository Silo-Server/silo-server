package metadata

import (
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type declaringBuiltin struct{}

func (declaringBuiltin) Slug() string       { return "test-declaring-builtin" }
func (declaringBuiltin) Name() string       { return "Test" }
func (declaringBuiltin) ForTypes() []string { return []string{"movie"} }
func (declaringBuiltin) RatingSources() []models.RatingSourceDefinition {
	return []models.RatingSourceDefinition{{Source: "rt_critic", Name: "RT", Label: "Rotten Tomatoes critics", Scale: 100, Percent: true}}
}

type silentBuiltin struct{}

func (silentBuiltin) Slug() string       { return "test-silent-builtin" }
func (silentBuiltin) Name() string       { return "Test" }
func (silentBuiltin) ForTypes() []string { return []string{"movie"} }

// A built-in provider declares its ratings in code, since its capability row
// comes from a migration rather than a plugin manifest.
func TestBuiltinRatingSources(t *testing.T) {
	RegisterBuiltinProvider("test-declaring-builtin", func() Provider { return declaringBuiltin{} })
	RegisterBuiltinProvider("test-silent-builtin", func() Provider { return silentBuiltin{} })

	if got, want := builtinRatingSources("test-declaring-builtin"), (declaringBuiltin{}).RatingSources(); !reflect.DeepEqual(got, want) {
		t.Fatalf("builtinRatingSources = %+v, want %+v", got, want)
	}
	if got := builtinRatingSources("test-silent-builtin"); got != nil {
		t.Fatalf("a built-in that declares nothing = %+v, want nil", got)
	}
	if got := builtinRatingSources("not-registered"); got != nil {
		t.Fatalf("an unregistered capability = %+v, want nil", got)
	}
}
