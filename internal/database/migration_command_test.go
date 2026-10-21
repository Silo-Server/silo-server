package database

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

func TestPrintMigrationStatuses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []MigrationStatus
		rows     string
	}{
		{name: "empty"},
		{name: "applied and pending", statuses: []MigrationStatus{
			{State: "applied", Version: 12, Source: "sql/012_example.sql", AppliedAt: time.Date(2026, 10, 15, 8, 0, 0, 0, time.FixedZone("test", 3600))},
			{State: "pending", Version: 13},
		}, rows: "applied        12 2026-10-15T07:00:00Z      012_example.sql\n" +
			"pending        13 -                         -\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := printMigrationStatuses(&output, tc.statuses); err != nil {
				t.Fatalf("print status = %v, want success", err)
			}
			want := "STATE     VERSION APPLIED_AT                MIGRATION\n" + tc.rows
			if output.String() != want {
				t.Errorf("status output = %q, want %q", output.String(), want)
			}
		})
	}
}

type failingMigrationWriter struct{ remaining int }

func (w *failingMigrationWriter) Write(p []byte) (int, error) {
	if w.remaining == 0 {
		return 0, io.ErrClosedPipe
	}
	w.remaining--
	return len(p), nil
}

func TestPrintMigrationStatusesOutputFailure(t *testing.T) {
	for _, writes := range []int{0, 1} {
		err := printMigrationStatuses(&failingMigrationWriter{remaining: writes}, []MigrationStatus{{Version: 1}})
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("failure after %d writes = %v, want %v", writes, err, io.ErrClosedPipe)
		}
	}
}
