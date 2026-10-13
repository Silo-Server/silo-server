package templates

import "sync"

// Registry holds the curated catalog of collection templates. Built-in entries
// are registered via init(); custom installations could add their own through
// Register().
type Registry struct {
	mu sync.RWMutex
	// templates preserves insertion order for stable presentation.
	templates []Template
	// byID enables O(1) lookups for template instantiation.
	byID map[string]int
	// bundles preserves insertion order for stable presentation.
	bundles []Bundle
	// byBundleID enables O(1) bundle lookup.
	byBundleID map[string]int
	// categoryOrder is the display order for category groups in Catalog().
	categoryOrder []Category
}

// NewRegistry returns an empty registry with the default category ordering.
func NewRegistry() *Registry {
	return &Registry{
		byID:       make(map[string]int),
		byBundleID: make(map[string]int),
		categoryOrder: []Category{
			CategoryTrending,
			CategoryPopular,
			CategoryStreaming,
			CategoryTopRated,
			CategoryInTheaters,
			CategoryUpcoming,
			CategoryAiring,
			CategoryEditorial,
			CategoryCustom,
		},
	}
}

// Register adds a template. Panics on duplicate ID — duplicates are a
// programming error, not a runtime concern.
func (r *Registry) Register(t Template) {
	if err := validate(t); err != nil {
		panic("templates: invalid template " + t.ID + ": " + err.Error())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[t.ID]; exists {
		panic("templates: duplicate template ID " + t.ID)
	}
	r.byID[t.ID] = len(r.templates)
	r.templates = append(r.templates, t)
}

// RegisterBundle adds a template bundle. Panics on duplicate IDs or invalid
// references because bundles are curated startup data.
func (r *Registry) RegisterBundle(b Bundle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.validateBundleLocked(b); err != nil {
		panic("templates: invalid bundle " + b.ID + ": " + err.Error())
	}
	if _, exists := r.byBundleID[b.ID]; exists {
		panic("templates: duplicate bundle ID " + b.ID)
	}
	r.byBundleID[b.ID] = len(r.bundles)
	r.bundles = append(r.bundles, b)
}

// Get returns the template with the given ID.
func (r *Registry) Get(id string) (Template, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	idx, ok := r.byID[id]
	if !ok {
		return Template{}, false
	}
	return r.templates[idx], true
}

// GetBundle returns the bundle with the given ID.
func (r *Registry) GetBundle(id string) (Bundle, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	idx, ok := r.byBundleID[id]
	if !ok {
		return Bundle{}, false
	}
	return r.bundles[idx], true
}

// List returns every registered template in registration order.
func (r *Registry) List() []Template {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Template, len(r.templates))
	copy(out, r.templates)
	return out
}

// ListBundles returns every registered bundle in registration order.
func (r *Registry) ListBundles() []Bundle {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Bundle, len(r.bundles))
	copy(out, r.bundles)
	return out
}

// BundleCatalog returns all registered bundles in stable display order.
func (r *Registry) BundleCatalog() BundleCatalog {
	return BundleCatalog{Bundles: r.ListBundles()}
}

// BundlesWithTemplates returns every registered bundle with its templates in
// the bundle's template_ids order.
func (r *Registry) BundlesWithTemplates() []BundleWithTemplates {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]BundleWithTemplates, 0, len(r.bundles))
	for _, b := range r.bundles {
		listed := make([]Template, 0, len(b.TemplateIDs))
		for _, id := range b.TemplateIDs {
			// RegisterBundle panics on unknown template IDs, so every
			// reference resolves and Templates always matches TemplateIDs.
			if idx, ok := r.byID[id]; ok {
				listed = append(listed, r.templates[idx])
			}
		}
		out = append(out, BundleWithTemplates{Bundle: b, Templates: listed})
	}
	return out
}

// Catalog returns templates grouped by category, in display order. Empty
// categories are omitted. Categories not in the configured order appear last
// in the order they were first registered.
func (r *Registry) Catalog() Catalog {
	r.mu.RLock()
	defer r.mu.RUnlock()

	groups := make(map[Category][]Template)
	seenCategories := make([]Category, 0)
	for _, t := range r.templates {
		if _, ok := groups[t.Category]; !ok {
			seenCategories = append(seenCategories, t.Category)
		}
		groups[t.Category] = append(groups[t.Category], t)
	}

	// Build the final ordering: configured order first, then any extras.
	ordered := make([]Category, 0, len(groups))
	included := make(map[Category]bool)
	for _, c := range r.categoryOrder {
		if _, ok := groups[c]; ok {
			ordered = append(ordered, c)
			included[c] = true
		}
	}
	for _, c := range seenCategories {
		if !included[c] {
			ordered = append(ordered, c)
		}
	}

	out := Catalog{Categories: make([]CategoryGroup, 0, len(ordered))}
	for _, c := range ordered {
		out.Categories = append(out.Categories, CategoryGroup{
			Category:  c,
			Label:     CategoryLabel(c),
			Templates: groups[c],
		})
	}
	return out
}

// Default is the package-level registry seeded with built-in templates.
var Default = NewRegistry()

// Register is shorthand for Default.Register.
func Register(t Template) { Default.Register(t) }

// RegisterBundle is shorthand for Default.RegisterBundle.
func RegisterBundle(b Bundle) { Default.RegisterBundle(b) }

// Get is shorthand for Default.Get.
func Get(id string) (Template, bool) { return Default.Get(id) }

// GetBundle is shorthand for Default.GetBundle.
func GetBundle(id string) (Bundle, bool) { return Default.GetBundle(id) }

// List is shorthand for Default.List.
func List() []Template { return Default.List() }

// ListBundles is shorthand for Default.ListBundles.
func ListBundles() []Bundle { return Default.ListBundles() }

// CatalogDefault is shorthand for Default.Catalog.
func CatalogDefault() Catalog { return Default.Catalog() }

// BundleCatalogDefault is shorthand for Default.BundleCatalog.
func BundleCatalogDefault() BundleCatalog { return Default.BundleCatalog() }
