package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// staticVersionList answers the item's selectable version rows from a fixed set,
// the way the detail service's projection does.
type staticVersionList struct {
	ids map[int]struct{}
	err error
}

func (s staticVersionList) SelectableVersionFileIDs(context.Context, string, catalog.AccessFilter) (map[int]struct{}, error) {
	return s.ids, s.err
}

func versionIDs(ids ...int) map[int]struct{} {
	set := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

// A plan's effective row must be one of the item's selectable version rows. The
// measured failure was a plan whose requested row was 304999 and whose effective
// row was 1320729, a row the item's 452-row version list did not carry: progress
// recorded one row and watch state the other, so the client could never
// reconcile "what is playing" with "what I picked".
func TestEffectiveRowMustBeInTheItemVersionList(t *testing.T) {
	const (
		neutral   = "virtual://movie/tt-version-list"
		requested = neutral + "?result=selected"
	)
	selected := rotationTrackFixtureRow(100, 20_000_000_000, requested, "Movie.2024.2160p.SELECTED", 2, 1)
	selected.ProbeUpdatedAt = timePtr(time.Now().Add(-time.Hour))
	// A provider-neutral listing row: no own candidate, no probe stamp, no
	// tracks. Binding it to the row that owns the served candidate is ordinary
	// auto selection, not a substituted version.
	neutralRow := rotationTrackFixtureRow(101, 0, neutral, "", 0, 0)
	neutralRow.FileSize = 0

	newHandler := func(list PlaybackVersionListLookup) *PlaybackHandler {
		h := NewPlaybackHandler(playback.NewSessionManager(0, 0), nil)
		h.VersionList = list
		return h
	}
	ctx := context.Background()
	unlisted := &models.MediaFile{ID: 1320729, ContentID: selected.ContentID}
	listedOther := &models.MediaFile{ID: 150, ContentID: selected.ContentID}

	cases := []struct {
		name      string
		list      PlaybackVersionListLookup
		requested *models.MediaFile
		effective *models.MediaFile
		wantErr   bool
	}{
		{
			name:      "unlisted effective row is refused",
			list:      staticVersionList{ids: versionIDs(100, 150)},
			requested: selected,
			effective: unlisted,
			wantErr:   true,
		},
		{
			name:      "listed effective row is accepted",
			list:      staticVersionList{ids: versionIDs(100, 150)},
			requested: selected,
			effective: listedOther,
		},
		{
			name:      "the requested row is always its own answer",
			list:      staticVersionList{ids: versionIDs(150)},
			requested: selected,
			effective: selected,
		},
		{
			name:      "a neutral listing row is exempt",
			list:      staticVersionList{ids: versionIDs(150)},
			requested: neutralRow,
			effective: unlisted,
		},
		{
			name:      "an unwired list fails open",
			list:      nil,
			requested: selected,
			effective: unlisted,
		},
		{
			name:      "a lookup error fails open",
			list:      staticVersionList{err: errors.New("catalog read failed")},
			requested: selected,
			effective: unlisted,
		},
		{
			name:      "an empty list is unknown, not a verdict",
			list:      staticVersionList{ids: versionIDs()},
			requested: selected,
			effective: unlisted,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := newHandler(tc.list).confineEffectiveVersionV3(ctx, tc.requested, tc.effective, catalog.AccessFilter{})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, errEffectiveFileNotInVersionList) {
				t.Fatalf("error = %v, want errEffectiveFileNotInVersionList", err)
			}
		})
	}
}

// An unlisted effective row is refused like any other unusable candidate: the
// start excludes the candidate that produced it and tries the next one. The
// measured production case had a 452-row version list; the resolver served a
// row that was not in it, and the plan was committed anyway.
func TestStartTriesTheNextCandidateWhenTheResolvedRowIsUnlisted(t *testing.T) {
	const (
		neutral      = "virtual://movie/tt-unlisted-effective"
		requestedURI = neutral + "?result=selected"
		foreignURI   = neutral + "?result=foreign"
		alternateURI = neutral + "?result=alternate"
	)
	scannerResetVirtualProbeState(t, foreignURI, alternateURI)

	requested := rotationTrackFixtureRow(300, 20_000_000_000, requestedURI, "Movie.2024.2160p.SELECTED", 2, 1)
	requested.ProbeUpdatedAt = timePtr(time.Now().Add(-time.Hour))
	// The unlisted row the resolver lands on, and the listed alternate the walk
	// must reach instead.
	foreign := rotationTrackFixtureRow(301, 12_000_000_000, foreignURI, "Movie.2024.1080p.FOREIGN", 2, 1)
	alternate := rotationTrackFixtureRow(302, 20_000_000_000, alternateURI, "Movie.2024.2160p.ALTERNATE", 2, 1)
	rows := map[string]*models.MediaFile{foreignURI: foreign, alternateURI: alternate}
	candidates := []VirtualPlaybackStream{
		{ID: "foreign", URI: foreignURI, FileSize: 12_000_000_000, OwnerInstallationID: 5},
		{ID: "alternate", URI: alternateURI, FileSize: 20_000_000_000, OwnerInstallationID: 5},
	}
	fx := newRotationTrackFixture(t, requested, rows, candidates, func(path string, f *models.MediaFile) error {
		f.VideoTracks = []models.VideoTrack{{Codec: "hevc", Width: 3840, Height: 2160}}
		f.CodecVideo, f.CodecAudio, f.Resolution, f.Container = "hevc", "eac3", "2160p", "mkv"
		f.Duration = 90 * 60
		f.AudioTracks, f.SubtitleTracks = rotationTrackFixtureTracks(2, 1)
		if path == foreignURI {
			f.ProbedFormatSize = 12_000_000_000
			return nil
		}
		f.ProbedFormatSize = 20_000_000_000
		return nil
	})
	fx.handler.VersionList = staticVersionList{ids: versionIDs(requested.ID, alternate.ID)}

	resolved, err := fx.handler.resolveVirtualPlaybackSource(
		httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil),
		fx.file, "profile-1", false, nil, "", "auto", 0, true,
	)
	if err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if resolved.File == nil || resolved.File.ID != foreign.ID {
		t.Fatalf("resolved effective row = %+v, want the unlisted row %d", resolved.File, foreign.ID)
	}

	r := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", nil)
	start := playback.StartRequestV3{QualityPreference: "auto"}
	confined, err := fx.handler.confineStartEffectiveVersionV3(r, r, fx.file, resolved, start, "profile-1", false, 0)
	if err != nil {
		t.Fatalf("confine error: %v", err)
	}
	if confined.File == nil {
		t.Fatal("confine returned no effective row")
	}
	// The retry must land on a row the item lists — the requested row itself or
	// the next candidate — and never on the row it just refused.
	if _, listed := versionIDs(requested.ID, alternate.ID)[confined.File.ID]; !listed {
		t.Fatalf("confined effective row = %d, want the requested row %d or the listed alternate %d",
			confined.File.ID, requested.ID, alternate.ID)
	}
	if confined.File.ID == foreign.ID || strings.Contains(confined.URI, "foreign") {
		t.Fatalf("confine served the refused candidate again: row %d uri %q", confined.File.ID, confined.URI)
	}
}

// An explicit pick whose first resolve bound a catalog row outside the item's
// version list must serve the row the viewer picked, never the sibling that owned
// the candidate URI. The measured production case picked 1975761 (720p H.264) and
// the resolve bound 1975715 (a duplicate row the item's 190-row list did not
// carry); the confinement re-walk then rotated to the item's default HDR row and
// terminalised on that file's `hdr_transcode_unsupported`, which is a verdict about
// a release the viewer never chose.
func TestExplicitPickOutsideListBindingRebindsToTheRequestedRow(t *testing.T) {
	const (
		neutral      = "virtual://movie/tt-explicit-rebind"
		requestedURI = neutral + "?result=selected"
		foreignURI   = neutral + "?result=duplicate"
	)
	scannerResetVirtualProbeState(t, requestedURI, foreignURI)

	requested := rotationTrackFixtureRow(400, 8_000_000_000, requestedURI, "Movie.2024.720p.SELECTED", 2, 1)
	requested.ProbeUpdatedAt = timePtr(time.Now().Add(-time.Hour))
	duplicate := rotationTrackFixtureRow(401, 20_000_000_000, foreignURI, "Movie.2024.2160p.HDR.DUPLICATE", 1, 0)
	rows := map[string]*models.MediaFile{requestedURI: requested, foreignURI: duplicate}
	candidates := []VirtualPlaybackStream{
		{ID: "duplicate", URI: foreignURI, FileSize: 20_000_000_000, OwnerInstallationID: 5},
		{ID: "selected", URI: requestedURI, FileSize: 8_000_000_000, OwnerInstallationID: 5},
	}
	fx := newRotationTrackFixture(t, requested, rows, candidates, func(string, *models.MediaFile) error {
		return nil
	})
	fx.handler.VersionList = staticVersionList{ids: versionIDs(requested.ID)}

	// The first resolve served the duplicate row, which the item does not list.
	outside := resolvedVirtualPlaybackSource{
		URI:     foreignURI,
		OwnerID: 5,
		File:    duplicate,
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)
	start := playback.StartRequestV3{QualityPreference: "auto", FileSelection: playback.FileSelectionExplicitV3}
	confined, err := fx.handler.confineStartEffectiveVersionV3(r, r, requested, outside, start, "profile-1", true, 0)
	if err != nil {
		t.Fatalf("confine error: %v", err)
	}
	if confined.File == nil {
		t.Fatal("confine returned no effective row")
	}
	if confined.File.ID != requested.ID {
		t.Fatalf("effective row = %d, want the selected row %d", confined.File.ID, requested.ID)
	}
	if confined.URI != requestedURI {
		t.Fatalf("confined URI = %q, want the selected row's own candidate %q", confined.URI, requestedURI)
	}
}

// When the selected row's own release cannot be resolved either, an explicit pick
// answers the selected version's own retryable failure. It must not plan a listed
// sibling (the silent-substitution shape) and must not carry the sibling's or the
// version list's verdict into a terminal about the picked release.
func TestExplicitPickOutsideListBindingFailsAboutTheRequestedRow(t *testing.T) {
	const (
		neutral      = "virtual://movie/tt-explicit-own-failure"
		requestedURI = neutral + "?result=selected"
		foreignURI   = neutral + "?result=sibling"
	)
	scannerResetVirtualProbeState(t, requestedURI, foreignURI)

	requested := rotationTrackFixtureRow(410, 8_000_000_000, requestedURI, "Movie.2024.720p.SELECTED", 2, 1)
	requested.ProbeUpdatedAt = timePtr(time.Now().Add(-time.Hour))
	sibling := rotationTrackFixtureRow(411, 12_000_000_000, foreignURI, "Movie.2024.1080p.SIBLING", 2, 1)
	rows := map[string]*models.MediaFile{foreignURI: sibling}
	fx := newRotationTrackFixture(t, requested, rows, nil, func(string, *models.MediaFile) error {
		return nil
	})
	// Every provider resolve fails, so re-resolving the selected row cannot succeed
	// and the only other answer on the table is a sibling.
	fx.handler.VirtualMediaDetailedResolver = nil
	fx.handler.VirtualPlaybackResolver = VirtualPlaybackResolverFunc(
		func(context.Context, string, int, string, int) (string, error) {
			return "", errors.New("provider listing failed")
		},
	)
	fx.handler.VersionList = staticVersionList{ids: versionIDs(requested.ID, sibling.ID)}

	r := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)
	start := playback.StartRequestV3{QualityPreference: "auto", FileSelection: playback.FileSelectionExplicitV3}
	// The sibling is listed, so the old guard would have accepted it and planned a
	// different release under the viewer's pick.
	confined, err := fx.handler.confineStartEffectiveVersionV3(r, r, requested, resolvedVirtualPlaybackSource{
		URI: foreignURI, OwnerID: 5, File: sibling,
	}, start, "profile-1", true, 0)
	if err == nil {
		t.Fatalf("confine planned the sibling row %d for an explicit pick of %d", confined.File.ID, requested.ID)
	}
	if !errors.Is(err, errSelectedReleaseNotServableV3) {
		t.Fatalf("error = %v, want errSelectedReleaseNotServableV3", err)
	}
	if errors.Is(err, errEffectiveFileNotInVersionList) {
		t.Fatalf("a different-release binding was reported as a version-list fault: %v", err)
	}
	terminal := virtualStartUnresolvedTerminalV3(err)
	if terminal.Terminal == nil {
		t.Fatal("no terminal for the selected row's own failure")
	}
	if !terminal.Terminal.Retryable {
		t.Fatal("the selected row's own failure must be retryable")
	}
	if !strings.Contains(terminal.Terminal.Message, "selected version") {
		t.Fatalf("terminal message %q does not name the selected version", terminal.Terminal.Message)
	}
	if virtualCandidateConfirmedDead(requested, err) {
		t.Fatal("the selected row was indicted by a different release's resolve")
	}
}

// When no candidate resolves inside the version list the start must answer a
// retryable terminal that names the version list, never a plan for the unlisted
// row. The failure must also stay out of the dead-pin classifier: the requested
// release is healthy, only the resolver's target was not a row the client could
// see.
func TestUnlistedEffectiveRowTerminalsWithTheVersionListHint(t *testing.T) {
	terminal := virtualStartUnresolvedTerminalV3(fmt.Errorf("%w: refused effective row 1320729", errEffectiveFileNotInVersionList))
	if terminal.Terminal == nil {
		t.Fatal("no terminal for an unlisted effective row")
	}
	if !terminal.Terminal.Retryable {
		t.Fatal("an unlisted effective row must be a retryable terminal")
	}
	if terminal.Terminal.Reason != virtualSourceUnavailableReasonV3 {
		t.Fatalf("terminal reason = %q, want %q", terminal.Terminal.Reason, virtualSourceUnavailableReasonV3)
	}
	if !strings.Contains(terminal.Terminal.Message, "version list") {
		t.Fatalf("terminal message %q does not name the version list", terminal.Terminal.Message)
	}
	if virtualCandidateConfirmedDead(&models.MediaFile{ID: 1}, fmt.Errorf("%w: refused effective row 2", errEffectiveFileNotInVersionList)) {
		t.Fatal("an unlisted effective row indicted the requested release as dead")
	}

	// A source mismatch keeps its own reason: the provider served another
	// release's bytes, which the version list calls source_mismatch.
	mismatch := virtualStartUnresolvedTerminalV3(fmt.Errorf("%w: candidate x served 1 byte for a declared 2-byte release", errVirtualSelectedReleaseUnverified))
	if mismatch.Terminal == nil || mismatch.Terminal.Reason != sourceMismatchReasonV3 {
		t.Fatalf("source-mismatch terminal = %+v, want reason %q", mismatch.Terminal, sourceMismatchReasonV3)
	}
	if !mismatch.Terminal.Retryable {
		t.Fatal("a source mismatch must be a retryable terminal")
	}
}

// One failed play attempt must not condemn a row unless the failure was the
// server's or the source's verdict. A client abort, a transport-temporary
// listing failure, and the two refusals above all leave the row eligible.
func TestOnlyAVerdictIndictsARow(t *testing.T) {
	file := &models.MediaFile{ID: 42, ContentID: "movie-indict"}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "client abort during the start request",
			err:  fmt.Errorf("virtual resolve: %w: no usable stream", context.Canceled),
		},
		{
			name: "client abort without a classification string",
			err:  fmt.Errorf("probe failed: %w", context.Canceled),
		},
		{
			name: "source mismatch",
			err:  fmt.Errorf("virtual playback provider returned no usable stream: %w", errVirtualSelectedReleaseUnverified),
		},
		{
			name: "unlisted effective row",
			err:  fmt.Errorf("%w: refused effective row 1320729", errEffectiveFileNotInVersionList),
		},
		{
			name: "transport-temporary outage",
			err:  errors.New("provider unavailable: no matching candidate"),
		},
		{
			name: "confirmed dead pin",
			err:  errors.New("provider returned no matching candidate"),
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := virtualCandidateConfirmedDead(file, tc.err); got != tc.want {
				t.Fatalf("virtualCandidateConfirmedDead(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A client abort must not reach the durable marker at all: the classifier is
// what every start-path stamp goes through.
func TestClientAbortDoesNotStampARowFailed(t *testing.T) {
	marks := 0
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0), nil)
	h.VirtualCandidateFailMarker = func(context.Context, int, string, *time.Time) error {
		marks++
		return nil
	}
	file := &models.MediaFile{ID: 42, ContentID: "movie-abort", FilePath: "virtual://movie/abort?result=pin"}
	h.stampStartVirtualCandidateFailed(context.Background(), file, fmt.Errorf("virtual resolve: %w: no usable stream", context.Canceled))
	if marks != 0 {
		t.Fatalf("a client abort stamped %d failed verdicts, want 0", marks)
	}
	h.stampStartVirtualCandidateFailed(context.Background(), file, fmt.Errorf("%w: refused effective row 9", errEffectiveFileNotInVersionList))
	if marks != 0 {
		t.Fatalf("an unlisted effective row stamped %d failed verdicts, want 0", marks)
	}
	h.stampStartVirtualCandidateFailed(context.Background(), file, errors.New("provider returned no matching candidate"))
	if marks != 1 {
		t.Fatalf("a confirmed-dead pin stamped %d failed verdicts, want 1", marks)
	}
}
