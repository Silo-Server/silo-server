package backup

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appendRaw(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
}

// TestJournalSurvivesTwoInterruptedRecoveries: each recovery that dies in the
// middle of writing a record leaves a torn last line. Reopening cuts it off
// before appending, so the next record never lands after damaged bytes and
// every complete record survives.
func TestJournalSurvivesTwoInterruptedRecoveries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restore.journal")
	j, err := createJournal(path, journalHeader{Time: "t", LiveDatabase: "silo", LiveOID: 100}, testJournalKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.step(t.Context(), stepLiveClosed); err != nil {
		t.Fatal(err)
	}
	j.close()
	for i, next := range []string{stepRollbackRename, stepRollbackBlobs} {
		appendRaw(t, path, `{"step":"rollback_dr`) // torn mid-write
		j, err = openJournal(path, testJournalKey)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		if err := j.step(t.Context(), next); err != nil {
			t.Fatal(err)
		}
		j.close()
	}
	j, err = openJournal(path, testJournalKey)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	for _, step := range []string{stepLiveClosed, stepRollbackRename, stepRollbackBlobs} {
		if !j.has(step) {
			t.Fatalf("step %s lost", step)
		}
	}
	if j.has(stepRollbackDrop) || j.header.LiveOID != 100 {
		t.Fatalf("journal state wrong: %+v", j.header)
	}
	data, _ := os.ReadFile(path)
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if !json.Valid([]byte(line)) || !strings.Contains(line, `"mac":`) {
			t.Fatalf("line %d is damaged: %q", i, line)
		}
	}
}

// TestJournalRefusesInteriorDamage: damage before the last record is not a
// torn write, and recovery must not guess past it.
func TestJournalRefusesInteriorDamage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restore.journal")
	j, err := createJournal(path, journalHeader{Time: "t", LiveDatabase: "silo"}, testJournalKey)
	if err != nil {
		t.Fatal(err)
	}
	j.close()
	appendRaw(t, path, "{not json\n"+`{"record":{"step":"live_closed"},"mac":"00"}`+"\n")
	if _, err := openJournal(path, testJournalKey); !errors.Is(err, errCorruptJournal) {
		t.Fatalf("openJournal = %v, want errCorruptJournal", err)
	}
}

// testJournalKey authenticates the journals of these tests.
var testJournalKey = []byte("restore-journal-test-key-0123456789")

// TestJournalAcceptsOnlyWhatItsKeyWrote: --recover acts only on a journal
// this server's SECRET_KEY wrote, record by record and in order. A torn
// last line is still cut off; a changed header, a changed middle record,
// another key, or a journal without MACs is refused.
func TestJournalAcceptsOnlyWhatItsKeyWrote(t *testing.T) {
	write := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "restore.journal")
		j, err := createJournal(path, journalHeader{Time: "t", LiveDatabase: "silo", LiveOID: 100, RestoredOID: 200}, testJournalKey)
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range []string{stepLiveClosed, stepRenameLiveIntent} {
			if err := j.step(t.Context(), step); err != nil {
				t.Fatal(err)
			}
		}
		j.close()
		return path
	}
	rewrite := func(t *testing.T, path, from, to string) {
		t.Helper()
		data, _ := os.ReadFile(path)
		if !strings.Contains(string(data), from) {
			t.Fatalf("journal lacks %q", from)
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(data), from, to, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		change func(t *testing.T, path string)
		key    []byte
		ok     bool
	}{
		"as written":            {func(*testing.T, string) {}, testJournalKey, true},
		"a torn last line":      {func(t *testing.T, path string) { appendRaw(t, path, `{"record":{"step":"live_ren`) }, testJournalKey, true},
		"a changed header":      {func(t *testing.T, path string) { rewrite(t, path, `"live_oid":100`, `"live_oid":300`) }, testJournalKey, false},
		"a changed middle line": {func(t *testing.T, path string) { rewrite(t, path, stepLiveClosed, stepLiveReopened) }, testJournalKey, false},
		"another key":           {func(*testing.T, string) {}, []byte("another-journal-key-0123456789abcdef"), false},
		"no MACs": {func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"header":{"time":"t","live_database":"silo"}}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, testJournalKey, false},
	} {
		t.Run(name, func(t *testing.T) {
			path := write(t)
			tc.change(t, path)
			j, err := openJournal(path, tc.key)
			if (err == nil) != tc.ok {
				t.Fatalf("openJournal = %v, want accepted %v", err, tc.ok)
			}
			if err == nil {
				j.close()
			}
		})
	}
	// A journal for another database than DATABASE_URL names: refused
	// before any control action (ctl is nil, so one would panic).
	if err := RecoverSwap(t.Context(), write(t), nil, nil, testJournalKey, "postgres://silo@db.invalid/other", nil, io.Discard); err == nil ||
		!strings.Contains(err.Error(), `not "other"`) {
		t.Fatalf("recovering another database's journal = %v, want refused", err)
	}
	// A journal in a directory others can write to is refused.
	path := write(t)
	if err := os.Chmod(filepath.Dir(path), 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := openJournal(path, testJournalKey); !errors.Is(err, errUnauthenticatedJournal) {
		t.Fatalf("a journal in a world-writable directory = %v, want refused", err)
	}
}
