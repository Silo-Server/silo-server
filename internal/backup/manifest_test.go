package backup

import (
	"encoding/json"
	"strings"
	"testing"
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
		"applied migrations out of order": func(m *Manifest) {
			m.GooseVersion, m.AppliedMigrations = 9, []int64{9, 1, 9}
		},
		"applied migrations end elsewhere": func(m *Manifest) {
			m.GooseVersion, m.AppliedMigrations = 9, []int64{1, 5}
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
	data, _ := json.Marshal(good)
	if _, err := ParseManifest(data); err != nil {
		t.Fatalf("a valid manifest with an absent table was refused: %v", err)
	}
}
