package abs

import (
	"context"
	"errors"
	"testing"
)

// fakeProgressStore is a minimal in-memory ProgressStore for the play
// resume tests. It returns a fixed row on GetProgress; other methods are
// no-ops sufficient to satisfy the interface.
type fakeProgressStore struct {
	row    *ProgressRow
	getErr error
}

func (f *fakeProgressStore) GetProgress(_ context.Context, _, _, _ string) (*ProgressRow, error) {
	return f.row, f.getErr
}
func (f *fakeProgressStore) ListProgressForAudiobooks(_ context.Context, _, _ string, _ int) ([]ProgressRow, error) {
	return nil, nil
}
func (f *fakeProgressStore) UpsertProgress(_ context.Context, _ ProgressRow) error { return nil }
func (f *fakeProgressStore) UpdateProgressPosition(_ context.Context, _, _, _ string, _ float64) error {
	return nil
}
func (f *fakeProgressStore) SetHideFromContinue(_ context.Context, _, _, _ string, _ bool) error {
	return nil
}
func (f *fakeProgressStore) DeleteProgress(_ context.Context, _, _, _ string) error {
	return nil
}

func TestResumeTimeFromProgressStore_NoRow(t *testing.T) {
	store := &fakeProgressStore{row: nil}
	got, err := resolveResumeTime(context.Background(), store, "1", "", "book123")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != 0 {
		t.Errorf("resume time = %v, want 0", got)
	}
}

func TestResumeTimeFromProgressStore_NilStore(t *testing.T) {
	got, err := resolveResumeTime(context.Background(), nil, "1", "", "book123")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != 0 {
		t.Errorf("resume time = %v, want 0", got)
	}
}

func TestResumeTimeFromProgressStore_LookupError(t *testing.T) {
	store := &fakeProgressStore{getErr: errors.New("boom")}
	got, err := resolveResumeTime(context.Background(), store, "1", "", "book123")
	if err == nil {
		t.Errorf("expected error, got nil")
	}
	if got != 0 {
		t.Errorf("resume time = %v, want 0 on error", got)
	}
}
