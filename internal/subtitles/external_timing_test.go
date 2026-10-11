package subtitles

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSyncKeys(t *testing.T) {
	stored := StoredSyncKey(42)
	if id, pathKey, ok := ParseSyncKey(stored); !ok || id != 42 || pathKey != "" {
		t.Fatalf("stored key %q parsed as %d %q %t", stored, id, pathKey, ok)
	}
	external := ExternalSyncKey("/media/Movie.en.srt")
	if id, pathKey, ok := ParseSyncKey(external); !ok || id != 0 || pathKey != ExternalPathKey("/media/Movie.en.srt") {
		t.Fatalf("external key %q parsed as %d %q %t", external, id, pathKey, ok)
	}
	if got := (SyncTarget{MediaFileID: 1, StoredID: 42}).Key(); got != stored {
		t.Fatalf("stored target key %q", got)
	}
	if got := (SyncTarget{MediaFileID: 1, ExternalPath: "/media/Movie.en.srt"}).Key(); got != external {
		t.Fatalf("external target key %q", got)
	}
	for _, bad := range []string{"", "stored-0", "stored-042", "stored-x", "external-abc", "external-" + strings.Repeat("A", 64), "embedded-1"} {
		if _, _, ok := ParseSyncKey(bad); ok {
			t.Errorf("ParseSyncKey(%q) accepted", bad)
		}
	}
}

type timingLookup map[string]*ExternalTiming

func (l timingLookup) ExternalTiming(_ context.Context, _ int, sha string) (*ExternalTiming, error) {
	if row, ok := l[sha]; ok {
		return row, nil
	}
	if row, ok := l["error"]; ok && row == nil {
		return nil, errors.New("database unavailable")
	}
	return nil, nil
}

func TestExternalDelivery(t *testing.T) {
	ctx := context.Background()
	srt := []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")
	sha := ContentSHA256(srt)

	same, revision, err := ExternalDelivery(ctx, nil, 7, FormatSRT, srt)
	if err != nil || string(same) != string(srt) || revision != sha[:16]+"-0" {
		t.Fatalf("no lookup: %q %q %v", same, revision, err)
	}

	lookup := timingLookup{sha: {ID: 1, MediaFileID: 7, ContentSHA256: sha, Timing: Timing{Scale: 1, OffsetMS: 1500}, Revision: 3}}
	timed, revision, err := ExternalDelivery(ctx, lookup, 7, FormatSRT, srt)
	if err != nil || !strings.Contains(string(timed), "00:00:02,500 --> 00:00:03,500") || revision != ContentSHA256(timed)[:16]+"-3" {
		t.Fatalf("corrected: %q %q %v", timed, revision, err)
	}
	// A correction recreated after a media file replacement can reach the
	// same revision with another timing; the delivered bytes tell them apart.
	recreated := timingLookup{sha: {ID: 2, MediaFileID: 7, ContentSHA256: sha, Timing: Timing{Scale: 1, OffsetMS: -400}, Revision: 3}}
	if _, other, err := ExternalDelivery(ctx, recreated, 7, FormatSRT, srt); err != nil || other == revision {
		t.Fatalf("recreated correction revision %q, first %q, err %v", other, revision, err)
	}

	// Other bytes (the sidecar was edited on disk) do not match the row.
	edited := []byte("1\n00:00:01,000 --> 00:00:02,000\nHello there\n")
	out, revision, err := ExternalDelivery(ctx, lookup, 7, FormatSRT, edited)
	if err != nil || string(out) != string(edited) || revision != ContentSHA256(edited)[:16]+"-0" {
		t.Fatalf("edited sidecar: %q %q %v", out, revision, err)
	}

	// A format Retime cannot rewrite is never looked up.
	micro := []byte("{25}{50}Hello")
	if out, err := ExternalDeliveryBytes(ctx, timingLookup{"error": nil}, 7, FormatSUB, micro); err != nil || string(out) != string(micro) {
		t.Fatalf("microdvd: %q %v", out, err)
	}
	if _, err := ExternalDeliveryBytes(ctx, timingLookup{"error": nil}, 7, FormatSRT, srt); err == nil {
		t.Fatal("lookup failure served uncorrected bytes")
	}
}
