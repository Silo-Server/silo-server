package blobstore

import (
	"context"
	"testing"
)

func TestMatchingRetryRecordsBackendAfterWriteRecordingFailure(t *testing.T) {
	ctx := context.Background()
	settings := &flakySettings{testSettings: testSettings{values: map[string]string{}}, fail: true}
	stores, _, err := Open(ctx, Options{Backend: BackendLocal, LocalPath: t.TempDir(), Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	store := stores.Assets
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
