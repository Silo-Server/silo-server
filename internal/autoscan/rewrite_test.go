package autoscan

import "testing"

func TestApplyRewrites(t *testing.T) {
	rw := []PathRewrite{{From: "/data/media", To: "/mnt/media"}}
	cases := []struct{ in, want string }{
		{"/data/media/Movies/Dune/Dune.mkv", "/mnt/media/Movies/Dune/Dune.mkv"},
		{"/other/path/file.mkv", "/other/path/file.mkv"}, // no-match pass-through
	}
	for _, tc := range cases {
		if got := applyRewrites(tc.in, rw); got != tc.want {
			t.Fatalf("applyRewrites(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Exact match (no trailing segment) rewrites the whole path.
	if got := applyRewrites("/data/media", rw); got != "/mnt/media" {
		t.Fatalf("exact match: got %q want %q", got, "/mnt/media")
	}

	// nil/empty rewrites pass through unchanged.
	if got := applyRewrites("/data/media/x", nil); got != "/data/media/x" {
		t.Fatalf("nil rewrites: got %q", got)
	}

	// Segment-boundary matching: a sibling dir sharing the prefix must NOT match.
	boundary := []PathRewrite{{From: "/data/media", To: "/mnt"}}
	if got := applyRewrites("/data/media2/x", boundary); got != "/data/media2/x" {
		t.Fatalf("boundary: /data/media2/x must not rewrite, got %q", got)
	}
}

// TestApplyRewritesMostSpecificWins verifies the longest matching From wins
// regardless of slice ordering: a broad rule must not shadow a nested one.
func TestApplyRewritesMostSpecificWins(t *testing.T) {
	// Broad rule listed FIRST: a first-match strategy would (wrongly) pick /data
	// and yield "/A/media/x". Most-specific must pick /data/media -> "/B/x".
	broadFirst := []PathRewrite{
		{From: "/data", To: "/A"},
		{From: "/data/media", To: "/B"},
	}
	if got := applyRewrites("/data/media/x", broadFirst); got != "/B/x" {
		t.Fatalf("broad-first: got %q want %q", got, "/B/x")
	}

	// Same rules, specific listed first: result must be identical (order-independent).
	specificFirst := []PathRewrite{
		{From: "/data/media", To: "/B"},
		{From: "/data", To: "/A"},
	}
	if got := applyRewrites("/data/media/x", specificFirst); got != "/B/x" {
		t.Fatalf("specific-first: got %q want %q", got, "/B/x")
	}

	// A path under the broad rule but NOT the specific one still uses the broad rule.
	if got := applyRewrites("/data/other/x", broadFirst); got != "/A/other/x" {
		t.Fatalf("broad fallthrough: got %q want %q", got, "/A/other/x")
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
	// A normalized Windows path then rewrites on the Linux host.
	rw := []PathRewrite{{From: "C:/Media", To: "/mnt/media"}}
	if got := applyRewrites(normalizeSeparators(`C:\Media\ShowA\S01\E01.mkv`), rw); got != "/mnt/media/ShowA/S01/E01.mkv" {
		t.Fatalf("windows rewrite = %q", got)
	}
}
