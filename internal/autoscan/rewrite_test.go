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

	// Segment-boundary matching: a sibling dir sharing the prefix must NOT match.
	boundary := []PathRewrite{{From: "/data/media", To: "/mnt"}}
	if got := applyRewrites("/data/media2/x", boundary); got != "/data/media2/x" {
		t.Fatalf("boundary: /data/media2/x must not rewrite, got %q", got)
	}
	if got := applyRewrites("/data/media/x", boundary); got != "/mnt/x" {
		t.Fatalf("boundary: /data/media/x -> /mnt/x, got %q", got)
	}
	if got := applyRewrites("/data/media", boundary); got != "/mnt" {
		t.Fatalf("boundary: exact /data/media -> /mnt, got %q", got)
	}
}
