package recipes

import (
	"fmt"
	"sort"
	"sync"
)

// Registry is a thread-safe collection of registered recipes.
type Registry struct {
	mu sync.RWMutex
	m  map[string]Recipe
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{m: make(map[string]Recipe)}
}

// Register adds a recipe. Panics on duplicate type — duplicate registration is a programming error.
func (r *Registry) Register(rec Recipe) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.m[rec.Type()]; exists {
		panic(fmt.Sprintf("recipes: duplicate registration for type %q", rec.Type()))
	}
	r.m[rec.Type()] = rec
}

// Get returns the recipe for a type, or (nil, false).
func (r *Registry) Get(t string) (Recipe, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.m[t]
	return rec, ok
}

// List returns all registered recipes sorted by type.
func (r *Registry) List() []Recipe {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Recipe, 0, len(r.m))
	for _, rec := range r.m {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type() < out[j].Type() })
	return out
}

// Default is the package-level registry. Recipes register into it via init().
var Default = NewRegistry()

// Register is a shortcut for Default.Register, called from per-recipe init() blocks.
func Register(rec Recipe) {
	Default.Register(rec)
}

// Get is a shortcut for Default.Get.
func Get(t string) (Recipe, bool) {
	return Default.Get(t)
}

// List is a shortcut for Default.List.
func List() []Recipe {
	return Default.List()
}
