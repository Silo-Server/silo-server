package downloads

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadstorage"
)

func TestOverCeilingIgnoresAMeasurementOlderThanTheLastEviction(t *testing.T) {
	m := &ArtifactManager{}
	now := time.Now()
	measured := now.Add(-time.Minute)
	loc := storageLocation{NodeID: 3, Usage: &downloadstorage.Usage{MeasuredAt: measured, FSUsedBytes: 900, FSTotalBytes: 1000}}
	if got := m.overCeiling(loc, 0, time.Time{}, now); got != 50 {
		t.Fatalf("over ceiling = %d, want 50 above 85%%", got)
	}
	if got := m.overCeiling(loc, 100, time.Time{}, now); got != -50 {
		t.Fatalf("over ceiling after freeing 100 = %d, want -50", got)
	}
	if got := m.overCeiling(loc, 0, now, now); got != 0 {
		t.Fatalf("a measurement taken before the last eviction was acted on again: %d", got)
	}
	stale := storageLocation{NodeID: 4, Usage: &downloadstorage.Usage{MeasuredAt: now, FSUsedBytes: 999, FSTotalBytes: 1000, Stale: true}}
	if got := m.overCeiling(stale, 0, time.Time{}, now); got != 0 {
		t.Fatalf("a stale measurement was acted on: %d", got)
	}
	// A node that stopped reporting may have freed space since.
	if got := m.overCeiling(loc, 0, time.Time{}, now.Add(ceilingMeasurementMaxAge)); got != 0 {
		t.Fatalf("an old measurement was acted on: %d", got)
	}
}

func TestFindUntracked(t *testing.T) {
	now := time.Now()
	old, fresh := now.Add(-2*time.Hour), now.Add(-time.Minute)
	const (
		a = "11-00000000-0000-4000-8000-00000000000a"
		b = "12-00000000-0000-4000-8000-00000000000b"
		c = "13-00000000-0000-4000-8000-00000000000c"
		d = "14-00000000-0000-4000-8000-00000000000d"
		e = "15-00000000-0000-4000-8000-00000000000e"
	)
	files := []downloadstorage.File{
		{Name: a + ".mp4", Kind: downloadstorage.KindComplete, Bytes: 10, ModTime: old},              // tracked
		{Name: a + ".mp4.receipt.json", Kind: downloadstorage.KindOther, Bytes: 1, ModTime: old},     // belongs to tracked
		{Name: b + ".mp4", Kind: downloadstorage.KindComplete, Bytes: 20, ModTime: old},              // untracked
		{Name: b + ".mp4.receipt.json", Kind: downloadstorage.KindOther, Bytes: 2, ModTime: old},     // untracked
		{Name: c + ".mp4", Kind: downloadstorage.KindComplete, Bytes: 30, ModTime: fresh},            // too new to judge
		{Name: d + ".mp4.part", Kind: downloadstorage.KindPartial, Bytes: 40, ModTime: old},          // dead partial
		{Name: e + ".mp4.part", Kind: downloadstorage.KindPartial, Bytes: 50, ModTime: fresh},        // encode in progress
		{Name: a + ".mp4.receipt.json.123", Kind: downloadstorage.KindOther, Bytes: 3, ModTime: old}, // temp of tracked
		// A dead partial beside a tracked file: deleting it would delete the
		// whole artifact on the node, finished file included.
		{Name: a + ".mp4.part", Kind: downloadstorage.KindPartial, Bytes: 4, ModTime: old},
		// Not named like a prepared file: never Silo's to delete.
		{Name: "Movie (2010).mp4", Kind: downloadstorage.KindComplete, Bytes: 70, ModTime: old},
		{Name: "README", Kind: downloadstorage.KindOther, Bytes: 80, ModTime: old},
		{Name: "12_remux_abc_34.mp4", Kind: downloadstorage.KindComplete, Bytes: 90, ModTime: old}, // a server file
	}
	got := findUntracked(files, nodeArtifactFileName, map[string]bool{a + ".mp4": true}, now)
	names := map[string]bool{}
	for _, f := range got {
		names[f.Name] = true
	}
	want := []string{b + ".mp4", b + ".mp4.receipt.json", d + ".mp4.part"}
	if len(got) != len(want) {
		t.Fatalf("untracked = %v, want %v", names, want)
	}
	for _, n := range want {
		if !names[n] {
			t.Fatalf("untracked = %v, missing %s", names, n)
		}
	}
	if s := sumUntracked(got); s.Files != 3 || s.Bytes != 62 {
		t.Fatalf("summary = %+v", s)
	}
}

func TestServerArtifactFileName(t *testing.T) {
	for name, want := range map[string]bool{
		"12_transcode_0a1b2c3d4e5f6a7b_146672991.mp4":     true,
		"12_remux_legacy-parameter_146672991.mp4.part":    true,
		"12_remux_abc_34.mp4.receipt.json":                false,
		"Movie (2010).mp4":                                false,
		"11-00000000-0000-4000-8000-00000000000a.mp4":     false,
		"12_transcode_0a1b2c3d4e5f6a7b_146672991.mp4.bak": false,
	} {
		if got := serverArtifactFileName.MatchString(name); got != want {
			t.Errorf("serverArtifactFileName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestParseLocationKey(t *testing.T) {
	for key, want := range map[string]int{"server": 0, "node:7": 7} {
		if got, ok := ParseLocationKey(key); !ok || got != want {
			t.Errorf("ParseLocationKey(%q) = (%d, %v)", key, got, ok)
		}
	}
	for _, key := range []string{"", "node:", "node:0", "node:-1", "node:07", "device", "node:x"} {
		if _, ok := ParseLocationKey(key); ok {
			t.Errorf("ParseLocationKey(%q) accepted", key)
		}
	}
}
