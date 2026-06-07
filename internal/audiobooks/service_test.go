package audiobooks

import (
	"context"
	"errors"
	"testing"
)

type fakeSettingsReader struct {
	value string
	err   error
}

func (f *fakeSettingsReader) GetString(_ context.Context, key string) (string, error) {
	if key != "audiobookshelf_compat.enabled" {
		return "", errors.New("unexpected key: " + key)
	}
	return f.value, f.err
}

func TestServiceABSCompatEnabledReadsFlag(t *testing.T) {
	cases := []struct {
		name   string
		stored string
		want   bool
	}{
		{"flag true", "true", true},
		{"flag false", "false", false},
		{"flag empty defaults false", "", false},
		{"flag garbage defaults false", "yes-please", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			svc := New(&fakeSettingsReader{value: tc.stored})
			got, err := svc.ABSCompatEnabled(context.Background())
			if err != nil {
				t.Fatalf("ABSCompatEnabled returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ABSCompatEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServiceABSCompatEnabledPropagatesError(t *testing.T) {
	wantErr := errors.New("db down")
	svc := New(&fakeSettingsReader{err: wantErr})
	_, err := svc.ABSCompatEnabled(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("ABSCompatEnabled error = %v, want %v wrapped", err, wantErr)
	}
}

func TestServiceABSCompatEnabledNilReceiverReturnsFalse(t *testing.T) {
	var svc *Service
	got, err := svc.ABSCompatEnabled(context.Background())
	if err != nil {
		t.Fatalf("ABSCompatEnabled returned error: %v", err)
	}
	if got {
		t.Fatal("ABSCompatEnabled = true, want false")
	}
}

func TestServiceABSCompatEnabledNilSettingsReturnsFalse(t *testing.T) {
	svc := New(nil)
	got, err := svc.ABSCompatEnabled(context.Background())
	if err != nil {
		t.Fatalf("ABSCompatEnabled returned error: %v", err)
	}
	if got {
		t.Fatal("ABSCompatEnabled = true, want false")
	}
}
