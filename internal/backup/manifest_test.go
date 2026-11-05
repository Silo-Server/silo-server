package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

func validTestManifest() Manifest {
	hash := strings.Repeat("ab", 32)
	m := Manifest{Format: manifestFormat, Version: manifestVersion, ID: "20261031T040000Z-nightly", Kind: KindNightly, ServerID: "server",
		Objects: []ObjectEntry{{Name: DumpName, Size: 10, SHA256: hash}, {Name: BlobsName, Size: 0, SHA256: hash}}}
	for _, name := range inventoryTables {
		m.Tables = append(m.Tables, TableCount{Name: name, Present: true, Rows: 1})
	}
	return m
}

// TestParseManifestRefusesWhatARestoreCouldNotCheck: a restore verifies the
// restored database against the manifest's inventory and its objects against
// their checksums, so a manifest that leaves either out, or names something
// else, is refused rather than trusted.
func TestParseManifestRefusesWhatARestoreCouldNotCheck(t *testing.T) {
	tests := map[string]func(*Manifest){
		"inventory missing a table":  func(m *Manifest) { m.Tables = m.Tables[1:] },
		"duplicate inventory table":  func(m *Manifest) { m.Tables = append(m.Tables, m.Tables[0]) },
		"unknown inventory table":    func(m *Manifest) { m.Tables[0].Name = "pg_authid" },
		"absent table with rows":     func(m *Manifest) { m.Tables[0].Present = false },
		"missing object":             func(m *Manifest) { m.Objects = m.Objects[:1] },
		"duplicate object":           func(m *Manifest) { m.Objects[1] = m.Objects[0] },
		"unknown object":             func(m *Manifest) { m.Objects[1].Name = "../escape" },
		"object without a checksum":  func(m *Manifest) { m.Objects[0].SHA256 = "" },
		"negative object size":       func(m *Manifest) { m.Objects[0].Size = -1 },
		"kind disagrees with the id": func(m *Manifest) { m.Kind = KindManual },
		"server id with a slash":     func(m *Manifest) { m.ServerID = "../other" },
		"newer version":              func(m *Manifest) { m.Version = manifestVersion + 1 },
		"encrypted with plain names": func(m *Manifest) { m.Encrypted = true },
		"applied migrations out of order": func(m *Manifest) {
			m.GooseVersion, m.AppliedMigrations = 9, []int64{9, 1, 9}
		},
		"applied migrations end elsewhere": func(m *Manifest) {
			m.GooseVersion, m.AppliedMigrations = 9, []int64{1, 5}
		},
		"restore set summary without its object": func(m *Manifest) {
			m.RestoreSet = &RestoreSetSummary{FormatVersion: restoreSetVersion, Sections: map[string]int{SectionSettings: 1}}
		},
		"unknown restore set section": func(m *Manifest) {
			m.Objects = append(m.Objects, ObjectEntry{Name: RestoreSetName, SHA256: m.Objects[0].SHA256})
			m.RestoreSet = &RestoreSetSummary{FormatVersion: restoreSetVersion, Sections: map[string]int{"../x": 1}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			m := validTestManifest()
			mutate(&m)
			data, _ := json.Marshal(m)
			if _, err := ParseManifest(data); err == nil {
				t.Fatal("ParseManifest accepted it")
			}
		})
	}
	good := validTestManifest()
	good.GooseVersion, good.AppliedMigrations = 9, []int64{1, 5, 9}
	good.Tables[0] = TableCount{Name: good.Tables[0].Name}
	good.Objects = append(good.Objects, ObjectEntry{Name: RestoreSetName, SHA256: good.Objects[0].SHA256})
	good.RestoreSet = &RestoreSetSummary{FormatVersion: restoreSetVersion, Sections: map[string]int{SectionSettings: 3, SectionPlugins: 0}}
	data, _ := json.Marshal(good)
	if _, err := ParseManifest(data); err != nil {
		t.Fatalf("a valid manifest with an absent table was refused: %v", err)
	}
}

// TestManifestMAC: a manifest written under this server's SECRET_KEY is
// authenticated after it is written out and read back, and refused when
// anything in it changed, its MAC included; one written under another key
// cannot be checked and is reported as not authenticated.
func TestManifestMAC(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	signed := func() *Manifest {
		m := validTestManifest()
		m.KeyCheck = "aaaa-bbbb-cccc-dddd"
		if err := m.sign(key); err != nil {
			t.Fatal(err)
		}
		data, err := json.MarshalIndent(&m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		read, err := ParseManifest(data)
		if err != nil {
			t.Fatal(err)
		}
		return read
	}
	if ok, err := signed().checkMAC(key, "aaaa-bbbb-cccc-dddd"); !ok || err != nil {
		t.Fatalf("a signed manifest = %v, %v; want authenticated", ok, err)
	}
	for name, change := range map[string]func(*Manifest){
		"object checksum": func(m *Manifest) { m.Objects[0].SHA256 = strings.Repeat("0", 64) },
		"object size":     func(m *Manifest) { m.Objects[0].Size++ },
		"created time":    func(m *Manifest) { m.CreatedAt = m.CreatedAt.Add(time.Second) },
		"MAC removed":     func(m *Manifest) { m.MAC = "" },
		"MAC changed":     func(m *Manifest) { m.MAC = strings.Repeat("ab", 32) },
	} {
		m := signed()
		change(m)
		if _, err := m.checkMAC(key, "aaaa-bbbb-cccc-dddd"); !errors.Is(err, ErrManifestTampered) {
			t.Errorf("%s: checkMAC = %v, want ErrManifestTampered", name, err)
		}
	}
	if ok, err := signed().checkMAC(key, "1111-2222-3333-4444"); ok || err != nil {
		t.Fatalf("another key's manifest = %v, %v; want not authenticated, no error", ok, err)
	}
}

// TestManifestsOfEarlierBuildsStillVerify: a manifest is authenticated by
// a MAC over its JSON encoding, so a field added since a build wrote one
// must encode to nothing at its zero value, or every backup that build
// took would read as tampered. The files are manifests signed by builds
// from before encryption (with and without a restore set), and by one that
// wrote "encrypted":false, byte for byte.
func TestManifestsOfEarlierBuildsStillVerify(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	for _, name := range []string{"manifest-before-encryption.json", "manifest-restore-set-before-encryption.json", "manifest-encrypted-false.json"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		m, err := ParseManifest(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ok, err := m.checkMAC(key, "aaaa-bbbb-cccc-dddd"); !ok || err != nil {
			t.Errorf("%s = %v, %v; want authenticated", name, ok, err)
		}
	}
}

// failingManifests is a destination whose manifest reads fail, as a
// timeout or a destination error does.
type failingManifests struct {
	Destination
	fail map[string]bool
}

func (d failingManifests) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if d.fail[key] {
		return nil, 0, errors.New("the destination timed out")
	}
	return d.Destination.Open(ctx, key)
}

// TestListShowsUnverifiedBackups: the listing says a backup is unverified
// when its manifest names another SECRET_KEY or none, and when it names
// this key but does not verify; that one shows no details and the sweep
// leaves it. One whose manifest could not be read is unreadable, with a
// warning, and the sweep leaves it too. One whose manifest lists an object
// that is gone is incomplete, but the sweep leaves it: it has a manifest.
func TestListShowsUnverifiedBackups(t *testing.T) {
	ctx := t.Context()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	svc := New(Options{Destination: NewStoreDestination(store), KeyCheck: "aaaa-bbbb-cccc-dddd", ManifestKey: []byte("0123456789abcdef0123456789abcdef"),
		Now: func() time.Time { return now }, ServerID: func(context.Context) (string, error) { return "server", nil }})
	write := func(day int, keyCheck string, tamper bool) string {
		t.Helper()
		m := validTestManifest()
		m.ID = FolderName(now.AddDate(0, 0, -day), KindNightly)
		m.CreatedAt, m.CompletedAt, m.KeyCheck = now.AddDate(0, 0, -day), now.AddDate(0, 0, -day), keyCheck
		for _, o := range m.Objects {
			if err := store.Put(ctx, objectKey("server", m.ID, o.Name), make([]byte, o.Size)); err != nil {
				t.Fatal(err)
			}
		}
		if err := writeManifest(ctx, target{Destination: svc.opts.Destination}, &m, svc.opts.ManifestKey); err != nil {
			t.Fatal(err)
		}
		if tamper {
			m.Objects[0].SHA256 = strings.Repeat("0", 64)
			data, _ := json.Marshal(&m)
			if err := store.Put(ctx, objectKey("server", m.ID, ManifestName), data); err != nil {
				t.Fatal(err)
			}
		}
		return m.ID
	}
	verified, other, none, tampered := write(1, "aaaa-bbbb-cccc-dddd", false), write(2, "1111-2222-3333-4444", false), write(3, "", false), write(4, "aaaa-bbbb-cccc-dddd", true)
	partial := write(6, "aaaa-bbbb-cccc-dddd", false)
	if _, err := store.Delete(ctx, []string{objectKey("server", partial, DumpName)}); err != nil {
		t.Fatal(err)
	}
	unreadable := write(5, "aaaa-bbbb-cccc-dddd", false)
	svc.opts.Destination = failingManifests{Destination: svc.opts.Destination, fail: map[string]bool{objectKey("server", unreadable, ManifestName): true}}
	listing, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]Summary{}
	for _, b := range listing.Backups {
		status[b.ID] = b
	}
	if status[verified].Status != StatusComplete || status[other].Status != StatusUnverified || status[none].Status != StatusUnverified ||
		status[tampered].Status != StatusUnverified || len(status[tampered].Objects) != 0 || status[unreadable].Status != StatusUnreadable ||
		!slices.ContainsFunc(listing.Warnings, func(w Warning) bool { return w.Code == WarningBackupUnreadable }) || status[partial].Status != StatusIncomplete {
		t.Fatalf("statuses = %+v", status)
	}
	folders, err := svc.folders(ctx, target{Destination: svc.opts.Destination}, "server")
	if err != nil {
		t.Fatal(err)
	}
	if swept := planSweep(folders, now.AddDate(0, 1, 0)); len(swept) != 0 {
		t.Fatalf("the sweep would delete %v", swept)
	}
}

// countingManifests counts the manifest reads through it, and fails those
// fail names.
type countingManifests struct {
	Destination
	fail   map[string]bool
	opened *[]string
}

func (d countingManifests) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if strings.HasSuffix(key, "/"+ManifestName) {
		*d.opened = append(*d.opened, key)
	}
	if d.fail[key] {
		return nil, 0, errors.New("the destination timed out")
	}
	return d.Destination.Open(ctx, key)
}

// TestListReadsUnchangedManifestsOnce: a listing reads again only the
// manifests it could not read or verify before, and one whose object
// changed; a manifest that becomes readable is read and shown, and a
// deleted folder leaves the cache.
func TestListReadsUnchangedManifestsOnce(t *testing.T) {
	ctx := t.Context()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	var opened []string
	fail := map[string]bool{}
	svc := New(Options{Destination: countingManifests{Destination: NewStoreDestination(store), fail: fail, opened: &opened},
		KeyCheck: "aaaa-bbbb-cccc-dddd", ManifestKey: []byte("0123456789abcdef0123456789abcdef"),
		Now: func() time.Time { return now }, ServerID: func(context.Context) (string, error) { return "server", nil }})
	write := func(day int) string {
		t.Helper()
		m := validTestManifest()
		m.ID = FolderName(now.AddDate(0, 0, -day), KindNightly)
		m.CreatedAt, m.CompletedAt, m.KeyCheck = now.AddDate(0, 0, -day), now.AddDate(0, 0, -day), "aaaa-bbbb-cccc-dddd"
		for _, o := range m.Objects {
			if err := store.Put(ctx, objectKey("server", m.ID, o.Name), make([]byte, o.Size)); err != nil {
				t.Fatal(err)
			}
		}
		if err := writeManifest(ctx, target{Destination: NewStoreDestination(store)}, &m, svc.opts.ManifestKey); err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	first, second, unreadable := write(1), write(2), write(3)
	fail[objectKey("server", unreadable, ManifestName)] = true
	list := func() map[string]Status {
		t.Helper()
		opened = nil
		listing, err := svc.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		status := map[string]Status{}
		for _, b := range listing.Backups {
			status[b.ID] = b.Status
		}
		return status
	}
	if status := list(); len(opened) != 3 || status[unreadable] != StatusUnreadable {
		t.Fatalf("first listing read %v: %v", opened, status)
	}
	if status := list(); !slices.Equal(opened, []string{objectKey("server", unreadable, ManifestName)}) || status[first] != StatusComplete {
		t.Fatalf("second listing read %v, want only the unreadable manifest: %v", opened, status)
	}
	delete(fail, objectKey("server", unreadable, ManifestName))
	if status := list(); status[unreadable] != StatusComplete {
		t.Fatalf("a manifest readable again lists %v", status[unreadable])
	}
	if list(); len(opened) != 0 {
		t.Fatalf("listing after every manifest was read read %v", opened)
	}
	// A rewritten manifest is read again.
	m := validTestManifest()
	m.ID, m.CreatedAt, m.CompletedAt, m.KeyCheck = second, now, now.Add(time.Minute), "aaaa-bbbb-cccc-dddd"
	m.SiloVersion = "rewritten-" + m.SiloVersion
	if err := writeManifest(ctx, target{Destination: NewStoreDestination(store)}, &m, svc.opts.ManifestKey); err != nil {
		t.Fatal(err)
	}
	if list(); !slices.Equal(opened, []string{objectKey("server", second, ManifestName)}) {
		t.Fatalf("listing after a rewrite read %v", opened)
	}
	if _, err := store.DeletePrefix(ctx, folderPrefix("server", first)); err != nil {
		t.Fatal(err)
	}
	list()
	if n := len(svc.manifests.entries); n != 2 {
		t.Fatalf("cache holds %d manifests after a folder was deleted, want 2", n)
	}
}
