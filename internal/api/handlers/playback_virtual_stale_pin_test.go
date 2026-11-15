package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// stalePinRow builds an identity-less virtual row. FileSize/CodecVideo/Duration
// are the fingerprint tiers; probe evidence is intentionally absent so the P0
// fast path cannot serve the row.
func stalePinRow(id int, contentID, neutralURI, resultID string) *models.MediaFile {
	return &models.MediaFile{
		ID:                         id,
		ContentID:                  contentID,
		FilePath:                   neutralURI + "?result=" + resultID,
		Container:                  "mkv",
		CodecVideo:                 "h264",
		CodecAudio:                 "aac",
		Resolution:                 "1080p",
		VirtualOwnerInstallationID: 5,
		FileSize:                   8_500_000_000,
		Duration:                   7200,
	}
}

// stalePinLiveStream is a live listing record for the same content under a new
// result id. FileSize and codec are overridable so a test can force a mismatch.
func stalePinLiveStream(neutralURI, resultID string, fileSize int64, videoCodec string) VirtualPlaybackStream {
	return VirtualPlaybackStream{
		ID: resultID, URI: neutralURI + "?result=" + resultID,
		Resolution: "1080p", CodecVideo: videoCodec, CodecAudio: "aac",
		Container: "mkv", FileSize: fileSize,
	}
}

// stalePinProbedTrack is the real ffprobe evidence the recovery's probe must
// return: a complete track plus a measured duration that can be tuned to match
// or miss the row's stored runtime.
func stalePinProbedTrack(duration int) *models.MediaFile {
	return &models.MediaFile{
		Duration:  duration,
		Container: "mkv", CodecVideo: "h264", CodecAudio: "aac", Resolution: "1080p", Bitrate: 8000,
		VideoTracks: []models.VideoTrack{{
			Codec: "h264", Width: 1920, Height: 1080, FrameRate: "24000/1001", BitDepth: 8, Bitrate: 8000,
		}},
		AudioTracks: []models.AudioTrack{{Codec: "aac", Channels: 2}},
	}
}

// stalePinRecoveryHandler wires the recovery's collaborators: the lister answers
// a fixed live set, the detailed resolver records every call and resolves the
// matched candidate (or substitutes/fails per the test), and the prober records
// every probe and returns a measured duration (or fails). resolveCalls counts
// handler-level resolves (must stay 1); listCalls counts listings; saveCalls
// counts evidence writes.
type stalePinRecoveryHandlerOpts struct {
	live          []VirtualPlaybackStream
	resolveErr    error
	substituteURI string
	probeDuration int
	probeErr      error
	noProber      bool
	// resolverURI, when set, is returned verbatim as the resolved URI (and the
	// CandidateID is derived from it) instead of echoing the requested URI. It
	// lets a test return a provider-neutral URI or a cross-content URI.
	resolverURI string
	// resolverCandidateID, when set, overrides the CandidateID the resolver
	// returns, so a test can return a CandidateID inconsistent with the URI.
	resolverCandidateID string
	// resolverOwnerID, when set, is returned as the resolver's OwnerID.
	resolverOwnerID int
	// pathOwnerRow, when set, is returned by the handler's exact-path lookup, so
	// the recovery can persist replacement evidence against an existing owner.
	pathOwnerRow      *models.MediaFile
	pathOwnerResolves bool
}

type stalePinRecoveryFixture struct {
	handler      *PlaybackHandler
	resolveCalls *atomic.Int32
	listCalls    *atomic.Int32
	saveCalls    *atomic.Int32
	// saved records every write admitted by the evidence saver; savedArgsMu
	// guards it for the interleaving test, which drives two recoveries from
	// separate goroutines.
	savedMu   sync.Mutex
	savedArgs []models.VirtualFilePersistArgs
}

func (f *stalePinRecoveryFixture) saved() []models.VirtualFilePersistArgs {
	f.savedMu.Lock()
	defer f.savedMu.Unlock()
	return append([]models.VirtualFilePersistArgs(nil), f.savedArgs...)
}

func newStalePinRecoveryFixture(pinnedURI string, opts stalePinRecoveryHandlerOpts) *stalePinRecoveryFixture {
	var resolveCalls, listCalls, saveCalls atomic.Int32
	fixture := &stalePinRecoveryFixture{}
	h := &PlaybackHandler{
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
			return "", errors.New("simple resolver must not be used when the detailed resolver is set")
		}),
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			listCalls.Add(1)
			return opts.live, nil
		}),
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			resolveCalls.Add(1)
			if opts.resolveErr != nil {
				return ResolvedVirtualMedia{}, opts.resolveErr
			}
			resolvedURI := uri
			if opts.substituteURI != "" {
				resolvedURI = opts.substituteURI
			}
			if opts.resolverURI != "" {
				resolvedURI = opts.resolverURI
			}
			candidateID := virtualResultCandidateID(resolvedURI)
			if opts.resolverCandidateID != "" {
				candidateID = opts.resolverCandidateID
			}
			resolved := ResolvedVirtualMedia{URL: "http://127.0.0.1:8080/stream?uri=" + resolvedURI, URI: resolvedURI, CandidateID: candidateID}
			if opts.resolverOwnerID != 0 {
				resolved.OwnerID = opts.resolverOwnerID
			}
			return resolved, nil
		}),
		VirtualFileSaver: func(_ context.Context, args models.VirtualFilePersistArgs) (int64, error) {
			saveCalls.Add(1)
			fixture.savedMu.Lock()
			fixture.savedArgs = append(fixture.savedArgs, args)
			fixture.savedMu.Unlock()
			return 1, nil
		},
	}
	if opts.pathOwnerRow != nil || opts.pathOwnerResolves {
		owner := opts.pathOwnerRow
		h.fileResolver = stalePinPathResolver{
			getByID: func(int) (*models.MediaFile, error) { return owner, nil },
			getByPath: func(string) (*models.MediaFile, error) {
				if opts.pathOwnerResolves {
					return owner, nil
				}
				return nil, ErrVirtualCandidateNotFound
			},
		}
	}
	fixture.handler = h
	fixture.resolveCalls = &resolveCalls
	fixture.listCalls = &listCalls
	fixture.saveCalls = &saveCalls
	if !opts.noProber {
		h.VirtualPlaybackSourceProber = func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			if opts.probeErr != nil {
				return nil, opts.probeErr
			}
			probed := stalePinProbedTrack(opts.probeDuration)
			probed.ID = f.ID
			probed.FilePath = f.FilePath
			return probed, nil
		}
	}
	return fixture
}

// stalePinPathResolver is the minimal FilePathResolver the recovery needs: the
// exact-path lookup behind the owner check. Only GetByID and GetByPath are
// reached by the recovery; both are supplied per test.
type stalePinPathResolver struct {
	getByID   func(int) (*models.MediaFile, error)
	getByPath func(string) (*models.MediaFile, error)
}

func (r stalePinPathResolver) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	return r.getByID(id)
}

func (r stalePinPathResolver) GetByPath(_ context.Context, path string) (*models.MediaFile, error) {
	return r.getByPath(path)
}

func stalePinRecoveryRequest() *http.Request {
	// Declare the cold-start intent the start path threads: the recovery refuses
	// a session-bound resolve, and the conservative default is bound, so a unit
	// test must opt in exactly as the start path does.
	req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)
	return req.WithContext(withVirtualSessionBindingV3(req.Context(), false))
}

func stalePinSessionBoundRequest() *http.Request {
	return httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)
}

func (f *stalePinRecoveryFixture) stickyKey(row *models.MediaFile) string {
	return bestResultCacheKey(row.ContentID, virtualPlaybackNeutralKey(row.FilePath), row.VirtualOwnerInstallationID)
}

// (a) A dead pin absent from a NON-EMPTY listing, on an identity-less row, with a
// live fingerprint match and a probe that measures a matching duration recovers:
// one handler-level resolve, one probe, in memory only.
func TestStalePinRecoveryRecoversFingerprintMatch(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-recover"
	pinnedURI := neutral + "?result=STALE"
	row := stalePinRow(1, "movie-stale-recover", neutral, "STALE")
	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")}
	fx := newStalePinRecoveryFixture(pinnedURI, stalePinRecoveryHandlerOpts{live: live, probeDuration: row.Duration})

	recovered, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if !ok {
		t.Fatal("a fingerprint-matched live candidate must recover the pin")
	}
	if recovered.File == nil || recovered.URI != neutral+"?result=NEW" {
		t.Fatalf("recovered = %#v, want the fingerprint-matched live candidate", recovered)
	}
	if got := virtualResultCandidateID(recovered.URI); got != "NEW" {
		t.Fatalf("recovered candidate = %q, want NEW", got)
	}
	if got := fx.resolveCalls.Load(); got != 1 {
		t.Fatalf("handler-level resolves = %d, want exactly 1 (single attempt, no sibling iteration)", got)
	}
	// One listing only: the general resolver's stale-source fallback would have
	// paid a second listing, proving the recovery did not invoke it.
	if got := fx.listCalls.Load(); got != 1 {
		t.Fatalf("provider listings = %d, want exactly 1 (no fallback invocation)", got)
	}
	// The sticky pin is set synchronously only after validation passes, so its
	// presence is the deterministic proof the recovery authorized the candidate.
	if got := fx.handler.peekVirtualSticky(fx.stickyKey(row)); got != neutral+"?result=NEW" {
		t.Fatalf("sticky pin = %q, want the matched candidate after a successful recovery", got)
	}
	// The row's durable pin is never rewritten: only the returned copy changed.
	if got := virtualResultCandidateID(row.FilePath); got != "STALE" {
		t.Fatalf("catalog row pin = %q, want the durable STALE unchanged", got)
	}
}

// (a2) A single attempt is enforced even when the matched candidate's resolve
// yields a substitution: the recovery rejects the different result id and never
// iterates to another sibling or falls back.
func TestStalePinRecoverySingleAttemptRejectsSubstitution(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-subst"
	pinnedURI := neutral + "?result=STALE"
	row := stalePinRow(2, "movie-stale-subst", neutral, "STALE")
	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")}
	fx := newStalePinRecoveryFixture(pinnedURI, stalePinRecoveryHandlerOpts{
		live:          live,
		probeDuration: row.Duration,
		// The detailed resolver substituted a different sibling.
		substituteURI: neutral + "?result=OTHER",
	})

	_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if ok {
		t.Fatal("a substituted candidate must not recover the pin")
	}
	if got := fx.resolveCalls.Load(); got != 1 {
		t.Fatalf("handler-level resolves = %d, want exactly 1 (no iteration after a substitution)", got)
	}
	if got := fx.listCalls.Load(); got != 1 {
		t.Fatalf("provider listings = %d, want exactly 1 (no stale-source fallback)", got)
	}
	if got := fx.saveCalls.Load(); got != 0 {
		t.Fatalf("evidence writes = %d, want 0 when the recovery is rejected", got)
	}
}

// (b) An EMPTY listing is a transient blackout, not a renumber: no recovery.
func TestStalePinRecoveryEmptyListingPreservesTerminal(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-empty"
	row := stalePinRow(3, "movie-stale-empty", neutral, "STALE")
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{probeDuration: row.Duration})

	_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if ok {
		t.Fatal("an empty listing must not recover")
	}
	if got := fx.resolveCalls.Load(); got != 0 {
		t.Fatalf("resolves = %d, want 0 for an empty listing", got)
	}
}

// (c) A non-empty listing with NO fingerprint match (size outside tolerance)
// does not recover.
func TestStalePinRecoveryNoFingerprintMatchPreservesTerminal(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-nomatch"
	row := stalePinRow(4, "movie-stale-nomatch", neutral, "STALE")
	// 30% larger: outside the 5% size tolerance.
	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "OTHER", row.FileSize*13/10, "h264")}
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{live: live, probeDuration: row.Duration})

	_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if ok {
		t.Fatal("a fingerprint miss must not recover")
	}
	if got := fx.resolveCalls.Load(); got != 0 {
		t.Fatalf("resolves = %d, want 0 without a fingerprint match", got)
	}
}

// (c2) A non-empty listing with the right size but a mismatched video codec does
// not recover.
func TestStalePinRecoveryCodecMismatchPreservesTerminal(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-codec"
	row := stalePinRow(5, "movie-stale-codec", neutral, "STALE")
	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "HEVC", row.FileSize, "hevc")}
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{live: live, probeDuration: row.Duration})

	_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if ok {
		t.Fatal("a codec mismatch must not recover")
	}
	if got := fx.resolveCalls.Load(); got != 0 {
		t.Fatalf("resolves = %d, want 0", got)
	}
}

// (d) The recovery never adopts across a different content_id: a live candidate
// under another neutral path is dropped by the same-content filter before
// matching, so no resolve is attempted for it.
func TestStalePinRecoveryNoCrossContentAdoption(t *testing.T) {
	const (
		neutral = "virtual://movie/tt-stale-cross"
		foreign = "virtual://movie/tt-some-other-content"
	)
	row := stalePinRow(6, "movie-stale-cross", neutral, "STALE")
	// Same size and codec, but a different content path.
	live := []VirtualPlaybackStream{stalePinLiveStream(foreign, "FOREIGN", row.FileSize, "h264")}
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{live: live, probeDuration: row.Duration})

	_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if ok {
		t.Fatal("a foreign-content candidate must not recover the pin")
	}
	if got := fx.resolveCalls.Load(); got != 0 {
		t.Fatalf("resolves = %d, want 0 (the foreign candidate is never resolved)", got)
	}
}

// (e) A fingerprint match whose resolve fails is a single attempt, not a loop:
// exactly one resolve is tried, and no evidence is persisted.
func TestStalePinRecoveryResolveFailureIsSingleAttempt(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-retry"
	row := stalePinRow(7, "movie-stale-retry", neutral, "STALE")
	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")}
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{
		live:          live,
		resolveErr:    errors.New("resolve failed"),
		probeDuration: row.Duration,
	})

	_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if ok {
		t.Fatal("a failing recovery resolve must not report success")
	}
	if got := fx.resolveCalls.Load(); got != 1 {
		t.Fatalf("resolves = %d, want exactly 1 (bounded single attempt)", got)
	}
	if got := fx.listCalls.Load(); got != 1 {
		t.Fatalf("listings = %d, want exactly 1 (no fallback after a failed resolve)", got)
	}
	if got := fx.saveCalls.Load(); got != 0 {
		t.Fatalf("evidence writes = %d, want 0 on a failed resolve", got)
	}
}

// (f) A row WITH durable identity is left to the resolver's same-release rematch:
// the recovery does not fire and no listing or resolve is attempted.
func TestStalePinRecoverySkippedForIdentityCarryingRow(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-identity"
	row := stalePinRow(8, "movie-stale-identity", neutral, "STALE")
	row.ProviderVideoHash = "hash-a" // durable identity present
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{
		live:          []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")},
		probeDuration: row.Duration,
	})

	_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if ok {
		t.Fatal("an identity-carrying row must not be recovered by the identity-less path")
	}
	if got := fx.resolveCalls.Load(); got != 0 {
		t.Fatalf("resolves = %d, want 0 (identity-carrying rows use the resolver rematch)", got)
	}
	if got := fx.listCalls.Load(); got != 0 {
		t.Fatalf("listings = %d, want 0 (the recovery must not even list)", got)
	}
}

// (g) The kill switch disables the recovery entirely.
func TestStalePinRecoveryKillSwitchDisables(t *testing.T) {
	prev := virtualStalePinRecoveryEnabled
	virtualStalePinRecoveryEnabled = false
	t.Cleanup(func() { virtualStalePinRecoveryEnabled = prev })

	const neutral = "virtual://movie/tt-stale-kill"
	row := stalePinRow(9, "movie-stale-kill", neutral, "STALE")
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{
		live:          []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")},
		probeDuration: row.Duration,
	})

	_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if ok {
		t.Fatal("the kill switch must disable recovery")
	}
	if got := fx.resolveCalls.Load(); got != 0 {
		t.Fatalf("resolves = %d, want 0 with the kill switch off", got)
	}
	if got := fx.listCalls.Load(); got != 0 {
		t.Fatalf("listings = %d, want 0 with the kill switch off", got)
	}
}

// (3a) A NO-PROBER handler is a no-match: with no probe there is no measured
// duration, so the recovery must not compare against the copied row's stale
// runtime, and it must not persist or pin.
func TestStalePinRecoveryNoProberIsNoMatch(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-noprober"
	row := stalePinRow(10, "movie-stale-noprober", neutral, "STALE")
	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")}
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{live: live, noProber: true})

	_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if ok {
		t.Fatal("a no-prober handler must not recover: there is no measured duration")
	}
	if got := fx.saveCalls.Load(); got != 0 {
		t.Fatalf("evidence writes = %d, want 0 without a probe", got)
	}
	if got := fx.handler.peekVirtualSticky(fx.stickyKey(row)); got != "" {
		t.Fatalf("sticky pin = %q, want none without a probe", got)
	}
}

// (3b) A failing probe is a no-match: the stale copied duration must not be used
// to authorize the recovery.
func TestStalePinRecoveryProbeFailureIsNoMatch(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-probefail"
	row := stalePinRow(11, "movie-stale-probefail", neutral, "STALE")
	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")}
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{
		live:     live,
		probeErr: errors.New("probe refused the stream"),
	})

	_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if ok {
		t.Fatal("a failed probe must not recover")
	}
	if got := fx.saveCalls.Load(); got != 0 {
		t.Fatalf("evidence writes = %d, want 0 on a failed probe", got)
	}
	if got := fx.handler.peekVirtualSticky(fx.stickyKey(row)); got != "" {
		t.Fatalf("sticky pin = %q, want none on a failed probe", got)
	}
}

// (3c) The measured duration is used: a probe that measures a runtime outside
// the tolerance rejects the recovery, and the rejection happens BEFORE any
// evidence persist or sticky-pin update.
func TestStalePinRecoveryDurationMismatchLeavesNoSideEffects(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-duration"
	row := stalePinRow(12, "movie-stale-duration", neutral, "STALE")
	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")}
	// 300s against a 7200s row: far outside the 2% duration tolerance.
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{live: live, probeDuration: 300})

	_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if ok {
		t.Fatal("a measured duration mismatch must not recover")
	}
	if got := fx.saveCalls.Load(); got != 0 {
		t.Fatalf("evidence writes = %d, want 0: validation must precede persistence", got)
	}
	if got := fx.handler.peekVirtualSticky(fx.stickyKey(row)); got != "" {
		t.Fatalf("sticky pin = %q, want none: validation must precede the pin update", got)
	}
}

// (3d) The measured duration is used: a probe that measures a matching runtime
// (even when the copied row carried a different one) recovers, proving the
// comparison is against the probe result rather than the copied value.
func TestStalePinRecoveryUsesMeasuredDuration(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-measured"
	row := stalePinRow(13, "movie-stale-measured", neutral, "STALE")
	// The copied row carries a stale runtime; the probe measures the true one.
	row.Duration = 10
	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")}
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{live: live, probeDuration: row.Duration})

	recovered, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if !ok {
		t.Fatal("a probe measuring a matching duration must recover")
	}
	if recovered.File == nil || recovered.File.Duration != row.Duration {
		t.Fatalf("recovered file duration = %v, want the measured %d", recovered.File, row.Duration)
	}
	if got := fx.handler.peekVirtualSticky(fx.stickyKey(row)); got != neutral+"?result=NEW" {
		t.Fatalf("sticky pin = %q, want the matched candidate after a successful recovery", got)
	}
}

// (4) A slow recovery yields to healthy alternates: the recovery's sub-budget
// expires and the version-fallback walk still resolves a healthy alternate
// instead of the recovery consuming the whole parent budget.
func TestStalePinRecoverySlowYieldsToHealthyAlternate(t *testing.T) {
	prevBudget := virtualStalePinRecoveryBudget
	virtualStalePinRecoveryBudget = 100 * time.Millisecond
	t.Cleanup(func() { virtualStalePinRecoveryBudget = prevBudget })

	const neutral = "virtual://movie/tt-stale-slow"
	row := stalePinRow(14, "movie-stale-slow", neutral, "STALE")
	alt := &models.MediaFile{ID: 15, ContentID: row.ContentID, FilePath: neutral + "?result=ALT", VirtualOwnerInstallationID: 5}

	// The primary resolve (its listing and the stale-source fallback's re-list)
	// answers empty so the primary genuinely terminals. The recovery runs its
	// own listing under the short recovery sub-budget, so that listing hangs
	// until the sub-budget expires, simulating a hung provider. Every other
	// listing (including the alternate's) answers empty, so the alternate
	// resolves through the detailed resolver once the recovery yields.
	h := &PlaybackHandler{
		FileVersionFetcher: testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{row.ContentID: {row, alt}}},
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
			return "", errors.New("simple resolver must not be used when the detailed resolver is set")
		}),
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(ctx context.Context, _ string, _ int, _ string, _ int) ([]VirtualPlaybackStream, error) {
			// Keyed on the recovery's own sub-budget rather than a global call
			// ordinal: the walk's call sequence is an implementation detail,
			// while "the recovery listing is the one running under its
			// sub-budget" is the behavior this test pins.
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= 2*virtualStalePinRecoveryBudget {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return nil, nil
		}),
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			if virtualResultCandidateID(uri) == "ALT" {
				return ResolvedVirtualMedia{URL: "http://127.0.0.1:8080/stream?uri=" + uri, URI: uri, CandidateID: "ALT"}, nil
			}
			return ResolvedVirtualMedia{}, absentSessionPinError(virtualResultCandidateID(uri))
		}),
	}
	req := stalePinRecoveryRequest()
	start := time.Now()
	resolved, err := h.resolveVirtualStartWithVersionFallback(req, row, "profile-1", playback.StartRequestV3{QualityPreference: "auto"}, false, 0)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("walk after a slow recovery: %v", err)
	}
	if resolved.File == nil || virtualResultCandidateID(resolved.URI) != "ALT" {
		t.Fatalf("resolved = %#v, want the healthy alternate ALT after the recovery yielded", resolved)
	}
	// The recovery must not have consumed the whole parent decision budget: the
	// alternate resolved well inside it.
	if elapsed >= virtualStartVersionFallbackDecisionBudget {
		t.Fatalf("walk took %s, want < the %s parent budget", elapsed, virtualStartVersionFallbackDecisionBudget)
	}
}

// (5) Session-bound refusal through the WALK: a walk that carries the
// session-bound intent must not re-pin, while the same walk carrying the
// fresh-start intent does. This proves the recovery preserves the caller's
// binding intent rather than forcing it false.
func TestStalePinRecoveryBindingIntentPreservedThroughWalk(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-walk-bind"
	row := stalePinRow(16, "movie-stale-walk-bind", neutral, "STALE")
	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")}

	// newHandler wires a primary outage followed by a non-empty recovery listing
	// so the walk reaches the recovery; the resolver answers the pin with an
	// absent-pin refusal and the sibling with a successful resolve.
	newHandler := func() *PlaybackHandler {
		var listCalls, resolveCalls atomic.Int32
		h := &PlaybackHandler{
			FileVersionFetcher: testPlaybackFileVersionFetcher{byContent: map[string][]*models.MediaFile{row.ContentID: {row}}},
			VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
				return "", errors.New("simple resolver must not be used when the detailed resolver is set")
			}),
			VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(ctx context.Context, _ string, _ int, _ string, _ int) ([]VirtualPlaybackStream, error) {
				if listCalls.Add(1) == 1 {
					return nil, nil
				}
				return live, nil
			}),
			VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
				resolveCalls.Add(1)
				if virtualResultCandidateID(uri) == "STALE" {
					return ResolvedVirtualMedia{}, absentSessionPinError("STALE")
				}
				return ResolvedVirtualMedia{URL: "http://127.0.0.1:8080/stream?uri=" + uri, URI: uri, CandidateID: virtualResultCandidateID(uri)}, nil
			}),
			VirtualPlaybackSourceProber: func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
				probed := stalePinProbedTrack(row.Duration)
				probed.ID = f.ID
				probed.FilePath = f.FilePath
				return probed, nil
			},
		}
		return h
	}

	// Fresh start: the request declares unbound, the walk threads it, recovery runs.
	freshReq := stalePinRecoveryRequest()
	fresh, err := newHandler().resolveVirtualStartWithVersionFallback(freshReq, row, "profile-1", playback.StartRequestV3{QualityPreference: "auto"}, false, 0)
	if err != nil {
		t.Fatalf("fresh-start walk: %v", err)
	}
	if fresh.File == nil || virtualResultCandidateID(fresh.URI) != "NEW" {
		t.Fatalf("fresh-start resolved = %#v, want the recovered NEW", fresh)
	}

	// Session-bound: the request declares bound, the walk preserves it, and the
	// recovery must refuse to re-pin.
	boundReq := stalePinSessionBoundRequest()
	bound, err := newHandler().resolveVirtualStartWithVersionFallback(boundReq, row, "profile-1", playback.StartRequestV3{QualityPreference: "auto"}, false, 0)
	if err == nil {
		t.Fatalf("session-bound walk = %#v, want the primary terminal preserved", bound)
	}
	if virtualResultCandidateID(bound.URI) == "NEW" {
		t.Fatal("a session-bound walk must not re-pin the absent candidate")
	}
}

// The fingerprint predicate is the boundary: inside tolerance accepts, outside
// refuses, and only the duration tier is neutral for an unknown side.
func TestStalePinFingerprintTolerance(t *testing.T) {
	row := &models.MediaFile{CodecVideo: "h264", CodecAudio: "aac", FileSize: 1_000_000, Duration: 7200}
	cases := []struct {
		name      string
		candidate VirtualPlaybackStream
		want      bool
	}{
		{"exact", VirtualPlaybackStream{CodecVideo: "h264", CodecAudio: "aac", FileSize: 1_000_000}, true},
		{"size +4%", VirtualPlaybackStream{CodecVideo: "h264", CodecAudio: "aac", FileSize: 1_040_000}, true},
		{"size -5%", VirtualPlaybackStream{CodecVideo: "h264", CodecAudio: "aac", FileSize: 950_000}, true},
		{"size +6% refused", VirtualPlaybackStream{CodecVideo: "h264", CodecAudio: "aac", FileSize: 1_060_000}, false},
		{"unknown candidate size refused", VirtualPlaybackStream{CodecVideo: "h264", CodecAudio: "aac", FileSize: 0}, false},
		{"codec mismatch refused", VirtualPlaybackStream{CodecVideo: "hevc", CodecAudio: "aac", FileSize: 1_000_000}, false},
		{"unknown candidate codec refused", VirtualPlaybackStream{CodecAudio: "aac", FileSize: 1_000_000}, false},
		{"audio codec mismatch refused", VirtualPlaybackStream{CodecVideo: "h264", CodecAudio: "eac3", FileSize: 1_000_000}, false},
		{"unknown candidate audio neutral", VirtualPlaybackStream{CodecVideo: "h264", FileSize: 1_000_000}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := virtualStalePinStreamFingerprintMatch(row, tc.candidate); got != tc.want {
				t.Fatalf("fingerprint = %v, want %v", got, tc.want)
			}
		})
	}

	durations := []struct {
		row, candidate int
		want           bool
	}{
		{7200, 7200, true},
		{7200, 7300, true},  // +1.4%
		{7200, 7400, false}, // +2.8%
		{0, 7300, true},     // unknown row duration is neutral
		{7200, 0, true},     // unknown candidate duration is neutral
	}
	for _, tc := range durations {
		if got := virtualStalePinDurationMatch(tc.row, tc.candidate); got != tc.want {
			t.Fatalf("duration(%d,%d) = %v, want %v", tc.row, tc.candidate, got, tc.want)
		}
	}
}

// TestStalePinRecoveryClearsOldCandidateState pins the in-memory re-pin's field
// hygiene via the WALK (the recovery itself): the matched candidate must not
// inherit the previous pin's stored URL, headers, durable identity, or probe
// evidence. Carrying the stored URL would serve the old bytes under the new id;
// carrying the probe stamp would report the old inventory as verified for the
// new candidate. The row's own lifecycle fields (verdict and delivery stamp) are
// deliberately preserved, so the recovery does not alter verdict semantics.
func TestStalePinRecoveryClearsOldCandidateState(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-clear"
	expiry := time.Now().Add(2 * time.Hour)
	delivered := time.Now().Add(-time.Minute)
	failedAt := time.Now().Add(-time.Minute)
	probedAt := time.Now().Add(-time.Minute)
	row := stalePinRow(17, "movie-stale-clear", neutral, "STALE")
	row.ResolvedURL = "https://old.example/stale.mkv"
	row.ResolvedURLExpiresAt = &expiry
	row.ProviderRequestHeaders = map[string]string{"Referer": "https://old.example/"}
	row.ProviderVideoHash = "old-hash"
	row.ProviderReleaseName = "Old.Release"
	row.ProviderReleaseSize = row.FileSize
	row.LastDeliveredAt = &delivered
	row.FailedAt = &failedAt
	row.ProbeUpdatedAt = &probedAt
	row.ProbeSource = virtualCollectionProbeSource
	row.VideoTracks = []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}
	row.AudioTracks = []models.AudioTrack{{Codec: "aac", Channels: 2}}
	row.SubtitleTracks = []models.SubtitleTrack{{Codec: "subrip", Language: "eng"}}
	row.ExternalSubtitles = []models.ExternalSubtitle{{Path: "/tmp/old.srt"}}

	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")}
	match, matchedStream, matched := virtualStalePinMatchStream(row, live)
	if matched != 1 {
		t.Fatalf("the fingerprint match was not found uniquely (matched=%d)", matched)
	}
	if match.FilePath != neutral+"?result=NEW" {
		t.Fatalf("matched path = %q, want the live candidate", match.FilePath)
	}
	if matchedStream.URI != neutral+"?result=NEW" {
		t.Fatalf("matched stream URI = %q, want the live candidate", matchedStream.URI)
	}
	if match.ResolvedURL != "" || match.ResolvedURLExpiresAt != nil || match.ProviderRequestHeaders != nil {
		t.Fatalf("re-pin carried the old transport state: url=%q expires=%v headers=%v", match.ResolvedURL, match.ResolvedURLExpiresAt, match.ProviderRequestHeaders)
	}
	if match.ProviderVideoHash != "" || match.ProviderReleaseName != "" || match.ProviderReleaseSize != 0 {
		t.Fatalf("re-pin carried the old durable identity: hash=%q name=%q size=%d", match.ProviderVideoHash, match.ProviderReleaseName, match.ProviderReleaseSize)
	}
	if match.LastDeliveredAt == nil || match.FailedAt == nil {
		t.Fatalf("re-pin cleared the row's own lifecycle state: delivered=%v failed=%v (verdict/delivery semantics must be untouched)", match.LastDeliveredAt, match.FailedAt)
	}
	if match.ProbeUpdatedAt != nil || match.ProbeSource != "" || len(match.VideoTracks) != 0 || len(match.AudioTracks) != 0 || len(match.SubtitleTracks) != 0 || len(match.ExternalSubtitles) != 0 {
		t.Fatalf("re-pin carried the old probe evidence: stamp=%v source=%q video=%d audio=%d subs=%d ext=%d",
			match.ProbeUpdatedAt, match.ProbeSource, len(match.VideoTracks), len(match.AudioTracks), len(match.SubtitleTracks), len(match.ExternalSubtitles))
	}
	// The declared fingerprint fields are what the match was made against and
	// must survive so candidate ranking still sees a 1080p h264 release.
	if match.FileSize != row.FileSize || match.CodecVideo != "h264" || match.Resolution != "1080p" {
		t.Fatalf("re-pin dropped the matched fingerprint fields: size=%d codec=%q res=%q", match.FileSize, match.CodecVideo, match.Resolution)
	}
}

// TestStalePinRecoveryRefusesSessionBound is a focused unit test of the helper:
// a session-bound resolve must keep refusing an absent pin, never re-pin it.
func TestStalePinRecoveryRefusesSessionBound(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-bound"
	row := stalePinRow(18, "movie-stale-bound", neutral, "STALE")
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{
		live:          []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")},
		probeDuration: row.Duration,
	})

	_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinSessionBoundRequest(), row, "profile-1")
	if ok {
		t.Fatal("a session-bound resolve must not be re-pinned")
	}
	if got := fx.resolveCalls.Load(); got != 0 {
		t.Fatalf("resolves = %d, want 0 for a session-bound resolve", got)
	}
	if got := fx.listCalls.Load(); got != 0 {
		t.Fatalf("listings = %d, want 0 (the recovery must not even list)", got)
	}
}

// TestStalePinRecoveryRefusesAmbiguousFingerprint is the fix-boundary test for
// ambiguous fingerprints: two same-title releases share codec, size and
// runtime, so both fingerprint-match the row. The recovery must REFUSE rather
// than rank-pick the first, whatever the listing order. Both orders are checked
// so "first match wins" cannot pass by accident.
func TestStalePinRecoveryRefusesAmbiguousFingerprint(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-ambiguous"
	row := stalePinRow(19, "movie-stale-ambiguous", neutral, "STALE")

	first := stalePinLiveStream(neutral, "AAA", row.FileSize, "h264")
	second := stalePinLiveStream(neutral, "BBB", row.FileSize, "h264")
	if first.URI == second.URI {
		t.Fatal("fixture candidates must be distinct identities")
	}

	for _, tc := range []struct {
		name string
		live []VirtualPlaybackStream
	}{
		{"listed AAA then BBB", []VirtualPlaybackStream{first, second}},
		{"listed BBB then AAA", []VirtualPlaybackStream{second, first}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{
				live:          tc.live,
				probeDuration: row.Duration,
			})
			_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
			if ok {
				t.Fatal("an ambiguous fingerprint must not recover: rank-picking could adopt a different release")
			}
			if got := fx.resolveCalls.Load(); got != 0 {
				t.Fatalf("resolves = %d, want 0 on an ambiguous match", got)
			}
			if got := fx.saveCalls.Load(); got != 0 {
				t.Fatalf("evidence writes = %d, want 0 on an ambiguous match", got)
			}
			if got := fx.handler.peekVirtualSticky(fx.stickyKey(row)); got != "" {
				t.Fatalf("sticky pin = %q, want none on an ambiguous match", got)
			}
			if got := virtualResultCandidateID(row.FilePath); got != "STALE" {
				t.Fatalf("catalog row pin = %q, want the durable STALE unchanged", got)
			}
		})
	}
}

// TestStalePinRecoveryVetoesResolutionConflict proves a known resolution
// conflict vetoes a candidate that the size+codec tiers alone would have let
// through, so an edition at a different resolution cannot be adopted and does
// not make the remaining match ambiguous.
func TestStalePinRecoveryVetoesResolutionConflict(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-resolution"
	row := stalePinRow(20, "movie-stale-resolution", neutral, "STALE")
	row.Resolution = "1080p"

	hd := stalePinLiveStream(neutral, "HD", row.FileSize, "h264")
	hd.Resolution = "1080p"
	sd := stalePinLiveStream(neutral, "SD", row.FileSize, "h264")
	sd.Resolution = "720p"

	for _, tc := range []struct {
		name string
		live []VirtualPlaybackStream
	}{
		{"HD then SD", []VirtualPlaybackStream{hd, sd}},
		{"SD then HD", []VirtualPlaybackStream{sd, hd}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match, stream, matched := virtualStalePinMatchStream(row, tc.live)
			if matched != 1 {
				t.Fatalf("matched=%d, want the single resolution-compatible candidate", matched)
			}
			if stream.URI != hd.URI || match.FilePath != hd.URI {
				t.Fatalf("matched %q, want the 1080p candidate %q", stream.URI, hd.URI)
			}
		})
	}
}

// TestStalePinRecoveryVetoesAudioLanguageConflict proves a known
// audio-language conflict vetoes a same-size same-codec candidate that is a
// different language variant of the title.
func TestStalePinRecoveryVetoesAudioLanguageConflict(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-language"
	row := stalePinRow(21, "movie-stale-language", neutral, "STALE")
	row.AudioTracks = []models.AudioTrack{{Codec: "aac", Channels: 2, Language: "eng"}}

	french := stalePinLiveStream(neutral, "FRA", row.FileSize, "h264")
	french.AudioLanguages = []string{"fra"}
	english := stalePinLiveStream(neutral, "ENG", row.FileSize, "h264")
	english.AudioLanguages = []string{"ENG"}

	for _, tc := range []struct {
		name string
		live []VirtualPlaybackStream
		want string
	}{
		{"french then english", []VirtualPlaybackStream{french, english}, english.URI},
		{"english then french", []VirtualPlaybackStream{english, french}, english.URI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			match, stream, matched := virtualStalePinMatchStream(row, tc.live)
			if matched != 1 {
				t.Fatalf("matched=%d, want only the language-compatible candidate", matched)
			}
			if stream.URI != tc.want || match.FilePath != tc.want {
				t.Fatalf("matched %q, want %q", stream.URI, tc.want)
			}
		})
	}

	// A listing whose only candidate is a conflicting language is ambiguous in
	// the sense that matters: it is not the same release, so no match at all.
	_, _, matched := virtualStalePinMatchStream(row, []VirtualPlaybackStream{french})
	if matched != 0 {
		t.Fatalf("a lone conflicting-language candidate matched=%d, want no match", matched)
	}
}

// TestStalePinRecoveryNewerSelectionFinishesFirst is the generation-fence proof
// for the early allocation. An earlier-started recovery blocks in its listing;
// a newer selection pins and caches first; the recovery then finishes. Because
// the recovery captured its generation before listing, its publication carries
// an older generation and must NOT overwrite the newer selection. Allocating the
// generation at publication instead would let the recovery's later finish win.
func TestStalePinRecoveryNewerSelectionFinishesFirst(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-interleave"
	row := stalePinRow(22, "movie-stale-interleave", neutral, "STALE")
	newerURI := neutral + "?result=NEWER"

	started := make(chan struct{})
	releaseList := make(chan struct{})
	var listOnce sync.Once
	h := &PlaybackHandler{
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
			return "", errors.New("simple resolver must not be used")
		}),
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			listOnce.Do(func() { close(started) })
			<-releaseList
			return []VirtualPlaybackStream{stalePinLiveStream(neutral, "RECOVERED", row.FileSize, "h264")}, nil
		}),
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			return ResolvedVirtualMedia{URL: "http://127.0.0.1:8080/stream?uri=" + uri, URI: uri, CandidateID: virtualResultCandidateID(uri)}, nil
		}),
		VirtualPlaybackSourceProber: func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			probed := stalePinProbedTrack(row.Duration)
			probed.ID = f.ID
			probed.FilePath = f.FilePath
			return probed, nil
		},
	}

	stickyKey := bestResultCacheKey(row.ContentID, neutral, row.VirtualOwnerInstallationID)
	done := make(chan bool, 1)
	go func() {
		_, ok := h.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
		done <- ok
	}()

	// Wait until the recovery has captured its generation and entered the
	// listing, then let a newer selection finish first.
	<-started
	h.pinVirtualStickyAt(stickyKey, newerURI, nextVirtualCacheGeneration())
	close(releaseList)

	if ok := <-done; !ok {
		t.Fatal("the recovery should still resolve its unique candidate")
	}
	if got := h.peekVirtualSticky(stickyKey); got != newerURI {
		t.Fatalf("sticky pin = %q, want the newer selection %q: the late-finishing recovery must not overwrite it", got, newerURI)
	}
}

// TestStalePinRecoveryRejectsNeutralAndCrossContentSubstitutions proves the
// exact-candidate check requires a concrete matched candidate: a neutral URI
// (no result= pick) and a same-id candidate under a different content both
// reject before any publication.
func TestStalePinRecoveryRejectsNeutralAndCrossContentSubstitutions(t *testing.T) {
	const (
		neutral = "virtual://movie/tt-stale-exact"
		foreign = "virtual://movie/tt-stale-foreign"
	)
	row := stalePinRow(23, "movie-stale-exact", neutral, "STALE")

	cases := []struct {
		name        string
		resolverURI string
		candidateID string
	}{
		{"neutral URI", neutral, ""},
		{"same result id under a different content", foreign + "?result=NEW", ""},
		{"candidate id inconsistent with the URI", neutral + "?result=NEW", "DIFFERENT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{
				live:                []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")},
				probeDuration:       row.Duration,
				resolverURI:         tc.resolverURI,
				resolverCandidateID: tc.candidateID,
			})
			_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
			if ok {
				t.Fatal("a substituted candidate identity must not recover the pin")
			}
			if got := fx.resolveCalls.Load(); got != 1 {
				t.Fatalf("resolves = %d, want exactly 1", got)
			}
			if got := fx.saveCalls.Load(); got != 0 {
				t.Fatalf("evidence writes = %d, want 0 on a rejected substitution", got)
			}
			if got := fx.handler.peekVirtualSticky(fx.stickyKey(row)); got != "" {
				t.Fatalf("sticky pin = %q, want none on a rejected substitution", got)
			}
		})
	}
}

// TestStalePinRecoveryPersistsOnlyAgainstExistingOwner proves the recovery is
// transient: with no row already owning the matched concrete path, it persists
// no evidence at all (so it can never adopt the path onto the requested row);
// with an existing alternate owner it writes a metadata-only update against that
// owner, never the requested row.
func TestStalePinRecoveryPersistsOnlyAgainstExistingOwner(t *testing.T) {
	const neutral = "virtual://movie/tt-stale-owner"
	row := stalePinRow(24, "movie-stale-owner", neutral, "STALE")
	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")}

	t.Run("no existing owner persists nothing", func(t *testing.T) {
		fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{
			live:          live,
			probeDuration: row.Duration,
		})
		_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
		if !ok {
			t.Fatal("the unique candidate should recover")
		}
		if got := fx.saveCalls.Load(); got != 0 {
			t.Fatalf("evidence writes = %d, want 0: the recovery must not adopt the candidate onto the requested row", got)
		}
		if got := virtualResultCandidateID(row.FilePath); got != "STALE" {
			t.Fatalf("catalog row pin = %q, want the durable STALE unchanged", got)
		}
	})

	t.Run("existing alternate owner gets a metadata-only write", func(t *testing.T) {
		ownerRow := &models.MediaFile{
			ID:                         99,
			ContentID:                  row.ContentID,
			FilePath:                   neutral + "?result=NEW",
			VirtualOwnerInstallationID: row.VirtualOwnerInstallationID,
			MediaFolderID:              row.MediaFolderID,
			ProbeSource:                "virtual",
		}
		fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{
			live:              live,
			probeDuration:     row.Duration,
			pathOwnerRow:      ownerRow,
			pathOwnerResolves: true,
		})
		_, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
		if !ok {
			t.Fatal("the unique candidate should recover")
		}
		fx.handler.StopVirtualEvidence()

		saved := fx.saved()
		if len(saved) != 1 {
			t.Fatalf("evidence writes = %d, want exactly 1 against the existing owner", len(saved))
		}
		if saved[0].FileID != ownerRow.ID {
			t.Fatalf("evidence write targeted file %d, want the owner %d", saved[0].FileID, ownerRow.ID)
		}
		if saved[0].AdoptPath != "" || saved[0].RequireAdopt {
			t.Fatalf("owner write nominated an adoption target: %+v", saved[0])
		}
	})
}

// TestStalePinRecoveryReListHonorsSharedDamper proves the recovery's re-list is
// bounded by the same per-provider budget the sibling fallback uses: once the
// damper is exhausted the recovery yields deterministically instead of listing,
// with no sleep and no side effects. It also proves the recovery is what spends
// that budget, so a burst of dead-pin starts cannot re-list a failing provider
// without the shared backpressure.
func TestStalePinRecoveryReListHonorsSharedDamper(t *testing.T) {
	resetVirtualRecoveryRelists(t)
	const neutral = "virtual://movie/tt-stale-damper"
	row := stalePinRow(25, "movie-stale-damper", neutral, "STALE")
	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")}

	// A full budget admits the recovery once and spends exactly one re-list.
	// A successful listing clears the key, so use a first recovery that answers
	// empty to keep the spent mark, then a second recovery must be denied.
	fxEmpty := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{probeDuration: row.Duration})
	for i := 0; i < virtualRecoveryRelistMax; i++ {
		if _, ok := fxEmpty.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1"); ok {
			t.Fatalf("recovery %d answered an empty listing but reported success", i+1)
		}
	}
	if got := fxEmpty.listCalls.Load(); got != int32(virtualRecoveryRelistMax) {
		t.Fatalf("listings = %d, want %d: an empty answer must accumulate toward the damper", got, virtualRecoveryRelistMax)
	}

	// The budget is now exhausted. A fingerprint-matched recovery must not even
	// list: the damper is what stops it, deterministically and without sleeping.
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{
		live:          live,
		probeDuration: row.Duration,
	})
	recovered, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1")
	if ok {
		t.Fatalf("recovery = %#v, want the terminal preserved once the shared damper is exhausted", recovered)
	}
	if got := fx.listCalls.Load(); got != 0 {
		t.Fatalf("listings = %d, want 0: an exhausted budget must not re-list the provider", got)
	}
	if got := fx.resolveCalls.Load(); got != 0 {
		t.Fatalf("resolves = %d, want 0 after a denied recovery re-list", got)
	}
	if got := fx.handler.peekVirtualSticky(fx.stickyKey(row)); got != "" {
		t.Fatalf("sticky pin = %q, want none after a denied recovery re-list", got)
	}
	if got := virtualResultCandidateID(row.FilePath); got != "STALE" {
		t.Fatalf("catalog row pin = %q, want the durable STALE unchanged", got)
	}
}

// TestStalePinRecoveryReListClearsBudgetOnAnswer proves the recovery uses the
// fallback's accounting exactly: a listing that answers with candidates is no
// longer defeating a provider fail-fast, so the recovery clears the key and a
// later failure starts from a full budget. A listing that answers empty
// deliberately keeps the mark, which the damper test above exercises.
func TestStalePinRecoveryReListClearsBudgetOnAnswer(t *testing.T) {
	resetVirtualRecoveryRelists(t)
	const neutral = "virtual://movie/tt-stale-clear-budget"
	row := stalePinRow(26, "movie-stale-clear-budget", neutral, "STALE")
	live := []VirtualPlaybackStream{stalePinLiveStream(neutral, "NEW", row.FileSize, "h264")}
	fx := newStalePinRecoveryFixture(neutral+"?result=STALE", stalePinRecoveryHandlerOpts{
		live:          live,
		probeDuration: row.Duration,
	})

	if _, ok := fx.handler.recoverStaleIdentityLessPinV3(stalePinRecoveryRequest(), row, "profile-1"); !ok {
		t.Fatal("the unique fingerprint match should recover")
	}
	recoveryKey := virtualRecoveryRelistKey(neutral, row.VirtualOwnerInstallationID)
	virtualRecoveryRelists.mu.Lock()
	_, marked := virtualRecoveryRelists.marks[recoveryKey]
	virtualRecoveryRelists.mu.Unlock()
	if marked {
		t.Fatal("an answered recovery listing must clear the key so a later failure starts from a full budget")
	}
}
