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

func TestNormalizeSeparators(t *testing.T) {
	if got := normalizeSeparators(`C:\Media\Movies\Dune\Dune.mkv`); got != "C:/Media/Movies/Dune/Dune.mkv" {
		t.Fatalf("normalizeSeparators(windows) = %q", got)
	}
	// POSIX paths are unchanged.
	if got := normalizeSeparators("/mnt/media/x.mkv"); got != "/mnt/media/x.mkv" {
		t.Fatalf("normalizeSeparators(posix) = %q", got)
	}
	// A normalized Windows path then rewrites + dedupes on the Linux host.
	rw := []PathRewrite{{From: "C:/Media", To: "/mnt/media"}}
	if got := applyRewrites(normalizeSeparators(`C:\Media\ShowA\S01\E01.mkv`), rw); got != "/mnt/media/ShowA/S01/E01.mkv" {
		t.Fatalf("windows rewrite = %q", got)
	}
}
