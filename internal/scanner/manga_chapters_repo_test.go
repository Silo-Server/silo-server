package scanner

import "testing"

func TestMangaChapterWrite(t *testing.T) {
	idx, vol := mangaChapterWrite("v13", 13, true)
	if idx == nil || *idx != 13 || vol != "v13" {
		t.Fatalf("has=true: got (%v,%q), want (13,\"v13\")", idx, vol)
	}
	idx, vol = mangaChapterWrite("", 178, true)
	if idx == nil || *idx != 178 || vol != "" {
		t.Fatalf("has=true no vol: got (%v,%q), want (178,\"\")", idx, vol)
	}
	idx, vol = mangaChapterWrite("", 0, false)
	if idx != nil || vol != "" {
		t.Fatalf("has=false: got (%v,%q), want (nil,\"\")", idx, vol)
	}
}
