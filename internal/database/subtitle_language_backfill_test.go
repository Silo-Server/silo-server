package database

import (
	"reflect"
	"testing"
)

func TestSubtitleLanguageRewrites(t *testing.T) {
	got, unparseable := subtitleLanguageRewrites([]string{
		"", "en", "eng", "English", "pt-br", "en-US", "zh-Hans", "sr-Latn",
		"fre", "ger", "spa", "und", "fil", "forced", "Монгол", "x-foo", "i-klingon",
	})
	want := map[string]string{
		"eng": "en", "English": "en", "pt-br": "pt-BR",
		"fre": "fr", "ger": "de", "spa": "es", "i-klingon": "tlh",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rewrites = %v, want %v", got, want)
	}
	// Values the canonicalizer rejects are reported, never rewritten: the
	// migration cannot restore what it overwrites.
	wantUnparseable := []string{"forced", "Монгол"}
	if !reflect.DeepEqual(unparseable, wantUnparseable) {
		t.Fatalf("unparseable = %v, want %v", unparseable, wantUnparseable)
	}
}
