package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
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
		{ID: "s1", Scope: "home", SectionType: "collection", Title: "Favorites", ItemLimit: 20, Config: json.RawMessage(`{"library_collection_id":"c1"}`), Enabled: true},
		{ID: "s2", Scope: "library", LibraryID: &library, SectionType: "collection", Title: "Picks", ItemLimit: 20, Config: json.RawMessage(`{"library_collection_id":"m1"}`), Enabled: true,
			Collection: &SectionCollectionEntry{CollectionEntry: CollectionEntry{ID: "m1", LibraryID: 3, Libraries: []CollectionLib{{LibraryID: 3}}, Title: "Picks", Items: []string{"m1"}}, ManagementKey: "s2"}},
	}})
	digest := func(path string, data []byte) FileDigest {
		sum := sha256.Sum256(data)
		return FileDigest{Path: path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	}
	index := RestoreSetIndex{Format: restoreSetFormat, Version: restoreSetVersion, ServerID: "server-a", CreatedAt: time.Unix(0, 0).UTC(),
		Sections: []SectionIndex{
			{ID: SectionLibraries, Count: 1, Files: []FileDigest{digest("libraries.json", libraries)}, Blobs: []string{"library-posters/3.jpg"}},
			{ID: SectionPlugins, Count: 1, Files: []FileDigest{digest("plugins/tmdb/plugin.json", plugin), archiveDigest}, Blobs: []string{}},
			{ID: SectionSettings, Count: 1, Files: []FileDigest{digest("settings.json", settings)}, Blobs: []string{}},
			{ID: SectionCollections, Count: 2, Files: []FileDigest{digest("collections.json", collections)}, Blobs: []string{original}},
			{ID: SectionHomeSections, Count: 2, Files: []FileDigest{digest(homeSectionsDocumentName, homeSections)}, Blobs: []string{}},
		}}
	entries := []testEntry{
		{name: "libraries.json", data: libraries},
		{name: "plugins/tmdb/plugin.json", data: plugin},
		{name: "plugins/tmdb/archive.bin", data: archive},
		{name: "settings.json", data: settings},
		{name: "collections.json", data: collections},
		{name: homeSectionsDocumentName, data: homeSections},
		{name: "blobs/library-posters/3.jpg", data: []byte("poster")},
		{name: "blobs/" + original, data: []byte("original")},
		{name: "blobs/collection-images/c1/poster/w300.abcd.jpg", data: []byte("variant")},
	}
	return index, entries
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
		len(rs.Collections.TemplateCollections) != 1 || len(rs.HomeSections.Sections) != 2 ||
		rs.HomeSections.Sections[1].Collection == nil {
		t.Fatalf("sections not decoded: %+v", rs)
	}
	if family := rs.blobFamily("collection-images/c1/poster/original.abcd.jpg"); len(family) != 2 {
		t.Fatalf("artwork variants not kept with their original: %v", family)
	}
	if archive, err := rs.pluginArchive("tmdb"); err != nil || string(archive) != "PK-not-really-a-zip" {
		t.Fatalf("plugin archive not extracted: %q %v", archive, err)
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
		"relative library path": replace("libraries.json", func(e *testEntry) {
			e.data = mustJSON(LibrariesDocument{Libraries: []LibraryEntry{{ID: 3, Name: "a", Type: "movies", Paths: []string{"media/../etc"}}}})
		}),
		"document points at an unlisted blob": replace("libraries.json", func(e *testEntry) {
			e.data = mustJSON(LibrariesDocument{Libraries: []LibraryEntry{{ID: 3, Name: "a", Type: "movies", Paths: []string{"/a"}, Poster: "library-posters/other.jpg"}}})
		}),
		"branding entry for another setting": brandingCase(BrandingEntry{Key: "setup.completed", Ref: "evil.example", Blob: "branding/mark/evil.example"}),
		"branding ref with a path":           brandingCase(BrandingEntry{Key: "branding.mark_ref", Ref: "../x.webp", Blob: "branding/x.webp"}),
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
// format 1 to 3. An older set holds none of what a later version added, and
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
		{"version 1 without them either", 1, func(i *RestoreSetIndex, e *[]testEntry) {
			dropSection(i, e, SectionHomeSections)
			dropTemplates(i, e)
		}, true},
		{"version 2 with home sections", 2, func(i *RestoreSetIndex, e *[]testEntry) { dropTemplates(i, e) }, false},
		{"version 2 with template collections", 2, func(i *RestoreSetIndex, e *[]testEntry) { dropSection(i, e, SectionHomeSections) }, false},
		{"a section carrying a collection its configuration does not name", 3, func(i *RestoreSetIndex, e *[]testEntry) {
			for j := range *e {
				if (*e)[j].name == homeSectionsDocumentName {
					var doc HomeSectionsDocument
					_ = json.Unmarshal((*e)[j].data, &doc)
					doc.Sections[1].Config = json.RawMessage(`{"library_collection_id":"c1"}`)
					(*e)[j].data = mustJSON(doc)
				}
			}
			resyncDigests(i, *e)
		}, false},
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
