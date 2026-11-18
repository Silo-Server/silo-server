package templates

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinCatalog(t *testing.T) {
	cat := CatalogDefault()
	if len(cat.Categories) == 0 {
		t.Fatal("expected at least one category in the built-in catalog")
	}

	seenIDs := make(map[string]bool)
	for _, group := range cat.Categories {
		if group.Label == "" {
			t.Errorf("category %q has empty label", group.Category)
		}
		if len(group.Templates) == 0 {
			t.Errorf("category %q has no templates", group.Category)
		}
		for _, tmpl := range group.Templates {
			if seenIDs[tmpl.ID] {
				t.Errorf("duplicate template id %q", tmpl.ID)
			}
			if strings.TrimSpace(tmpl.PosterPath) == "" {
				t.Errorf("template %q has empty poster path", tmpl.ID)
			}
			if tmpl.PosterPath != "" && !strings.HasPrefix(tmpl.PosterPath, "/images/collection-templates/") {
				t.Errorf("template %q has invalid poster path %q", tmpl.ID, tmpl.PosterPath)
			}
			seenIDs[tmpl.ID] = true
		}
	}
	kids, ok := Get("tmdb_discover_kids_movies")
	if !ok || kids.TMDBDiscover == nil || kids.TMDBDiscover.CertificationLte != "PG" {
		t.Fatalf("Kids Movies must keep its PG certification ceiling: %+v", kids)
	}
}

func TestBuiltinTemplatePosterAssetsExist(t *testing.T) {
	assetRoot := filepath.Join("..", "..", "..", "web", "public", "images", "collection-templates")

	for _, tmpl := range List() {
		t.Run(tmpl.ID, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(assetRoot, tmpl.ID+".jpg")); err != nil {
				t.Fatalf("final poster asset missing: %v", err)
			}
		})
	}
}

func TestBuiltinTemplateSourcePlatesStayOutOfPublicAssets(t *testing.T) {
	webRoot := filepath.Join("..", "..", "..", "web")
	sourceRoot := filepath.Join(webRoot, "assets-source", "collection-templates", "raw")
	publicRoot := filepath.Join(webRoot, "public", "images", "collection-templates", "raw")

	if _, err := os.Stat(publicRoot); !os.IsNotExist(err) {
		t.Fatalf("raw poster plates must not be public assets: %v", err)
	}

	for _, tmpl := range List() {
		t.Run(tmpl.ID, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(sourceRoot, tmpl.ID+".png")); err != nil {
				t.Fatalf("raw poster plate missing: %v", err)
			}
		})
	}
}

// retiredTemplatePosterIDs lists removed templates whose final poster still
// ships. A collection created from a template stores the template's poster
// path and keeps it unless the poster was copied into artwork storage, so
// deleting one of these JPGs would blank that collection's poster. Remove an ID
// together with its JPG once no stored poster path can point at it. Retired
// templates keep no raw plate.
var retiredTemplatePosterIDs = map[string]bool{
	// Trakt templates, removed with Trakt-backed collection creation.
	"trakt_popular_movies":     true,
	"trakt_popular_shows":      true,
	"trakt_recommended_movies": true,
	"trakt_recommended_shows":  true,
	"trakt_trending_movies":    true,
	"trakt_trending_shows":     true,
}

// Files an OS or file manager may leave in an asset directory.
var ignoredAssetDirEntries = map[string]bool{"Thumbs.db": true, "desktop.ini": true}

func TestBuiltinTemplateAssetsHaveTemplates(t *testing.T) {
	webRoot := filepath.Join("..", "..", "..", "web")
	registered := make(map[string]bool)
	for _, tmpl := range List() {
		registered[tmpl.ID] = true
	}
	retired := slices.Sorted(maps.Keys(retiredTemplatePosterIDs))

	for _, id := range retired {
		if registered[id] {
			t.Errorf("retired template %q is registered again; drop it from retiredTemplatePosterIDs", id)
		}
	}

	dirs := []struct {
		path         string
		ext          string
		allowRetired bool
	}{
		{filepath.Join(webRoot, "public", "images", "collection-templates"), ".jpg", true},
		{filepath.Join(webRoot, "assets-source", "collection-templates", "raw"), ".png", false},
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir.path)
		if err != nil {
			t.Fatalf("read %s: %v", dir.path, err)
		}
		found := make(map[string]bool)
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") || ignoredAssetDirEntries[name] {
				continue
			}
			id, ok := strings.CutSuffix(name, dir.ext)
			if ok {
				found[id] = true
			}
			switch {
			case entry.IsDir() || !ok:
				t.Errorf("%s: unexpected entry %q; only {template id}%s files belong here", dir.path, name, dir.ext)
			case registered[id]:
			case dir.allowRetired && retiredTemplatePosterIDs[id]:
			case dir.allowRetired:
				t.Errorf("%s has no registered template; delete it, or add %q to retiredTemplatePosterIDs if existing collections still use it", filepath.Join(dir.path, name), id)
			default:
				t.Errorf("%s has no registered template; delete it", filepath.Join(dir.path, name))
			}
		}
		if !dir.allowRetired {
			continue
		}
		for _, id := range retired {
			if !found[id] {
				t.Errorf("%s is missing; restore it, or drop %q from retiredTemplatePosterIDs once no stored poster path can point at it", filepath.Join(dir.path, id+dir.ext), id)
			}
		}
	}
}

func TestBundleOnlyTemplatesAreReachableFromBundles(t *testing.T) {
	bundled := make(map[string]bool)
	for _, bundle := range ListBundles() {
		for _, id := range bundle.TemplateIDs {
			bundled[id] = true
		}
	}

	for _, tmpl := range List() {
		switch tmpl.Source {
		case SourceTMDBDiscover, SourceTMDBCollection:
			if !bundled[tmpl.ID] {
				t.Errorf("bundle-only template %q is not referenced by any bundle", tmpl.ID)
			}
		}
	}
}
