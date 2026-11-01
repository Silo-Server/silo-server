package backup

import (
	"encoding/json"
	"errors"
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
	j, err := createJournal(path, journalHeader{Time: "t", LiveDatabase: "silo", LiveOID: 100})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.step(t.Context(), stepLiveClosed); err != nil {
		t.Fatal(err)
	}
	j.close()
	for i, next := range []string{stepRollbackRename, stepRollbackBlobs} {
		appendRaw(t, path, `{"step":"rollback_dr`) // torn mid-write
		j, err = openJournal(path)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		if err := j.step(t.Context(), next); err != nil {
			t.Fatal(err)
		}
		j.close()
	}
	j, err = openJournal(path)
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
		if !json.Valid([]byte(line)) {
			t.Fatalf("line %d is damaged: %q", i, line)
		}
	}
}

// TestJournalRefusesInteriorDamage: damage before the last record is not a
// torn write, and recovery must not guess past it.
func TestJournalRefusesInteriorDamage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restore.journal")
	j, err := createJournal(path, journalHeader{Time: "t", LiveDatabase: "silo"})
	if err != nil {
		t.Fatal(err)
	}
	j.close()
	appendRaw(t, path, "{not json\n"+`{"step":"live_closed"}`+"\n")
	if _, err := openJournal(path); !errors.Is(err, errCorruptJournal) {
		t.Fatalf("openJournal = %v, want errCorruptJournal", err)
	}
}
