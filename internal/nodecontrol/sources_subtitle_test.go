package nodecontrol_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodecontrol/nodecontroltest"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// fakeBundles answers subtitle bundle requests with err, or with a file
// holding bundle, and records what it was asked for.
type fakeBundles struct {
	mu     sync.Mutex
	asked  []playback.SubtitleBundleSource
	err    error
	bundle string
}

func (b *fakeBundles) OpenSubtitleBundle(_ context.Context, src playback.SubtitleBundleSource) (*os.File, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asked = append(b.asked, src)
	if b.err != nil {
		return nil, b.err
	}
	f, err := os.CreateTemp("", "bundle-*")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(f.Name())
	if _, err := f.WriteString(b.bundle); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// newSubtitleFixture is a source fixture whose core serves subtitle bundles
// from bundles.
func newSubtitleFixture(t *testing.T, bundles *fakeBundles) *sourceFixture {
	t.Helper()
	f := newSourceFixture(t, 7)
	f.coreURL, f.key = nodecontroltest.SourceCore(t, f.nodeID, nodecontrol.Options{
		Grants: f.grants, Files: f.catalog, Subtitles: bundles,
		FFmpegPath: func() string { return "/opt/ffmpeg/ffmpeg" },
	})
	return f
}

func subtitlePath(track, codec string) string {
	return nodesource.PathSubtitle + "?" + url.Values{nodesource.QuerySubtitleTrack: {track}, nodesource.QuerySubtitleCodec: {codec}}.Encode()
}

// The core builds the bundle of the granted file's track from its own path,
// and serves it to the node the grant names.
func TestSubtitleServesTheGrantedFilesBundle(t *testing.T) {
	bundles := &fakeBundles{bundle: "tar bytes"}
	f := newSubtitleFixture(t, bundles)
	resp, body := f.do(t, http.MethodGet, subtitlePath("2", "SubRip"), nil, withGrant(nil, f.grant(t, f.nodeID)))
	if resp.StatusCode != http.StatusOK || string(body) != "tar bytes" || resp.Header.Get("Content-Type") != "application/x-tar" {
		t.Fatalf("bundle = %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if len(bundles.asked) != 1 {
		t.Fatalf("asked %d times", len(bundles.asked))
	}
	asked := bundles.asked[0]
	if asked.Path != f.path || asked.Track != 2 || asked.Codec != "subrip" || asked.FFmpegPath != "/opt/ffmpeg/ffmpeg" {
		t.Fatalf("asked for %+v", asked)
	}
	// The bundle must be built from the granted revision.
	info, err := os.Stat(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if asked.Matches == nil || !asked.Matches(info) {
		t.Fatal("the bundle is not bound to the granted revision")
	}
	if err := os.WriteFile(f.path, []byte("another revision"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, _ = os.Stat(f.path); asked.Matches(info) {
		t.Fatal("another revision matches the grant")
	}
}

func TestSubtitleAnswers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		path   string
		grant  func(f *sourceFixture) string
		status int
	}{
		{name: "building", err: playback.ErrSubtitleBundlePending, status: http.StatusAccepted},
		{name: "unbuildable", err: fmt.Errorf("%w: no such stream", playback.ErrSubtitleBundleFailed), status: http.StatusUnprocessableEntity},
		{name: "passing failure", err: context.DeadlineExceeded, status: http.StatusServiceUnavailable},
		{name: "changed under the build", err: fmt.Errorf("subtitle bundle: %w", nodesource.ErrSourceChanged), status: http.StatusPreconditionFailed},
		{name: "no track", path: nodesource.PathSubtitle + "?codec=ass", status: http.StatusBadRequest},
		{name: "negative track", path: subtitlePath("-1", "ass"), status: http.StatusBadRequest},
		{name: "odd codec", path: subtitlePath("0", "../ass"), status: http.StatusBadRequest},
		{name: "another node's grant", grant: func(f *sourceFixture) string { return f.grant(t, f.nodeID+1) }, status: http.StatusForbidden},
		{name: "no grant", grant: func(*sourceFixture) string { return "" }, status: http.StatusForbidden},
	} {
		bundles := &fakeBundles{err: tc.err}
		f := newSubtitleFixture(t, bundles)
		path := tc.path
		if path == "" {
			path = subtitlePath("0", "ass")
		}
		grant := f.grant(t, f.nodeID)
		if tc.grant != nil {
			grant = tc.grant(f)
		}
		if resp, body := f.do(t, http.MethodGet, path, nil, withGrant(nil, grant)); resp.StatusCode != tc.status {
			t.Errorf("%s: %d %s, want %d", tc.name, resp.StatusCode, body, tc.status)
		}
	}
}

// A changed or removed source is refused as a source read refuses it, before
// any bundle is built.
func TestSubtitleChecksTheGrantedRevision(t *testing.T) {
	bundles := &fakeBundles{bundle: "tar bytes"}
	f := newSubtitleFixture(t, bundles)
	grant := f.grant(t, f.nodeID)
	if err := os.WriteFile(f.path, []byte("another revision"), 0o600); err != nil {
		t.Fatal(err)
	}
	if resp, _ := f.do(t, http.MethodGet, subtitlePath("0", "ass"), nil, withGrant(nil, grant)); resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("changed source = %d", resp.StatusCode)
	}
	if err := os.Remove(f.path); err != nil {
		t.Fatal(err)
	}
	if resp, _ := f.do(t, http.MethodGet, subtitlePath("0", "ass"), nil, withGrant(nil, grant)); resp.StatusCode != http.StatusGone {
		t.Fatalf("removed source = %d", resp.StatusCode)
	}
	if len(bundles.asked) != 0 {
		t.Fatalf("bundles built for a refused source: %+v", bundles.asked)
	}
}

// Without a subtitle cache the core cannot build bundles.
func TestSubtitleWithoutBundles(t *testing.T) {
	f := newSourceFixture(t, 7)
	if resp, _ := f.do(t, http.MethodGet, subtitlePath("0", "ass"), nil, withGrant(nil, f.grant(t, f.nodeID))); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no bundles = %d", resp.StatusCode)
	}
}
