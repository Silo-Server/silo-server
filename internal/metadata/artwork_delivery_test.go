package metadata

import "testing"

// TestRequestRecoverableArtworkSourceMatchesRepairAdmissibility pins the
// repair/protect split: a source is only "recoverable" when the repair queue
// (or the request-time fallback) can actually act on it. Classifying a
// non-refetchable scheme as recoverable marks the revision repair-queued while
// no job is ever admitted, so the loss never raises the protected-loss alert.
func TestRequestRecoverableArtworkSourceMatchesRepairAdmissibility(t *testing.T) {
	recoverable := []string{
		"https://image.tmdb.org/t/p/original/x.jpg",
		"http://example.com/poster.jpg",
		"tmdb://poster/123",
		"tvdb-plugin://series/42/backdrop",
		"file:///library/Movies/Poster.jpg",
		"library-artwork://abc",
	}
	for _, source := range recoverable {
		if !isRequestRecoverableArtworkSource(source) {
			t.Errorf("isRequestRecoverableArtworkSource(%q) = false, want true", source)
		}
	}
	protected := []string{
		"",
		"upload://poster/1",
		"embedded://cover",
		"generated://collage/9",
		"s3://bucket/legacy/key.webp",
		"local://old/path.jpg",
		"artwork/v1/ab/cd/rev/poster/original.webp",
	}
	for _, source := range protected {
		if isRequestRecoverableArtworkSource(source) {
			t.Errorf("isRequestRecoverableArtworkSource(%q) = true, want false", source)
		}
	}
}
