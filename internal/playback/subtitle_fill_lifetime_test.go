package playback

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestSubtitleFillClaimsCoverPublicationFaultAndRepeatedDiscard(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fault bool
	}{{"commit", false}, {"rename failure", true}} {
		t.Run(tc.name, func(t *testing.T) {
			cache, source := newTestCache(t)
			before := SubtitleFillsInFlight()
			fill := cache.BeginFill(source, 0)
			if fill == nil || SubtitleFillsInFlight() != before+1 {
				t.Fatal("fill was not reserved before scheduling")
			}
			if _, err := fill.Tee(io.Discard).Write([]byte("complete subtitles")); err != nil {
				t.Fatal(err)
			}
			if tc.fault {
				if err := os.Mkdir(filepath.Join(cache.dir(), fill.key), 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := fill.Commit()
			if (err != nil) != tc.fault {
				t.Fatalf("publication result: %v", err)
			}
			if SubtitleFillsInFlight() != before {
				t.Fatal("finished publication retained a claim")
			}
			if _, err := os.Stat(fill.tmp.Name()); !os.IsNotExist(err) {
				t.Fatalf("publication retained temporary file: %v", err)
			}
			next := cache.BeginFill(source, 0)
			if next == nil || SubtitleFillsInFlight() != before+1 {
				t.Fatal("next fill lost admission")
			}
			fill.Discard()
			fill.Discard()
			if SubtitleFillsInFlight() != before+1 {
				t.Fatal("old repeated discard released a newer fill")
			}
			next.Discard()
			if SubtitleFillsInFlight() != before {
				t.Fatal("next fill cleanup retained a claim")
			}
		})
	}
}
