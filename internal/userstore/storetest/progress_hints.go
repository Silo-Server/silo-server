package storetest

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunProgressHints verifies that a version hints write reaches offline-sync
// deltas only when a hint changes. Every native heartbeat and playback stop
// repeats the hints of the file being played, so an unchanged write must not
// restamp the row's sync cursor.
func RunProgressHints(t *testing.T, newStore func(*testing.T) userstore.UserStore) {
	const profileID, itemID = "p1", "m1"
	ctx := t.Context()
	store := newStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: profileID}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateProgress(ctx, profileID, itemID, 500, 1000, userstore.ProgressThresholds{}); err != nil {
		t.Fatal(err)
	}
	_, cursor, err := store.ListProgressSince(ctx, profileID, "")
	if err != nil {
		t.Fatal(err)
	}
	writeHints := func(hints userstore.VersionHints) []userstore.WatchProgress {
		t.Helper()
		if err := store.UpdateProgressHints(ctx, profileID, itemID, hints); err != nil {
			t.Fatal(err)
		}
		delta, next, err := store.ListProgressSince(ctx, profileID, cursor)
		if err != nil {
			t.Fatal(err)
		}
		cursor = next
		return delta
	}
	wantHints := func(step string, delta []userstore.WatchProgress, want userstore.VersionHints) {
		t.Helper()
		if len(delta) != 1 || delta[0].MediaItemID != itemID {
			t.Fatalf("%s: delta = %+v, want the %s row", step, delta, itemID)
		}
		if got := storedHints(delta[0]); got != want {
			t.Fatalf("%s: hints = %+v, want %+v", step, got, want)
		}
	}

	// The row starts with no hints, so the first write is a change. The blank
	// edition key is stored as NULL. Each change is then repeated, as the next
	// heartbeat would, and the repeat must not reach the delta.
	hints := userstore.VersionHints{FileID: 1, Resolution: "1080p", CodecVideo: "h264"}
	wantUnchanged := func(step string) {
		t.Helper()
		if delta := writeHints(hints); len(delta) != 0 {
			t.Fatalf("%s repeated: unchanged hints redelivered the row: %+v", step, delta)
		}
	}
	wantHints("first hints", writeHints(hints), hints)
	wantUnchanged("first hints")
	for _, change := range []struct {
		name string
		edit func(*userstore.VersionHints)
	}{
		{"file", func(h *userstore.VersionHints) { h.FileID = 2 }},
		{"resolution", func(h *userstore.VersionHints) { h.Resolution = "2160p" }},
		{"hdr", func(h *userstore.VersionHints) { h.HDR = true }},
		{"codec", func(h *userstore.VersionHints) { h.CodecVideo = "hevc" }},
		{"edition set", func(h *userstore.VersionHints) { h.EditionKey = "directors-cut" }},
		{"edition cleared", func(h *userstore.VersionHints) { h.EditionKey = "" }},
	} {
		change.edit(&hints)
		wantHints(change.name, writeHints(hints), hints)
		wantUnchanged(change.name)
	}
}

// storedHints reads a row's hints back with NULL as the zero value, so both
// backends compare alike.
func storedHints(wp userstore.WatchProgress) userstore.VersionHints {
	var h userstore.VersionHints
	if wp.LastFileID != nil {
		h.FileID = *wp.LastFileID
	}
	if wp.LastResolution != nil {
		h.Resolution = *wp.LastResolution
	}
	if wp.LastHDR != nil {
		h.HDR = *wp.LastHDR
	}
	if wp.LastCodecVideo != nil {
		h.CodecVideo = *wp.LastCodecVideo
	}
	if wp.LastEditionKey != nil {
		h.EditionKey = *wp.LastEditionKey
	}
	return h
}
