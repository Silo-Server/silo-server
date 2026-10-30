package heroclips

import (
	"strings"
	"testing"
)

func TestClipKeyLayoutAndStability(t *testing.T) {
	const contentID = "movie:tmdb-447332"

	recipe := NewRecipe(DefaultWidth)
	key := ClipKey(contentID, recipe)

	if !strings.HasPrefix(key, keyRoot) || !strings.HasSuffix(key, "/"+recipe.String()+".mp4") {
		t.Fatalf("ClipKey = %q, want a recipe file under a directory of its own", key)
	}

	// A colon in the content id must not reach storage: local storage refuses it
	// in a filename, and a URL has to escape it.
	if strings.Contains(key, ":") {
		t.Fatalf("key %q is not storage safe", key)
	}

	if again := ClipKey(contentID, recipe); again != key {
		t.Fatalf("ClipKey is not stable: %q then %q", key, again)
	}

	if ClipKey("series:tvdb-372264", recipe) == key {
		t.Fatal("two items must not name the same object")
	}

	if !strings.HasPrefix(ClipKey(contentID, NewRecipe(640)), ItemPrefix(contentID)) {
		t.Fatal("every recipe of one item belongs under the item's prefix")
	}

	if ItemPrefix("movie:tmdb-447332") == ItemPrefix("series:tvdb-372264") {
		t.Fatal("two items must not share a prefix")
	}
}

func TestClipGroupsArePerKey(t *testing.T) {
	const contentID = "movie:tmdb-447332"

	recipe := NewRecipe(DefaultWidth)
	key := ClipKey(contentID, recipe)

	group, ok := clipGroup(key)
	if !ok || group != key {
		t.Fatalf("clipGroup(%q) = %q, %v; want the key itself", key, group, ok)
	}

	// Grouping per key is what lets a superseded recipe be collected while the
	// item stays live.
	superseded, ok := clipGroup(ClipKey(contentID, NewRecipe(640)))
	if !ok || superseded == group {
		t.Fatalf("a superseded recipe shares the live clip's group: %q", superseded)
	}

	if !IsClipKey(key) {
		t.Fatalf("IsClipKey(%q) = false", key)
	}
}

func TestClipKeyRecognitionRejectsForeignKeys(t *testing.T) {
	digest := strings.Repeat("a", contentIDHashLength)

	for _, key := range []string{
		"chapter-images/1/0-deadbeef/w320.webp",
		"trickplay/1/2/0.webp",
		"hero-clips/",
		"hero-clips/" + digest,
		"hero-clips/nothex/v1-w1280-crf28-a128k.mp4",
		"hero-clips/" + strings.Repeat("a", contentIDHashLength-1) + "/v1-w1280-crf28-a128k.mp4",
		"hero-clips/" + strings.ToUpper(digest) + "/v1-w1280-crf28-a128k.mp4",
		"hero-clips/" + digest + "/../v1-w1280-crf28-a128k.mp4",
		"hero-clips/" + digest + "/nested/v1-w1280-crf28-a128k.mp4",
	} {
		if _, ok := clipGroup(key); ok {
			t.Fatalf("clipGroup(%q) accepted a foreign key", key)
		}
		if IsClipKey(key) {
			t.Fatalf("IsClipKey(%q) accepted a foreign key", key)
		}
	}
}
