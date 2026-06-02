package autoscan

import "testing"

func TestApplyRewrites(t *testing.T) {
	rw := []PathRewrite{{From: "/data/media", To: "/mnt/media"}}
	cases := []struct{ in, want string }{
		{"/data/media/Movies/Dune/Dune.mkv", "/mnt/media/Movies/Dune/Dune.mkv"},
		{"/other/path/file.mkv", "/other/path/file.mkv"},
	}
	for _, tc := range cases {
		if got := applyRewrites(tc.in, rw); got != tc.want {
			t.Fatalf("applyRewrites(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	multi := []PathRewrite{{From: "/data", To: "/A"}, {From: "/data/media", To: "/B"}}
	if got := applyRewrites("/data/media/x", multi); got != "/A/media/x" {
		t.Fatalf("first-match: got %q", got)
	}
	if got := applyRewrites("/data/media/x", nil); got != "/data/media/x" {
		t.Fatalf("nil rewrites: got %q", got)
	}
}
