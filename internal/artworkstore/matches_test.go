package artworkstore

import (
	"context"
	"testing"
)

func TestFilesystemMatchesContent(t *testing.T) {
	store, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if ok, err := store.Matches(ctx, "a.webp", []byte("abc")); err != nil || ok {
		t.Fatalf("missing: %v %v", ok, err)
	}
	if err := store.Put(ctx, "a.webp", []byte("abc")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		data string
		want bool
	}{{"abc", true}, {"xyz", false}, {"longer", false}} {
		if ok, err := store.Matches(ctx, "a.webp", []byte(tc.data)); err != nil || ok != tc.want {
			t.Fatalf("%q: %v %v", tc.data, ok, err)
		}
	}
	if _, err := store.Matches(ctx, "../outside", nil); err == nil {
		t.Fatal("invalid key accepted")
	}
}

func TestRecordingStoreForwardsMatches(t *testing.T) {
	store, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.Put(ctx, "a.webp", []byte("abc")); err != nil {
		t.Fatal(err)
	}
	settings := &testSettings{values: map[string]string{}}
	recorded := &recordingStore{Store: store, settings: settings}
	if ok, err := recorded.Matches(ctx, "a.webp", []byte("abc")); err != nil || !ok {
		t.Fatalf("match: %v %v", ok, err)
	}
	if settings.values[IdentitySettingKey] != store.Identity() {
		t.Fatal("existing content did not pin backend")
	}
}

func TestMatchingRetryRecordsBackendAfterWriteRecordingFailure(t *testing.T) {
	ctx := context.Background()
	settings := &flakySettings{testSettings: testSettings{values: map[string]string{}}, fail: true}
	store, _, err := Open(ctx, Options{Backend: BackendLocal, LocalPath: t.TempDir(), Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, "a.webp", []byte("abc")); err == nil {
		t.Fatal("recording failure was hidden")
	}
	matcher := store.(interface {
		Matches(context.Context, string, []byte) (bool, error)
	})
	if matched, err := matcher.Matches(ctx, "a.webp", []byte("abc")); err != nil || !matched {
		t.Fatalf("retry: %v %v", matched, err)
	}
	if settings.values[IdentitySettingKey] != store.Identity() {
		t.Fatalf("backend not recorded: %#v", settings.values)
	}
}
