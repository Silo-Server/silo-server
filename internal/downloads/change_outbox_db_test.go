package downloads

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"
)

// TestDownloadChangeOutboxDB pins what download.changed reports for
// administrators and monitor keys: a managed row's creation, each status or
// revision change, a move to another title and its removal, whatever
// statement made them, with identifiers and state only. Ephemeral rows and
// writes that change none of those queue nothing.
func TestDownloadChangeOutboxDB(t *testing.T) {
	ctx := context.Background()
	f := seedManagedFixture(t)
	outbox := NewChangeOutbox(f.pool)
	// Other tests share the outbox; keep only this test's rows.
	claim := func(t *testing.T, ids ...string) []ChangeEvent {
		t.Helper()
		var out []ChangeEvent
		for {
			changes, more, err := outbox.Claim(ctx, 500, 0, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range changes {
				if slices.Contains(ids, c.DownloadID) {
					out = append(out, c)
				}
			}
			if !more {
				return out
			}
		}
	}
	claim(t)

	managed := f.fenceDownload("change-managed", f.fileID, f.contentID, true)
	ephemeral := f.fenceDownload("change-ephemeral", f.fileID, f.contentID, false)
	for _, d := range []*Download{managed, ephemeral} {
		if err := f.repo.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	steps := []string{
		// A file-request address and a byte count change neither status nor revision.
		`UPDATE downloads SET last_client_ip = '192.0.2.1', bytes_sent = 10 WHERE id = $1`,
		// A catalog merge moves the row to another title without a status change.
		`UPDATE downloads SET content_id = content_id || '-merged' WHERE id = $1`,
		`UPDATE downloads SET status = 'failed', error_message = 'open /srv/media/secret.mkv: no such file' WHERE id = $1`,
		`UPDATE downloads SET status = 'revoked', revoked_reason = 'secret reason' WHERE id = $1`,
		`DELETE FROM downloads WHERE id = $1`,
	}
	for _, step := range steps {
		for _, id := range []string{managed.ID, ephemeral.ID} {
			if _, err := f.pool.Exec(ctx, step, id); err != nil {
				t.Fatal(err)
			}
		}
	}

	changes := claim(t, managed.ID, ephemeral.ID)
	var got []string
	for _, c := range changes {
		got = append(got, c.Change+":"+c.Status)
	}
	if want := []string{"created:ready", "changed:ready", "changed:failed", "changed:revoked", "removed:revoked"}; !slices.Equal(got, want) {
		t.Fatalf("changes = %v, want %v", got, want)
	}
	for i, c := range changes {
		wantContent := f.contentID
		if i > 0 {
			wantContent += "-merged"
		}
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatal(err)
		}
		keys := slices.Sorted(maps.Keys(payload))
		if want := []string{"at", "change", "content_id", "device_id", "download_id", "profile_id", "status", "user_id"}; !slices.Equal(keys, want) {
			t.Fatalf("payload keys = %v, want %v (%s)", keys, want, raw)
		}
		if payload["user_id"] != strconv.Itoa(f.userID) || payload["profile_id"] != f.profileA || payload["device_id"] != f.deviceA || payload["content_id"] != wantContent {
			t.Fatalf("payload identifiers = %s", raw)
		}
		if at, err := time.Parse("2006-01-02T15:04:05.000Z", c.At); err != nil || at.IsZero() {
			t.Fatalf("at = %q: %v", c.At, err)
		}
	}
	if changes[0].EventID() == changes[1].EventID() {
		t.Fatal("two changes share an event id")
	}
}
