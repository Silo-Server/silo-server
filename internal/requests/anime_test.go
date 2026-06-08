package requests

import "testing"

func TestDetectAnime(t *testing.T) {
	if !detectAnime([]int{99, animeKeywordID, 7}) {
		t.Fatal("expected anime when keyword 210024 present")
	}
	if detectAnime([]int{99, 7}) {
		t.Fatal("expected non-anime when keyword 210024 absent")
	}
	if detectAnime(nil) {
		t.Fatal("expected non-anime for empty keywords")
	}
}
