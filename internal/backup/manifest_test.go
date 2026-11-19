package backup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// beforeUpgrade makes m the database-only backup taken before migrations.
func beforeUpgrade(m *Manifest) {
	m.ID, m.Kind, m.Objects = "20261031T040000Z-before-upgrade", KindBeforeUpgrade, m.Objects[:1]
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
		"before-upgrade with blobs.tar": func(m *Manifest) {
			beforeUpgrade(m)
			m.Objects = append(m.Objects, ObjectEntry{Name: BlobsName, SHA256: m.Objects[0].SHA256})
		},
		"before-upgrade counting blobs": func(m *Manifest) { beforeUpgrade(m); m.Blobs.Count = 1 },
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
	upgrade := validTestManifest()
	beforeUpgrade(&upgrade)
	data, _ = json.Marshal(upgrade)
	if _, err := ParseManifest(data); err != nil {
		t.Fatalf("a database-only before-upgrade manifest was refused: %v", err)
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
