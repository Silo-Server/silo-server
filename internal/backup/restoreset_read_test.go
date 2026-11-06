package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/backup/jsonsize"
	"github.com/Silo-Server/silo-server/internal/backup/userdata"
)

// mutate changes one thing about a valid restore set.
type mutate = func(index *RestoreSetIndex, entries *[]testEntry)

// testEntry is one tar entry a hostile-archive case can rewrite freely.
type testEntry struct {
	name     string
	data     []byte
	typeflag byte
	linkname string
	size     int64 // overrides len(data) when non-zero
	// beforeIndex places the entry ahead of restore-set.json.
	beforeIndex bool
}

// testRestoreSet is a valid restore set: every section, a plugin with its
// archive, and blobs for each section that carries them, including an
// artwork variant beside its original.
func testRestoreSet() (RestoreSetIndex, []testEntry) {
	settings := mustJSON(SettingsDocument{Settings: []SettingEntry{{Key: "branding.server_name", Value: "Home"}}})
	libraries := mustJSON(LibrariesDocument{Libraries: []LibraryEntry{{ID: 3, Name: "Movies", Type: "movies", Paths: []string{"/media/movies"}, TrailerKinds: []string{}, Poster: "library-posters/3.jpg"}}})
	archive := []byte("PK-not-really-a-zip")
	archiveSum := sha256.Sum256(archive)
	archiveDigest := FileDigest{Path: "plugins/tmdb/archive.bin", Size: int64(len(archive)), SHA256: hex.EncodeToString(archiveSum[:])}
	plugin := mustJSON(PluginDocument{PluginID: "tmdb", Version: "1.0.0", Enabled: true, InstallationID: 7, Archive: archiveDigest,
		Configs: []PluginConfigEntry{{Key: "global", Value: json.RawMessage(`{"a":1}`)}}})
	original := "collection-images/c1/poster/original.abcd.jpg"
	collections := mustJSON(CollectionsDocument{
		Collections: []CollectionEntry{{ID: "c1", LibraryID: 3, Libraries: []CollectionLib{{LibraryID: 3}}, Title: "Favorites", Poster: original, Items: []string{"m1"}}},
		TemplateCollections: []TemplateCollectionEntry{{ID: "t1", BundleID: "core_defaults", TemplateID: "tmdb_popular_movies",
			ManagementKey: TemplateManagementKey("core_defaults", "tmdb_popular_movies", 3), LibraryID: 3, Libraries: []CollectionLib{{LibraryID: 3}}, Title: "Popular"}},
		ItemArtwork: []ItemArtworkEntry{}, Branding: []BrandingEntry{},
	})
	library := 3
	homeSections := mustJSON(HomeSectionsDocument{Sections: []HomeSectionEntry{
		// An untitled section, as bulk section creation stores one.
		{ID: "s1", Scope: "home", SectionType: "collection", Title: "", Featured: true, ItemLimit: 20, Config: json.RawMessage(`{"library_collection_id":"c1"}`), Enabled: true, WebOnly: true},
		{ID: "s2", Scope: "library", LibraryID: &library, SectionType: "collection", Title: "Picks", ItemLimit: 20, Config: json.RawMessage(`{"library_collection_id":"m1"}`), Enabled: true,
			Collection: &SectionCollectionEntry{CollectionEntry: CollectionEntry{ID: "m1", LibraryID: 3, Libraries: []CollectionLib{{LibraryID: 3}}, Title: "Picks", Items: []string{"m1"}}, ManagementKey: "s2"}},
	}})
	avatar := "profile-avatars/5/p1/original.webp"
	users := mustJSON(testUserDocument(5, "upload:"+avatar))
	digest := func(path string, data []byte) FileDigest {
		sum := sha256.Sum256(data)
		return FileDigest{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	}
	index := RestoreSetIndex{Format: restoreSetFormat, Version: restoreSetVersion, ServerID: "server-a", CreatedAt: time.Unix(0, 0).UTC(),
		Sections: []SectionIndex{
			{ID: SectionLibraries, Count: 1, Files: []FileDigest{digest("libraries.json", libraries)}, Blobs: []string{"library-posters/3.jpg"}},
			{ID: SectionPlugins, Count: 1, Files: []FileDigest{digest("plugins/tmdb/plugin.json", plugin), archiveDigest}, Blobs: []string{}},
			{ID: SectionSettings, Count: 1, Files: []FileDigest{digest("settings.json", settings)}, Blobs: []string{}},
			{ID: SectionUsers, Count: 1, Files: []FileDigest{digest("users/5.json", users)}, Blobs: []string{avatar}},
			{ID: SectionCollections, Count: 2, Files: []FileDigest{digest("collections.json", collections)}, Blobs: []string{original}},
			{ID: SectionHomeSections, Count: 2, Files: []FileDigest{digest(homeSectionsDocumentName, homeSections)}, Blobs: []string{}},
		}}
	entries := []testEntry{
		{name: "libraries.json", data: libraries},
		{name: "plugins/tmdb/plugin.json", data: plugin},
		{name: "plugins/tmdb/archive.bin", data: archive},
		{name: "settings.json", data: settings},
		{name: "users/5.json", data: users},
		{name: "collections.json", data: collections},
		{name: homeSectionsDocumentName, data: homeSections},
		{name: "blobs/" + avatar, data: []byte("avatar")},
		{name: "blobs/profile-avatars/5/p1/w256.webp", data: []byte("avatar small")},
		{name: "blobs/library-posters/3.jpg", data: []byte("poster")},
		{name: "blobs/" + original, data: []byte("original")},
		{name: "blobs/collection-images/c1/poster/w300.abcd.jpg", data: []byte("variant")},
	}
	return index, entries
}

// testUserDocument is one account with one profile that has an avatar and
// a rating.
func testUserDocument(id int, avatar string) UserDocument {
	return UserDocument{
		Account: AccountEntry{ID: id, Username: "alice", Email: "alice@example.test", Role: "user", Permissions: []string{}},
		Profiles: []ProfileEntry{{ID: "p1", Name: "Main", Avatar: avatar, IsPrimary: true, AllowedLibraryIDs: []int{},
			Data: map[string]json.RawMessage{"ratings": json.RawMessage(`{"ratings":[{"item":"m1","rating":4,"rated_at":"2026-01-01T00:00:00Z"}]}`)}}},
		Items: []ItemRef{{ContentID: "m1", Kind: "movie", Title: "One"}},
	}
}

// userCase rewrites users/5.json.
func userCase(fn func(*UserDocument)) func(*RestoreSetIndex, *[]testEntry) {
	return func(_ *RestoreSetIndex, entries *[]testEntry) {
		for i := range *entries {
			if (*entries)[i].name == "users/5.json" {
				doc := testUserDocument(5, "upload:profile-avatars/5/p1/original.webp")
				fn(&doc)
				(*entries)[i].data = mustJSON(doc)
			}
		}
	}
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

func buildTar(t *testing.T, index RestoreSetIndex, entries []testEntry) []byte {
	t.Helper()
	var all []testEntry
	for _, e := range entries {
		if e.beforeIndex {
			all = append(all, e)
		}
	}
	all = append(all, testEntry{name: restoreSetIndexName, data: mustJSON(index)})
	for _, e := range entries {
		if !e.beforeIndex {
			all = append(all, e)
		}
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range all {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		size := int64(len(e.data))
		if e.size != 0 {
			size = e.size
		}
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: size, Typeflag: typeflag, Linkname: e.linkname, Format: tar.FormatPAX}
		if typeflag != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tw.Write(e.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReadRestoreSetAcceptsAValidArchive(t *testing.T) {
	index, entries := testRestoreSet()
	brandingCase(BrandingEntry{Key: "branding.mark_ref", Ref: "0123abcd.webp", Blob: "branding/mark/0123abcd.webp"})(&index, &entries)
	resyncDigests(&index, entries)
	data := buildTar(t, index, entries)
	rs, err := ReadRestoreSet(context.Background(), bytes.NewReader(data), ReadOptions{ExtractDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Cleanup()
	sum := sha256.Sum256(data)
	if rs.Digest != hex.EncodeToString(sum[:]) {
		t.Fatal("digest is not the archive's SHA-256")
	}
	if len(rs.Plugins) != 1 || len(rs.Libraries.Libraries) != 1 || len(rs.Collections.Collections) != 1 || len(rs.Settings.Settings) != 1 || len(rs.Collections.Branding) != 1 ||
		len(rs.Users) != 1 || len(mustLoadUser(t, rs, rs.Users[0]).Profiles) != 1 || len(rs.Collections.TemplateCollections) != 1 || len(rs.HomeSections.Sections) != 2 ||
		rs.HomeSections.Sections[1].Collection == nil {
		t.Fatalf("sections not decoded: %+v", rs)
	}
	if family := rs.blobFamily("collection-images/c1/poster/original.abcd.jpg"); len(family) != 2 {
		t.Fatalf("artwork variants not kept with their original: %v", family)
	}
	if family := rs.blobFamily("profile-avatars/5/p1/original.webp"); len(family) != 2 {
		t.Fatalf("avatar variants not kept with their original: %v", family)
	}
	// A plugin archive is checked and never kept: nothing installs it.
	if kept, _ := filepath.Glob(filepath.Join(rs.dir, "archive-*")); len(kept) != 0 {
		t.Fatalf("plugin archive kept on disk: %v", kept)
	}
}

// TestRestoreExtractsOnlyTheSelectedItemsFiles: a restore writes to the
// temporary directory only the files its selected items need, and refuses
// to start when they would not fit there.
func TestRestoreExtractsOnlyTheSelectedItemsFiles(t *testing.T) {
	index, entries := testRestoreSet()
	data := buildTar(t, index, entries)
	planned, err := ReadRestoreSet(context.Background(), bytes.NewReader(data), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const poster, collectionImage = "library-posters/3.jpg", "collection-images/c1/poster/w300.abcd.jpg"
	for sel, want := range map[string][]string{
		SectionLibraries:   {poster},
		SectionCollections: {"collection-images/c1/poster/original.abcd.jpg", collectionImage},
	} {
		got := slices.Sorted(maps.Keys(selectedBlobs(planned, map[string][]string{sel: {"3", collectionItemID("c1")}})))
		if !slices.Equal(got, want) {
			t.Fatalf("selecting %s extracts %v, want %v", sel, got, want)
		}
	}

	keep := selectedBlobs(planned, map[string][]string{SectionLibraries: {"3"}})
	rs, err := ReadRestoreSet(context.Background(), bytes.NewReader(data), ReadOptions{ExtractDir: t.TempDir(), Extract: func(key string) bool { return keep[key] }})
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Cleanup()
	if f, _, err := rs.openBlob(poster); err != nil {
		t.Fatalf("selected file not extracted: %v", err)
	} else {
		_ = f.Close()
	}
	if _, _, err := rs.openBlob(collectionImage); !errors.Is(err, ErrNotFound) || !rs.HasBlob(collectionImage) {
		t.Fatalf("unselected file: %v, carried %v; want it read, not extracted", err, rs.HasBlob(collectionImage))
	}

	if err := checkTempSpace(t.TempDir(), planned, keep); err != nil {
		t.Fatalf("a small restore: %v", err)
	}
	planned.blobSizes[poster] = 1 << 60
	if err := checkTempSpace(t.TempDir(), planned, keep); !errors.Is(err, ErrNotEnoughTempSpace) {
		t.Fatalf("a restore larger than the free space = %v, want ErrNotEnoughTempSpace", err)
	}
}

// TestRestoreExtractsOnlyReferencedFiles: a selected collection extracts
// its poster and the variants stored beside it, nothing else under its
// directory; the reader refuses an original with more variants than a
// ladder has, so a set cannot pad one with filler.
func TestRestoreExtractsOnlyReferencedFiles(t *testing.T) {
	const original = "collection-images/c1/poster/original.r1.jpg"
	rs := &RestoreSet{blobs: map[string]string{}}
	for _, key := range []string{original, "collection-images/c1/poster/w300.r1.jpg", "collection-images/c1/poster/w500.r1.jpg",
		"collection-images/c1/poster/w300.r0.jpg", "collection-images/c1/filler/0.jpg", "collection-images/c1/backdrop/original.r1.jpg"} {
		rs.blobs[key] = ""
	}
	rs.Collections.Collections = []CollectionEntry{{ID: "c1", Poster: original}}
	got := slices.Sorted(maps.Keys(selectedBlobs(rs, map[string][]string{SectionCollections: {collectionItemID("c1")}})))
	want := []string{original, "collection-images/c1/poster/w300.r1.jpg", "collection-images/c1/poster/w500.r1.jpg"}
	if !slices.Equal(got, want) {
		t.Fatalf("selecting the collection extracts %v, want %v", got, want)
	}

	index, entries := testRestoreSet()
	for i := range maxLadderVariants + 1 {
		key := fmt.Sprintf("collection-images/c1/poster/w%d.abcd.jpg", 1000+i)
		entries = append(entries, testEntry{name: "blobs/" + key, data: []byte("x")})
	}
	if _, err := ReadRestoreSet(context.Background(), bytes.NewReader(buildTar(t, index, entries)), ReadOptions{}); err == nil ||
		!strings.Contains(err.Error(), "variants of") {
		t.Fatalf("an original with %d variants: %v, want it refused", maxLadderVariants+1, err)
	}
}

// TestReaderFindsVariantsInConstantTime: originals of many revisions in
// one directory, each with its variants, are read without scanning the
// directory's originals once per variant.
func TestReaderFindsVariantsInConstantTime(t *testing.T) {
	const originals = 10_000
	index, entries := testRestoreSet()
	for i := range index.Sections {
		if index.Sections[i].ID != SectionCollections {
			continue
		}
		for r := range originals {
			key := fmt.Sprintf("collection-images/c1/poster/original.r%d.jpg", r)
			index.Sections[i].Blobs = append(index.Sections[i].Blobs, key)
			entries = append(entries, testEntry{name: "blobs/" + key, data: []byte("o")},
				testEntry{name: fmt.Sprintf("blobs/collection-images/c1/poster/w1.r%d.jpg", r), data: []byte("v")},
				testEntry{name: fmt.Sprintf("blobs/collection-images/c1/poster/w2.r%d.jpg", r), data: []byte("v")})
		}
	}
	data := buildTar(t, index, entries)
	started := time.Now()
	rs, err := ReadRestoreSet(context.Background(), bytes.NewReader(data), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(rs.blobFamily("collection-images/c1/poster/original.r7.jpg")); got != 3 {
		t.Fatalf("family of one original = %d keys, want it and its two variants", got)
	}
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Fatalf("reading %d originals with their variants took %v", originals, elapsed)
	}
}

// TestArtworkFamiliesAreLinear: 20,000 keys in one directory, named like
// originals and legacy variants, are grouped into families in one pass,
// each kept to the ladder's size, so the index holds about one reference
// per key.
func TestArtworkFamiliesAreLinear(t *testing.T) {
	rs := &RestoreSet{blobs: map[string]string{}}
	const dir = "collection-images/c1/poster/"
	rs.blobs[dir+"original.jpg"] = ""
	for i := range 20_000 {
		for _, name := range []string{"original.%d", "original.r%d.jpg", "w%d.jpg"} {
			rs.blobs[dir+fmt.Sprintf(name, i)] = ""
		}
	}
	started := time.Now()
	x := rs.lookup()
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("indexing took %v", elapsed)
	}
	members := 0
	for _, keys := range x.variants {
		members += len(keys)
	}
	if got := len(x.variants[dir+"original.jpg"]); got != maxLadderVariants || members > len(rs.blobs) {
		t.Fatalf("legacy family of %d, %d members in all for %d keys; want it capped and about one per key", got, members, len(rs.blobs))
	}
}

// TestSelectedBlobsIsLinear: choosing the files of every collection of a set
// at the archive's entry limit classifies each file once. A scan per
// selected collection would take billions of prefix checks here and run
// past the test timeout.
func TestSelectedBlobsIsLinear(t *testing.T) {
	const collections, perCollection = 20_000, maxRestoreSetEntries / 20_000
	rs := &RestoreSet{blobs: make(map[string]string, maxRestoreSetEntries)}
	selection := make([]string, 0, collections)
	for i := range collections {
		id := strconv.Itoa(i)
		poster := collectionImagePrefix + id + "/poster/original.r1.jpg"
		rs.Collections.Collections = append(rs.Collections.Collections, CollectionEntry{ID: id, Poster: poster})
		selection = append(selection, collectionItemID(id))
		rs.blobs[poster] = ""
		for j := 1; j < perCollection; j++ {
			rs.blobs[fmt.Sprintf("%s%s/poster/w%d.r1.jpg", collectionImagePrefix, id, j)] = ""
		}
	}
	keep := selectedBlobs(rs, map[string][]string{SectionCollections: selection[:collections/2]})
	if len(keep) != collections/2*perCollection {
		t.Fatalf("kept %d files, want %d", len(keep), collections/2*perCollection)
	}
	if keep[collectionImagePrefix+strconv.Itoa(collections-1)+"/poster/w1.r1.jpg"] {
		t.Fatal("kept a file of a collection not selected")
	}
}

// TestReadRestoreSetBoundsWhatDocumentsDecodeTo: a short document of
// empty entries, here 100,000 empty collections or account profiles, is
// refused before it is decoded, without allocating what it would decode to.
func TestReadRestoreSetBoundsWhatDocumentsDecodeTo(t *testing.T) {
	empties := strings.Repeat(`{},`, 99_999) + `{}`
	for name, bomb := range map[string][]byte{
		collectionsDocumentName: []byte(`{"collections":[` + empties + `],"item_artwork":[],"branding":[]}`),
		"users/5.json":          []byte(`{"account":{"id":5},"profiles":[` + empties + `],"items":[]}`),
	} {
		t.Run(name, func(t *testing.T) {
			index, entries := testRestoreSet()
			for i := range entries {
				if entries[i].name == name {
					entries[i].data = bomb
				}
			}
			for i := range index.Sections {
				for j, f := range index.Sections[i].Files {
					if f.Path == name {
						sum := sha256.Sum256(bomb)
						index.Sections[i].Files[j] = FileDigest{Path: f.Path, Size: int64(len(bomb)), SHA256: hex.EncodeToString(sum[:])}
					}
				}
			}
			data := buildTar(t, index, entries)
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			started := time.Now()
			_, err := ReadRestoreSet(context.Background(), bytes.NewReader(data), ReadOptions{})
			elapsed := time.Since(started)
			runtime.ReadMemStats(&after)
			if !errors.Is(err, jsonsize.ErrTooLarge) {
				t.Fatalf("%d bytes of empty entries: %v, want jsonsize.ErrTooLarge", len(bomb), err)
			}
			// What reading the archive itself takes, a few times its size,
			// far below the tens of megabytes the entries would decode to.
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8<<20 {
				t.Fatalf("reading allocated %d bytes", allocated)
			}
			if elapsed > 5*time.Second {
				t.Fatalf("refusing took %v", elapsed)
			}
		})
	}
}

// setDocument replaces a document of a test restore set, with its digest
// and its section's item count.
func setDocument(index *RestoreSetIndex, entries []testEntry, name string, data []byte, count int) {
	for i := range entries {
		if entries[i].name == name {
			entries[i].data = data
		}
	}
	sum := sha256.Sum256(data)
	for i := range index.Sections {
		for j, f := range index.Sections[i].Files {
			if f.Path == name {
				index.Sections[i].Files[j] = FileDigest{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
				index.Sections[i].Count = count
			}
		}
	}
}

// TestUnusableEntriesAreCappedPerSection: a section of 500,000 malformed
// entries lists the first maxUnusableListed and counts the rest in one
// more, so a preview never builds an item for each.
func TestUnusableEntriesAreCappedPerSection(t *testing.T) {
	const bad = maxArtworkEntries
	index, entries := testRestoreSet()
	setDocument(&index, entries, homeSectionsDocumentName, mustJSON(HomeSectionsDocument{Sections: []HomeSectionEntry{}}), 0)
	for i := range index.Sections {
		if index.Sections[i].ID == SectionCollections {
			index.Sections[i].Blobs = []string{}
		}
	}
	entries = slices.DeleteFunc(entries, func(e testEntry) bool { return strings.HasPrefix(e.name, "blobs/collection-images/") })
	malformed := `{"content_id":"m","title":"` + strings.Repeat("t", 40) + `"}`
	doc := `{"collections":[],"item_artwork":[` + strings.Repeat(malformed+",", bad-1) + malformed + `],"branding":[]}`
	setDocument(&index, entries, collectionsDocumentName, []byte(doc), bad)
	rs, err := ReadRestoreSet(context.Background(), bytes.NewReader(buildTar(t, index, entries)), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	listed := 0
	for _, e := range rs.Unusable {
		if e.Section == SectionCollections {
			listed++
		}
	}
	last := rs.Unusable[len(rs.Unusable)-1]
	if listed != maxUnusableListed+1 || last.Label != fmt.Sprintf("%d more entries", bad-maxUnusableListed) {
		t.Fatalf("listed %d unusable entries, the last %+v; want %d and a count of the rest", listed, last, maxUnusableListed+1)
	}
}

// TestValidEntriesAreCappedPerSection: a set of 400,000 valid item
// artwork entries is refused when read, quickly, rather than previewed as
// 400,000 items.
func TestValidEntriesAreCappedPerSection(t *testing.T) {
	index, entries := testRestoreSet()
	setDocument(&index, entries, homeSectionsDocumentName, mustJSON(HomeSectionsDocument{Sections: []HomeSectionEntry{}}), 0)
	for i := range index.Sections {
		if index.Sections[i].ID == SectionCollections {
			index.Sections[i].Blobs = []string{}
		}
	}
	entries = slices.DeleteFunc(entries, func(e testEntry) bool { return strings.HasPrefix(e.name, "blobs/collection-images/") })
	const n = 400_000
	var doc strings.Builder
	doc.WriteString(`{"collections":[],"branding":[],"item_artwork":[`)
	for i := range n {
		if i > 0 {
			doc.WriteByte(',')
		}
		fmt.Fprintf(&doc, `{"target_type":"movie","content_id":"m%d","image_type":"poster","path":"","source":"","thumbhash":"","title":"","parent_content_id":""}`, i)
	}
	doc.WriteString(`]}`)
	setDocument(&index, entries, collectionsDocumentName, []byte(doc.String()), n)
	started := time.Now()
	_, err := ReadRestoreSet(context.Background(), bytes.NewReader(buildTar(t, index, entries)), ReadOptions{})
	if err == nil || !strings.Contains(err.Error(), "item artwork entries") {
		t.Fatalf("400,000 artwork entries: %v, want them refused", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("refusing took %v", elapsed)
	}
}

// TestEveryEntryHasOneSizeBound: every kind of entry a restore writes is
// left out, as unusable, when it holds more than an editor accepts in one
// request (maxRestoredEntryBytes); the rest of the set stays readable.
func TestEveryEntryHasOneSizeBound(t *testing.T) {
	big := strings.Repeat("x", maxRestoredEntryBytes)
	library := 3
	noCollections := func(d CollectionsDocument) CollectionsDocument {
		if d.Collections == nil {
			d.Collections = []CollectionEntry{}
		}
		if d.ItemArtwork == nil {
			d.ItemArtwork = []ItemArtworkEntry{}
		}
		d.Branding = []BrandingEntry{}
		return d
	}
	cases := map[string]struct {
		document string
		doc      func(entries []testEntry) any
	}{
		SectionSettings: {settingsDocumentName, func([]testEntry) any {
			return SettingsDocument{Settings: []SettingEntry{{Key: "branding.server_name", Value: big}}}
		}},
		SectionLibraries: {librariesDocumentName, func([]testEntry) any {
			return LibrariesDocument{Libraries: []LibraryEntry{{ID: 3, Name: big, Type: "movies", Paths: []string{"/media/movies"}, TrailerKinds: []string{}}}}
		}},
		SectionPlugins: {"plugins/tmdb/plugin.json", func(entries []testEntry) any {
			var doc PluginDocument
			for _, e := range entries {
				if e.name == "plugins/tmdb/plugin.json" {
					_ = json.Unmarshal(e.data, &doc)
				}
			}
			doc.Configs = []PluginConfigEntry{{Key: "global", Value: json.RawMessage(`"` + big + `"`)}}
			return doc
		}},
		KindCollection: {collectionsDocumentName, func([]testEntry) any {
			return noCollections(CollectionsDocument{Collections: []CollectionEntry{
				{ID: "c1", LibraryID: 3, Libraries: []CollectionLib{{LibraryID: 3}}, Title: "Favorites", Description: big}}})
		}},
		KindTemplateCollection: {collectionsDocumentName, func([]testEntry) any {
			return noCollections(CollectionsDocument{TemplateCollections: []TemplateCollectionEntry{{ID: "t1", BundleID: "core_defaults",
				TemplateID: "tmdb_popular_movies", ManagementKey: TemplateManagementKey("core_defaults", "tmdb_popular_movies", 3), LibraryID: 3,
				Title: "Popular", Description: big}}})
		}},
		KindItemArtwork: {collectionsDocumentName, func([]testEntry) any {
			return noCollections(CollectionsDocument{ItemArtwork: []ItemArtworkEntry{{TargetType: "movie", ContentID: "m1", ImageType: artworkkey.ImagePoster, Thumbhash: big}}})
		}},
		SectionHomeSections: {homeSectionsDocumentName, func([]testEntry) any {
			return HomeSectionsDocument{Sections: []HomeSectionEntry{{ID: "s2", Scope: "library", LibraryID: &library, SectionType: "recently_added",
				Title: "Picks", Config: json.RawMessage(`{"x":"` + big + `"}`)}}}
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			index, entries := testRestoreSet()
			if c.document == collectionsDocumentName {
				// The page sections name the backup's collections, and its
				// images are theirs.
				setDocument(&index, entries, homeSectionsDocumentName, mustJSON(HomeSectionsDocument{Sections: []HomeSectionEntry{}}), 0)
				for i := range index.Sections {
					if index.Sections[i].ID == SectionCollections {
						index.Sections[i].Blobs = []string{}
					}
				}
				entries = slices.DeleteFunc(entries, func(e testEntry) bool { return strings.HasPrefix(e.name, "blobs/collection-images/") })
			}
			setDocument(&index, entries, c.document, mustJSON(c.doc(entries)), 1)
			rs, err := ReadRestoreSet(context.Background(), bytes.NewReader(buildTar(t, index, entries)), ReadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(rs.Unusable, func(e UnusableEntry) bool { return e.Message == msgEntryTooLarge }) {
				t.Fatalf("an oversized entry was kept; unusable: %+v", rs.Unusable)
			}
		})
	}
}

// TestReadRestoreSetChecksBeforeDecoding: an index that does not match the
// manifest's digest is refused before it is decoded, a failed archive check
// stops the read before any document is decoded, and documents larger than
// a restore set may hold are refused from the index alone.
func TestReadRestoreSetChecksBeforeDecoding(t *testing.T) {
	ctx := context.Background()
	index, entries := testRestoreSet()
	data := buildTar(t, index, entries)
	sum := sha256.Sum256(mustJSON(index))
	if _, err := ReadRestoreSet(ctx, bytes.NewReader(data), ReadOptions{IndexSHA256: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatalf("matching index digest: %v", err)
	}
	if _, err := ReadRestoreSet(ctx, bytes.NewReader(data), ReadOptions{IndexSHA256: strings.Repeat("0", 64)}); err == nil || !strings.Contains(err.Error(), "does not match the digest its manifest records") {
		t.Fatalf("another index digest = %v", err)
	}

	// A document that would fail to decode: the archive check runs first.
	bIndex, bEntries := testRestoreSet()
	for i := range bEntries {
		if bEntries[i].name == settingsDocumentName {
			bEntries[i].data = []byte("{not json")
		}
	}
	resyncDigests(&bIndex, bEntries)
	refused := errors.New("the archive is not the one its manifest records")
	if _, err := ReadRestoreSet(ctx, bytes.NewReader(buildTar(t, bIndex, bEntries)), ReadOptions{Verify: func() error { return refused }}); !errors.Is(err, refused) {
		t.Fatalf("read with a failing archive check = %v, want the check's error", err)
	}

	for name, change := range map[string]func(*RestoreSetIndex){
		"documents over the budget": func(index *RestoreSetIndex) {
			for i := range index.Sections {
				for j := range index.Sections[i].Files {
					if path.Base(index.Sections[i].Files[j].Path) != pluginArchiveName {
						index.Sections[i].Files[j].Size = maxRestoreSetDocument
					}
				}
			}
		},
		"an oversized plugin document": func(index *RestoreSetIndex) {
			for i := range index.Sections {
				for j := range index.Sections[i].Files {
					if path.Base(index.Sections[i].Files[j].Path) == pluginDocumentName {
						index.Sections[i].Files[j].Size = maxPluginDocument + 1
					}
				}
			}
		},
	} {
		index, entries := testRestoreSet()
		change(&index)
		if _, err := ReadRestoreSet(ctx, bytes.NewReader(buildTar(t, index, entries)), ReadOptions{}); err == nil || !strings.Contains(err.Error(), restoreSetIndexName) {
			t.Fatalf("%s: %v, want the index refused", name, err)
		}
	}
}

// TestReadRestoreSetRefusesHostileArchives: restore sets are untrusted input,
// uploaded or read from a destination something else can write. Each case
// changes one thing about a valid archive.
func TestReadRestoreSetRefusesHostileArchives(t *testing.T) {
	replace := func(name string, fn func(*testEntry)) mutate {
		return func(_ *RestoreSetIndex, entries *[]testEntry) {
			for i := range *entries {
				if (*entries)[i].name == name {
					fn(&(*entries)[i])
				}
			}
		}
	}
	add := func(e testEntry) mutate {
		return func(_ *RestoreSetIndex, entries *[]testEntry) { *entries = append(*entries, e) }
	}
	section := func(id string, fn func(*SectionIndex)) mutate {
		return func(index *RestoreSetIndex, _ *[]testEntry) {
			for i := range index.Sections {
				if index.Sections[i].ID == id {
					fn(&index.Sections[i])
				}
			}
		}
	}
	deep := strings.Repeat("[", maxRestoreSetJSONDepth+1) + strings.Repeat("]", maxRestoreSetJSONDepth+1)
	cases := map[string]mutate{
		"symlink":        add(testEntry{name: "blobs/library-posters/x.jpg", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}),
		"absolute path":  add(testEntry{name: "/settings.json", data: []byte("{}")}),
		"parent segment": add(testEntry{name: "blobs/../settings.json", data: []byte("{}")}),
		"duplicate path": func(_ *RestoreSetIndex, e *[]testEntry) {
			for _, entry := range *e {
				if entry.name == "settings.json" {
					*e = append(*e, entry)
					return
				}
			}
		},
		"unlisted file":     add(testEntry{name: "users/a.json", data: []byte("{}")}),
		"unreferenced blob": add(testEntry{name: "blobs/library-posters/9.jpg", data: []byte("x")}),
		"index not first":   replace("settings.json", func(e *testEntry) { e.beforeIndex = true }),
		"document changed": replace("settings.json", func(e *testEntry) {
			e.data = bytes.Replace(e.data, []byte("Home"), []byte("Hack"), 1)
		}),
		"archive changed":              replace("plugins/tmdb/archive.bin", func(e *testEntry) { e.data = []byte("PK-not-really-a-zip!") }),
		"listed file missing":          func(_ *RestoreSetIndex, e *[]testEntry) { *e = (*e)[1:] },
		"unknown field":                replace("settings.json", func(e *testEntry) { e.data = []byte(`{"settings":[],"extra":1}`) }),
		"too deep":                     replace("settings.json", func(e *testEntry) { e.data = []byte(deep) }),
		"oversized document":           replace("settings.json", func(e *testEntry) { e.size = maxRestoreSetDocument + 1; e.data = make([]byte, maxRestoreSetDocument+1) }),
		"count disagrees":              section(SectionSettings, func(s *SectionIndex) { s.Count = 2 }),
		"unknown section":              section(SectionSettings, func(s *SectionIndex) { s.ID = "widgets" }),
		"file under the wrong section": section(SectionSettings, func(s *SectionIndex) { s.Files[0].Path = "libraries.json" }),
		"blob key outside prefixes":    section(SectionLibraries, func(s *SectionIndex) { s.Blobs = append(s.Blobs, "profile-avatars/a.jpg") }),
		"server id with a slash":       func(i *RestoreSetIndex, _ *[]testEntry) { i.ServerID = "../x" },
		"not a restore set":            func(i *RestoreSetIndex, _ *[]testEntry) { i.Format = "something-else" },
		"document points at an unlisted blob": replace("libraries.json", func(e *testEntry) {
			e.data = mustJSON(LibrariesDocument{Libraries: []LibraryEntry{{ID: 3, Name: "a", Type: "movies", Paths: []string{"/a"}, Poster: "library-posters/other.jpg"}}})
		}),
		"plugin document names another plugin": replace("plugins/tmdb/plugin.json", func(e *testEntry) {
			var doc PluginDocument
			_ = json.Unmarshal(e.data, &doc)
			doc.PluginID = "other"
			e.data = mustJSON(doc)
		}),
	}
	refuseAll(t, cases)
}

// refuseAll applies each case to a valid restore set and requires the
// reader to refuse the result.
func refuseAll(t *testing.T, cases map[string]func(*RestoreSetIndex, *[]testEntry)) {
	t.Helper()
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			index, entries := testRestoreSet()
			fn(&index, &entries)
			// Keep document digests in step with edits made to test something
			// other than the digest check itself.
			if name != "document changed" && name != "archive changed" {
				resyncDigests(&index, entries)
			}
			data := buildTar(t, index, entries)
			if _, err := ReadRestoreSet(context.Background(), bytes.NewReader(data), ReadOptions{}); err == nil {
				t.Fatal("ReadRestoreSet accepted it")
			} else {
				t.Log(err)
			}
		})
	}
}

// TestReadRestoreSetRefusesHostileUserDocuments: the users section carries
// password and PIN hashes and access fields, so its documents get the same
// checks, and a version 1 set may not hold it at all.
func TestReadRestoreSetRefusesHostileUserDocuments(t *testing.T) {
	replace := func(name string, fn func(*testEntry)) func(*RestoreSetIndex, *[]testEntry) {
		return func(_ *RestoreSetIndex, entries *[]testEntry) {
			for i := range *entries {
				if (*entries)[i].name == name {
					fn(&(*entries)[i])
				}
			}
		}
	}
	refuseAll(t, map[string]func(*RestoreSetIndex, *[]testEntry){
		"users in a version 1 set": func(i *RestoreSetIndex, _ *[]testEntry) { i.Version = 1 },
		"an owner flag in a user document": replace("users/5.json", func(e *testEntry) {
			e.data = bytes.Replace(e.data, []byte(`"role":"user"`), []byte(`"role":"user","is_owner":true`), 1)
		}),
		"avatar not listed": userCase(func(d *UserDocument) { d.Profiles[0].Avatar = "upload:profile-avatars/5/p9/original.webp" }),
	})
	// A document whose own values no restore could write leaves that
	// account out, listed, and the rest of the set readable.
	for name, edit := range map[string]func(*RestoreSetIndex, *[]testEntry){
		"user document of another account": replace("users/5.json", func(e *testEntry) {
			e.data = mustJSON(testUserDocument(6, "upload:profile-avatars/5/p1/original.webp"))
		}),
		"an unknown role":            userCase(func(d *UserDocument) { d.Account.Role = "owner" }),
		"unknown profile data":       userCase(func(d *UserDocument) { d.Profiles[0].Data["passwords"] = json.RawMessage(`{}`) }),
		"repeated profile id":        userCase(func(d *UserDocument) { d.Profiles = append(d.Profiles, d.Profiles[0]) }),
		"avatar outside the avatars": userCase(func(d *UserDocument) { d.Profiles[0].Avatar = "upload:library-posters/3.jpg" }),
	} {
		t.Run(name, func(t *testing.T) {
			index, entries := testRestoreSet()
			edit(&index, &entries)
			resyncDigests(&index, entries)
			rs, err := ReadRestoreSet(context.Background(), bytes.NewReader(buildTar(t, index, entries)), ReadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(rs.Users) != 0 || len(rs.Unusable) != 1 || rs.Unusable[0].Section != SectionUsers {
				t.Fatalf("users %d, unusable %+v", len(rs.Users), rs.Unusable)
			}
		})
	}
}

// brandingCase puts one branding entry, its blob listed and present, into an
// otherwise valid restore set.
func brandingCase(b BrandingEntry) func(*RestoreSetIndex, *[]testEntry) {
	return func(index *RestoreSetIndex, entries *[]testEntry) {
		for i := range *entries {
			e := &(*entries)[i]
			if e.name != "collections.json" {
				continue
			}
			var doc CollectionsDocument
			_ = json.Unmarshal(e.data, &doc)
			doc.Branding = append(doc.Branding, b)
			e.data = mustJSON(doc)
		}
		for i := range index.Sections {
			if index.Sections[i].ID == SectionCollections {
				index.Sections[i].Count++
				index.Sections[i].Blobs = append(index.Sections[i].Blobs, b.Blob)
			}
		}
		*entries = append(*entries, testEntry{name: "blobs/" + b.Blob, data: []byte("asset")})
	}
}

// resyncDigests rewrites the index digests of documents a case replaced.
func resyncDigests(index *RestoreSetIndex, entries []testEntry) {
	byName := map[string][]byte{}
	for _, e := range entries {
		byName[e.name] = e.data
	}
	for i := range index.Sections {
		for j, f := range index.Sections[i].Files {
			if data, ok := byName[f.Path]; ok && !strings.HasSuffix(f.Path, pluginArchiveName) {
				sum := sha256.Sum256(data)
				index.Sections[i].Files[j].Size, index.Sections[i].Files[j].SHA256 = int64(len(data)), hex.EncodeToString(sum[:])
			}
		}
	}
}

func TestReadRestoreSetRefusesANewerFormat(t *testing.T) {
	index, entries := testRestoreSet()
	index.Version = restoreSetVersion + 1
	// A newer format may have sections and files this server has never
	// heard of; the version decides the message.
	entries = append(entries, testEntry{name: "users/a.json", data: []byte("{}")})
	_, err := ReadRestoreSet(context.Background(), bytes.NewReader(buildTar(t, index, entries)), ReadOptions{})
	if !errors.Is(err, ErrRestoreSetTooNew) {
		t.Fatalf("err = %v, want ErrRestoreSetTooNew", err)
	}
}

// TestReadRestoreSetReadsEveryFormatVersion: this server reads sets of
// format 1 to 4. An older set holds none of what a later version added, and
// one that claims an older version while holding it is refused, as an older
// server would refuse it.
func TestReadRestoreSetReadsEveryFormatVersion(t *testing.T) {
	dropSection := func(index *RestoreSetIndex, entries *[]testEntry, id string) {
		spec, _ := sectionByID(id)
		var sections []SectionIndex
		for _, si := range index.Sections {
			if si.ID != id {
				sections = append(sections, si)
			}
		}
		index.Sections = sections
		var kept []testEntry
		for _, e := range *entries {
			if _, owned := spec.ownsFile(e.name); !owned && !strings.HasPrefix(e.name, "blobs/profile-avatars/") {
				kept = append(kept, e)
			}
		}
		*entries = kept
	}
	dropTemplates := func(index *RestoreSetIndex, entries *[]testEntry) {
		for i := range *entries {
			if (*entries)[i].name == collectionsDocumentName {
				var doc CollectionsDocument
				_ = json.Unmarshal((*entries)[i].data, &doc)
				doc.TemplateCollections = nil
				(*entries)[i].data = mustJSON(doc)
			}
		}
		for i := range index.Sections {
			if index.Sections[i].ID == SectionCollections {
				index.Sections[i].Count = 1
			}
		}
		resyncDigests(index, *entries)
	}
	webOnly := func(on bool) func(*RestoreSetIndex, *[]testEntry) {
		return func(index *RestoreSetIndex, entries *[]testEntry) {
			for _, e := range *entries {
				if e.name == homeSectionsDocumentName {
					var doc HomeSectionsDocument
					_ = json.Unmarshal(e.data, &doc)
					for i := range doc.Sections {
						doc.Sections[i].WebOnly = on
					}
					setDocument(index, *entries, homeSectionsDocumentName, mustJSON(doc), len(doc.Sections))
				}
			}
		}
	}
	read := func(index RestoreSetIndex, entries []testEntry) error {
		_, err := ReadRestoreSet(context.Background(), bytes.NewReader(buildTar(t, index, entries)), ReadOptions{})
		return err
	}
	for _, c := range []struct {
		name    string
		version int
		edit    func(*RestoreSetIndex, *[]testEntry)
		ok      bool
	}{
		{"version 2 without home sections or template collections", 2, func(i *RestoreSetIndex, e *[]testEntry) {
			dropSection(i, e, SectionHomeSections)
			dropTemplates(i, e)
		}, true},
		{"version 1 without users either", 1, func(i *RestoreSetIndex, e *[]testEntry) {
			dropSection(i, e, SectionHomeSections)
			dropSection(i, e, SectionUsers)
			dropTemplates(i, e)
		}, true},
		{"version 3 without web-only sections", 3, webOnly(false), true},
		{"version 3 with a web-only section", 3, webOnly(true), false},
		{"version 2 with home sections", 2, func(i *RestoreSetIndex, e *[]testEntry) { dropTemplates(i, e) }, false},
		{"version 2 with template collections", 2, func(i *RestoreSetIndex, e *[]testEntry) { dropSection(i, e, SectionHomeSections) }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			index, entries := testRestoreSet()
			index.Version = c.version
			c.edit(&index, &entries)
			if err := read(index, entries); (err == nil) != c.ok {
				t.Fatalf("read = %v, want ok %v", err, c.ok)
			}
		})
	}
}

func TestReadRestoreSetReportsMissingBlobs(t *testing.T) {
	index, entries := testRestoreSet()
	var kept []testEntry
	for _, e := range entries {
		if e.name != "blobs/library-posters/3.jpg" {
			kept = append(kept, e)
		}
	}
	rs, err := ReadRestoreSet(context.Background(), bytes.NewReader(buildTar(t, index, kept)), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.MissingBlobs) != 1 || rs.MissingBlobs[0] != "library-posters/3.jpg" {
		t.Fatalf("missing = %v", rs.MissingBlobs)
	}
}

// TestPendingDecodesFollowTheComparison: documents decoded ahead are handed
// over in the comparison's order, those of skipped profiles are dropped
// without stalling a single worker, a passed or undecodable-ahead document
// falls back to Pending, and a decode error reaches the caller.
func TestPendingDecodesFollowTheComparison(t *testing.T) {
	good := json.RawMessage(`{"progress":[{"item":"mv-1","updated_at":"2025-01-01T00:00:00Z"}]}`)
	bad := json.RawMessage(`{"progress":[{"item":"mv-1"}]}`)
	profile := func(id string, history json.RawMessage) ProfileEntry {
		return ProfileEntry{ID: id, Data: map[string]json.RawMessage{userdata.WatchHistory: history, userdata.Ratings: json.RawMessage(`[]`)}}
	}
	doc := &UserDocument{Profiles: []ProfileEntry{profile("a", good), profile("b", bad), profile("c", good), profile("d", bad)}}
	d := startPendingDecodes(doc, 1)
	defer d.stop()
	if r, ok := d.take(&doc.Profiles[2], userdata.WatchHistory); !ok || r.err != nil || r.compare == nil {
		t.Fatalf("c: compare %v, err %v, ok %v; want its decoded document", r.compare != nil, r.err, ok)
	}
	if _, ok := d.take(&doc.Profiles[0], userdata.WatchHistory); ok {
		t.Fatal("a was passed; want a fallback to Pending")
	}
	if _, ok := d.take(&doc.Profiles[3], userdata.Ratings); ok {
		t.Fatal("ratings are not decoded ahead; want a fallback to Pending")
	}
	if r, ok := d.take(&doc.Profiles[3], userdata.WatchHistory); !ok || r.err == nil {
		t.Fatalf("d: err %v, ok %v; want its decode error", r.err, ok)
	}
}

// TestReadRestoreSetLeavesOutUnusableEntries: an entry holding a value no
// restore could write is left out and listed, and the rest of the set stays
// readable; nothing it names is written.
func TestReadRestoreSetLeavesOutUnusableEntries(t *testing.T) {
	editDocument := func(name string, into any, edit func()) func(*RestoreSetIndex, *[]testEntry) {
		return func(_ *RestoreSetIndex, entries *[]testEntry) {
			for j := range *entries {
				if (*entries)[j].name == name {
					_ = json.Unmarshal((*entries)[j].data, into)
					edit()
					(*entries)[j].data = mustJSON(into)
				}
			}
		}
	}
	var libraries LibrariesDocument
	var sections HomeSectionsDocument
	var collections CollectionsDocument
	huge := strings.Repeat("x", maxRestoredEntryBytes)
	for name, c := range map[string]struct {
		edit    func(*RestoreSetIndex, *[]testEntry)
		section string
		gone    func(*RestoreSet) bool
	}{
		"relative library path": {editDocument("libraries.json", &libraries, func() { libraries.Libraries[0].Paths = []string{"media/../etc"} }),
			SectionLibraries, func(rs *RestoreSet) bool { return len(rs.Libraries.Libraries) == 0 }},
		"branding entry for another setting": {brandingCase(BrandingEntry{Key: "setup.completed", Ref: "evil.example", Blob: "branding/mark/evil.example"}),
			SectionCollections, func(rs *RestoreSet) bool { return len(rs.Collections.Branding) == 0 }},
		"branding ref with a path": {brandingCase(BrandingEntry{Key: "branding.mark_ref", Ref: "../x.webp", Blob: "branding/x.webp"}),
			SectionCollections, func(rs *RestoreSet) bool { return len(rs.Collections.Branding) == 0 }},
		"a section carrying a collection its configuration does not name": {editDocument(homeSectionsDocumentName, &sections, func() {
			sections.Sections[1].Config = json.RawMessage(`{"library_collection_id":"c1"}`)
		}), SectionHomeSections, func(rs *RestoreSet) bool { return len(rs.HomeSections.Sections) == 1 }},
		"a collection listing an item twice": {editDocument(collectionsDocumentName, &collections, func() {
			collections.Collections[0].Items = []string{"m1", "m1"}
		}), SectionCollections, func(rs *RestoreSet) bool { return len(rs.Collections.Collections) == 0 }},
		// No editor writes more for one collection in one request.
		"a collection description larger than a request": {editDocument(collectionsDocumentName, &collections, func() { collections.Collections[0].Description = huge }),
			SectionCollections, func(rs *RestoreSet) bool { return len(rs.Collections.Collections) == 0 }},
		"a section configuration larger than a request": {editDocument(homeSectionsDocumentName, &sections, func() {
			sections.Sections[0].Config = json.RawMessage(`{"library_collection_id":"c1","pad":"` + huge + `"}`)
		}), SectionHomeSections, func(rs *RestoreSet) bool { return len(rs.HomeSections.Sections) == 1 }},
	} {
		t.Run(name, func(t *testing.T) {
			index, entries := testRestoreSet()
			c.edit(&index, &entries)
			resyncDigests(&index, entries)
			rs, err := ReadRestoreSet(context.Background(), bytes.NewReader(buildTar(t, index, entries)), ReadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if len(rs.Unusable) != 1 || rs.Unusable[0].Section != c.section || !c.gone(rs) {
				t.Fatalf("unusable = %+v", rs.Unusable)
			}
		})
	}
}

// A PAX sparse entry would read as a regular file of its logical size, with
// holes filled from nothing; the reader refuses it before reading on.
func TestReadRestoreSetRefusesSparseEntries(t *testing.T) {
	index, entries := testRestoreSet()
	const name = "blobs/library-posters/3.jpg"
	sparseMap := []byte("1\n0\n1\n")
	for i := range entries {
		if entries[i].name == name {
			entries[i].data = append(append(sparseMap, make([]byte, 512-len(sparseMap))...), 'x')
		}
	}
	data := withPAXRecords(t, buildTar(t, index, entries), name, map[string]string{"GNU.sparse.major": "1", "GNU.sparse.minor": "0",
		"GNU.sparse.name": name, "GNU.sparse.realsize": strconv.Itoa(1 << 30)})
	_, err := ReadRestoreSet(context.Background(), bytes.NewReader(data), ReadOptions{ExtractDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "sparse") {
		t.Fatalf("read = %v, want a refused sparse file", err)
	}
}

// withPAXRecords puts a PAX extended header with records ahead of the entry
// named name: archive/tar's writer will not write GNU sparse records.
func withPAXRecords(t *testing.T, archive []byte, name string, records map[string]string) []byte {
	t.Helper()
	var body []byte
	for _, key := range slices.Sorted(maps.Keys(records)) {
		rec := " " + key + "=" + records[key] + "\n"
		n := len(rec) + 1
		for len(strconv.Itoa(n))+len(rec) != n {
			n = len(strconv.Itoa(n)) + len(rec)
		}
		body = append(body, strconv.Itoa(n)+rec...)
	}
	header := make([]byte, 512)
	copy(header, "PaxHeaders/x")
	copy(header[100:], "0000644\x00")
	copy(header[124:], fmt.Sprintf("%011o\x00", len(body)))
	copy(header[136:], "00000000000\x00")
	header[156] = 'x'
	copy(header[257:], "ustar\x0000")
	copy(header[148:], "        ")
	sum := 0
	for _, b := range header {
		sum += int(b)
	}
	copy(header[148:], fmt.Sprintf("%06o\x00 ", sum))
	block := slices.Concat(header, body)
	block = append(block, make([]byte, (512-len(body)%512)%512)...)
	for off := 0; off+512 <= len(archive); off += 512 {
		if bytes.Equal(bytes.TrimRight(archive[off:off+100], "\x00"), []byte(name)) && string(archive[off+257:off+262]) == "ustar" {
			return append(append(append([]byte{}, archive[:off]...), block...), archive[off:]...)
		}
	}
	t.Fatalf("no header for %s", name)
	return nil
}

// An account left out of a restore set is recorded with fields the reader
// accepts, however long its name or error: one long username must not make
// the whole index unreadable.
func TestOmittedAccountsStayReadable(t *testing.T) {
	long := strings.Repeat("é", 400) // 800 bytes, over maxAccountField
	cases := []struct {
		name    string
		account AccountEntry
		err     error
	}{
		{"long username", AccountEntry{ID: 1, Username: long}, errors.New("boom")},
		{"long email", AccountEntry{ID: 2, Email: long + "@example.com"}, errors.New("boom")},
		{"long error", AccountEntry{ID: 3, Username: "a"}, errors.New(strings.Repeat("x", 3*maxOmittedText))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b restoreSetBuild
			b.omitFailed(sectionSpec{id: SectionUsers}, tc.account, &exportError{part: "ratings", err: fmt.Errorf("reading ratings: %w", tc.err)})
			if err := validateOmitted(b.index.Omitted); err != nil {
				t.Fatalf("omitted %+v: %v", b.index.Omitted, err)
			}
			if !utf8.ValidString(b.index.Omitted[0].Label) || b.index.Omitted[0].Label == "" {
				t.Fatalf("label %q is not valid text", b.index.Omitted[0].Label)
			}
		})
	}
}

// TestReadRestoreSetRefusesExpandingHeaders: a restore set whose first
// header is an old-GNU sparse entry with 50,000 extension blocks is
// refused at that header, without expanding them.
func TestReadRestoreSetRefusesExpandingHeaders(t *testing.T) {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	_ = tw.WriteHeader(&tar.Header{Name: restoreSetIndexName, Mode: 0o644, Size: 0, Typeflag: tar.TypeReg})
	raw := b.Bytes()[:512]
	raw[156] = 'S'
	copy(raw[148:156], "        ")
	sum := 0
	for _, c := range raw {
		sum += int(c)
	}
	copy(raw[148:156], fmt.Sprintf("%06o\x00 ", sum))
	archive := append(slices.Clone(raw), bytes.Repeat(append(make([]byte, 511), 1), 50_000)...)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := ReadRestoreSet(context.Background(), bytes.NewReader(archive), ReadOptions{})
	runtime.ReadMemStats(&after)
	if !errors.Is(err, errTarShape) {
		t.Fatalf("a sparse header: %v, want errTarShape", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 4<<20 {
		t.Fatalf("refusing allocated %d bytes", allocated)
	}
}

func mustLoadUser(t *testing.T, rs *RestoreSet, ref UserRef) *UserDocument {
	t.Helper()
	doc, err := rs.loadUser(ref)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestAccountDocumentsStayOnDisk: an account's document is written to the
// set's directory and loaded on demand, never held or charged to the set's
// memory, so a server's accounts together may exceed maxRestoreSetMetadata;
// a written document that changed since the read is refused.
func TestAccountDocumentsStayOnDisk(t *testing.T) {
	index, entries := testRestoreSet()
	doc := testUserDocument(5, "upload:profile-avatars/5/p1/original.webp")
	title := strings.Repeat("t", 1000)
	for i := range 4000 {
		doc.Items = append(doc.Items, ItemRef{ContentID: fmt.Sprintf("mv-%d", i), Kind: "movie", Title: title})
	}
	data := mustJSON(doc)
	setDocument(&index, entries, "users/5.json", data, 1)
	rs, err := ReadRestoreSet(context.Background(), bytes.NewReader(buildTar(t, index, entries)), ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Cleanup()
	if rs.metadataBytes >= int64(len(data)) {
		t.Fatalf("charged %d bytes for a set whose account document alone is %d", rs.metadataBytes, len(data))
	}
	if loaded := mustLoadUser(t, rs, rs.Users[0]); len(loaded.Items) != len(doc.Items) {
		t.Fatalf("loaded %d items, want %d", len(loaded.Items), len(doc.Items))
	}
	if err := os.WriteFile(rs.Users[0].file, mustJSON(testUserDocument(5, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.loadUser(rs.Users[0]); err == nil {
		t.Fatal("a written account document that changed was loaded")
	}
}

// TestOrphanedSetDirs: the startup sweep removes a set directory whose owner
// is gone and keeps one a live set holds or one too new to be locked yet.
func TestOrphanedSetDirs(t *testing.T) {
	parent := t.TempDir()
	var live, gone RestoreSet
	if err := live.makeDir(parent); err != nil {
		t.Fatal(err)
	}
	defer live.Cleanup()
	if err := gone.makeDir(parent); err != nil {
		t.Fatal(err)
	}
	_ = gone.owner.Close() // as when its process exits
	young := filepath.Join(parent, setDirPrefix+"young")
	if err := os.Mkdir(young, 0o700); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]bool{live.dir: false, gone.dir: true, young: false} {
		if got := orphanedSetDir(dir); got != want {
			t.Errorf("orphanedSetDir(%s) = %v, want %v", filepath.Base(dir), got, want)
		}
	}
}
