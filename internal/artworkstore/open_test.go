package artworkstore

import (
	"context"
	"path/filepath"
	"testing"
)

type testSettings struct {
	values map[string]string
	writes int
}

type flakySettings struct {
	testSettings
	fail bool
}

func (s *flakySettings) SetIfAbsent(ctx context.Context, key, value string) (bool, error) {
	if s.fail {
		s.fail = false
		return false, context.Canceled
	}
	return s.testSettings.SetIfAbsent(ctx, key, value)
}

func (s *testSettings) Get(_ context.Context, key string) (string, error) { return s.values[key], nil }
func (s *testSettings) SetIfAbsent(_ context.Context, key, value string) (bool, error) {
	s.writes++
	if s.values[key] != "" {
		return false, nil
	}
	s.values[key] = value
	return true, nil
}

func TestOpenLocalRecordsBackendOnFirstPut(t *testing.T) {
	settings := &testSettings{values: map[string]string{}}
	store, backend, err := Open(context.Background(), Options{Backend: "auto", LocalPath: filepath.Join(t.TempDir(), "artwork"), Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	if backend != BackendLocal {
		t.Fatalf("backend=%q", backend)
	}
	if settings.writes != 0 {
		t.Fatal("recorded before write")
	}
	if err = store.Put(context.Background(), "a.webp", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if settings.values[IdentitySettingKey] != store.Identity() || settings.writes != 1 {
		t.Fatalf("settings=%#v writes=%d", settings.values, settings.writes)
	}
}
func TestOpenRejectsRecordedStorageMismatch(t *testing.T) {
	root := t.TempDir()
	current, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, recorded := range map[string]string{
		"other backend": BackendS3 + "|https://s3.example|artwork|",
		"other root":    BackendLocal + "|" + filepath.Join(root, "elsewhere"),
	} {
		settings := &testSettings{values: map[string]string{IdentitySettingKey: recorded}}
		if _, _, err := Open(context.Background(), Options{Backend: BackendLocal, LocalPath: root, Settings: settings}); err == nil {
			t.Fatalf("%s: mismatch accepted", name)
		}
	}
	settings := &testSettings{values: map[string]string{IdentitySettingKey: current.Identity()}}
	if _, _, err := Open(context.Background(), Options{Backend: BackendLocal, LocalPath: root, Settings: settings}); err != nil {
		t.Fatalf("same root rejected: %v", err)
	}
}

func TestOpenRetriesBackendRecordingAfterSettingsFailure(t *testing.T) {
	settings := &flakySettings{testSettings: testSettings{values: map[string]string{}}, fail: true}
	store, _, err := Open(context.Background(), Options{Backend: BackendLocal, LocalPath: filepath.Join(t.TempDir(), "artwork"), Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "a.webp", []byte("x")); err == nil {
		t.Fatal("write hid backend recording failure")
	}
	if err := store.Put(context.Background(), "a.webp", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if settings.values[IdentitySettingKey] != store.Identity() {
		t.Fatalf("settings = %#v", settings.values)
	}
}
