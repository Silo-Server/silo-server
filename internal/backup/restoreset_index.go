package backup

import (
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
)

// restoreSetIndex finds a restore set's entries by the ids and keys planning
// and restoring look them up by, so neither scans a list once per entry: a
// set holds up to maxRestoreSetEntries files and as many entries as its
// documents fit. It is built once, on first use, from a set nothing changes
// after its read (RestoreSet.lookup).
type restoreSetIndex struct {
	libraryNames map[int]string
	// By preview item id.
	collections, templates, sections, artwork, branding map[string]int
	plugins                                             map[string]int
	// By backup id: a collection's or template collection's, a section's;
	// sectionCollections by the id of the collection a section shows.
	collectionIDs, templateIDs, sectionIDs, sectionCollections map[string]int
	// variants lists, by original, the blob keys stored beside it
	// (ladderSibling), sorted; legacyKeys are, by directory, the legacy
	// originals (legacyOriginalName).
	variants   map[string][]string
	legacyKeys map[string][]string
	// The backup's entries that count as twins of one another, by what
	// makes them twins, and then by their backup library (noLibrary for
	// none): sectionTwins, collectionTwins, templateTwins.
	sectionTwins    map[sectionTwinKey]map[int]int
	collectionTwins map[string]map[int]int
	templateTwins   map[[2]string]map[int]int
}

// noLibrary stands for a section without a library in sectionTwins.
const noLibrary = -1

type sectionTwinKey struct{ scope, sectionType, title string }

// lookup is the set's index.
func (rs *RestoreSet) lookup() *restoreSetIndex {
	rs.indexOnce.Do(func() { rs.index = buildRestoreSetIndex(rs) })
	return rs.index
}

func buildRestoreSetIndex(rs *RestoreSet) *restoreSetIndex {
	x := &restoreSetIndex{
		libraryNames: map[int]string{}, collections: map[string]int{}, templates: map[string]int{},
		sections: map[string]int{}, artwork: map[string]int{}, branding: map[string]int{}, plugins: map[string]int{},
		collectionIDs: map[string]int{}, templateIDs: map[string]int{}, sectionIDs: map[string]int{}, sectionCollections: map[string]int{},
		variants: map[string][]string{}, legacyKeys: map[string][]string{},
		sectionTwins: map[sectionTwinKey]map[int]int{}, collectionTwins: map[string]map[int]int{}, templateTwins: map[[2]string]map[int]int{},
	}
	// The reader refuses two entries with one item id, so the first is
	// the only one.
	first := func(m map[string]int, key string, i int) {
		if _, ok := m[key]; !ok {
			m[key] = i
		}
	}
	count := func(m map[int]int, library int) map[int]int {
		if m == nil {
			m = map[int]int{}
		}
		m[library]++
		return m
	}
	for _, l := range rs.Libraries.Libraries {
		if _, ok := x.libraryNames[l.ID]; !ok {
			x.libraryNames[l.ID] = l.Name
		}
	}
	for i, c := range rs.Collections.Collections {
		first(x.collections, collectionItemID(c.ID), i)
		first(x.collectionIDs, c.ID, i)
		key := foldKey(c.Title)
		x.collectionTwins[key] = count(x.collectionTwins[key], c.LibraryID)
	}
	for i, t := range rs.Collections.TemplateCollections {
		first(x.templates, t.itemID(), i)
		first(x.templateIDs, t.ID, i)
		key := [2]string{t.BundleID, t.TemplateID}
		x.templateTwins[key] = count(x.templateTwins[key], t.LibraryID)
	}
	for i, a := range rs.Collections.ItemArtwork {
		first(x.artwork, a.itemID(), i)
	}
	for i, b := range rs.Collections.Branding {
		first(x.branding, b.itemID(), i)
	}
	for i, e := range rs.HomeSections.Sections {
		first(x.sections, e.itemID(), i)
		first(x.sectionIDs, e.ID, i)
		if e.Collection != nil {
			first(x.sectionCollections, e.Collection.ID, i)
		}
		library := noLibrary
		if e.LibraryID != nil {
			library = *e.LibraryID
		}
		key := sectionTwinKey{e.Scope, e.SectionType, e.Title}
		x.sectionTwins[key] = count(x.sectionTwins[key], library)
	}
	for i, doc := range rs.Plugins {
		first(x.plugins, doc.PluginID, i)
	}
	// A family is grouped in one pass: a revision's variants by the
	// original they name, legacy ones (the writer's legacy names only) by
	// the legacy originals of their directory, of which there is one per
	// image format. Each family keeps at most maxLadderVariants members.
	for key := range rs.blobs {
		switch {
		case artworkkey.Revision(key) != "":
			if original := artworkkey.OriginalOf(key); original != key {
				x.variants[original] = append(x.variants[original], key)
			}
		case legacyOriginalName(key):
			dir := path.Dir(key) + "/"
			x.legacyKeys[dir] = append(x.legacyKeys[dir], key)
		}
	}
	for key := range rs.blobs {
		if artworkkey.Revision(key) != "" || !legacyVariantName(key) {
			continue
		}
		for _, original := range x.legacyKeys[path.Dir(key)+"/"] {
			if ladderSibling(original, key) {
				x.variants[original] = append(x.variants[original], key)
			}
		}
	}
	for original, keys := range x.variants {
		slices.Sort(keys)
		x.variants[original] = keys[:min(len(keys), maxLadderVariants)]
	}
	return x
}

// foldKey is the same for two strings exactly when strings.EqualFold
// reports them equal: each rune becomes the smallest of its case folding
// orbit.
func foldKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		least := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least = min(least, f)
		}
		b.WriteRune(least)
	}
	return b.String()
}

// indexed returns the item at m[id] of list, or nil.
func indexed[T any](list []T, m map[string]int, id string) *T {
	if i, ok := m[id]; ok {
		return &list[i]
	}
	return nil
}
