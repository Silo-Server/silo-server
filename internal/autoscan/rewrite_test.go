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

// TestApplyRewritesNormalizesStoredFrom verifies that a Windows-style / dup-slash
// stored From is normalized the same way coveredBy/normalizePath does, so a
// rewrite the suggester reports as "covered" actually matches at poll time.
func TestApplyRewritesNormalizesStoredFrom(t *testing.T) {
	// Backslash From: a Windows-hosted arr root stored verbatim.
	winFrom := []PathRewrite{{From: `D:\data\tv`, To: "/mnt/media/tv"}}
	if got := applyRewrites("D:/data/tv/Show/S01/E01.mkv", winFrom); got != "/mnt/media/tv/Show/S01/E01.mkv" {
		t.Fatalf("backslash From should match normalized path, got %q", got)
	}
	// coveredBy must agree with applyRewrites: the normalized root is covered.
	if !coveredBy("D:/data/tv", winFrom) {
		t.Fatalf("coveredBy should report the backslash From as covering the root")
	}

	// Dup-slash From collapses too.
	dupFrom := []PathRewrite{{From: "/data//tv/", To: "/mnt/media/tv"}}
	if got := applyRewrites("/data/tv/Show/E.mkv", dupFrom); got != "/mnt/media/tv/Show/E.mkv" {
		t.Fatalf("dup-slash From should match collapsed path, got %q", got)
	}
}

// TestApplyRewritesUNCPath verifies a Windows UNC root (\\NAS\Media\TV) from a
// Windows-hosted arr matches its rewrite rule. Separator swapping alone turns
// the incoming path into //NAS/... while the From normalizes to /NAS/..., so
// the prefix never matched — both sides must go through normalizePath.
func TestApplyRewritesUNCPath(t *testing.T) {
	incoming := `\\NAS\Media\TV\Show\S01\E01.mkv`
	for _, from := range []string{`\\NAS\Media\TV`, "//NAS/Media/TV", "/NAS/Media/TV"} {
		rw := []PathRewrite{{From: from, To: "/mnt/media/tv"}}
		if got := applyRewrites(incoming, rw); got != "/mnt/media/tv/Show/S01/E01.mkv" {
			t.Fatalf("UNC path with From=%q: got %q", from, got)
		}
	}

	// An unmatched UNC path still comes back normalized (collapsed slashes),
	// consistent with what the resolver sees for matched paths.
	if got := applyRewrites(incoming, nil); got != "/NAS/Media/TV/Show/S01/E01.mkv" {
		t.Fatalf("unmatched UNC path: got %q", got)
	}

	// A trailing-slash To must not produce a doubled separator at the join.
	slashTo := []PathRewrite{{From: `\\NAS\Media\TV`, To: "/mnt/media/tv/"}}
	if got := applyRewrites(incoming, slashTo); got != "/mnt/media/tv/Show/S01/E01.mkv" {
		t.Fatalf("trailing-slash To: got %q", got)
	}
}

// TestApplyRewritesPreservesTrailingSlash verifies a trailing separator on the
// incoming path survives normalization and rewriting. It is semantic for
// legacy-scope changes: filepath.Dir("/x/Show/") is the directory itself while
// filepath.Dir("/x/Show") is its parent, so dropping it would widen a targeted
// directory notification into a parent/library scan.
func TestApplyRewritesPreservesTrailingSlash(t *testing.T) {
	rw := []PathRewrite{{From: "/data/tv", To: "/mnt/media/tv"}}
	if got := applyRewrites("/data/tv/Show/", rw); got != "/mnt/media/tv/Show/" {
		t.Fatalf("rewritten dir: got %q", got)
	}
	// Unmatched paths keep it too.
	if got := applyRewrites("/other/Show/", rw); got != "/other/Show/" {
		t.Fatalf("unmatched dir: got %q", got)
	}
	// Windows separator form: trailing backslash counts as a trailing separator.
	unc := []PathRewrite{{From: `\\NAS\Media\TV`, To: "/mnt/media/tv"}}
	if got := applyRewrites(`\\NAS\Media\TV\Show\`, unc); got != "/mnt/media/tv/Show/" {
		t.Fatalf("UNC dir: got %q", got)
	}
	// Files without a trailing separator stay without one.
	if got := applyRewrites("/data/tv/Show/E01.mkv", rw); got != "/mnt/media/tv/Show/E01.mkv" {
		t.Fatalf("file: got %q", got)
	}
	// Bare root never doubles.
	if got := applyRewrites("/", nil); got != "/" {
		t.Fatalf("root: got %q", got)
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
