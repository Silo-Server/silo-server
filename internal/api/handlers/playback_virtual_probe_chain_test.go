package handlers

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// The pre-play probe chain must never block its caller, never probe bytes it
// cannot name, and must terminate on its own budget and free its slot. These
// tests pin the admission contract and the skip rules with stub targets and
// probers; the probe tail itself (damper, evidence write) is covered by the
// machinery tests it already shares.

const probeChainTestContent = "movie-probe-chain"

func probeChainProviderURL(row *models.MediaFile) string {
	return fmt.Sprintf("https://probe.example/stream/%d", row.ID)
}

func probeChainRow(fileID int, filePath string, probeUpdatedAt *time.Time) *models.MediaFile {
	row := &models.MediaFile{
		ID: fileID, ContentID: probeChainTestContent, FilePath: filePath,
		MediaFolderID: 77, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
		ProbeUpdatedAt: probeUpdatedAt,
		// A stored URL carries no expiry here, so the chain's staleness fence
		// reads the row's own freshness clock. These fixtures describe a
		// candidate the discovery run just listed.
		UpdatedAt: time.Now(),
	}
	if probeUpdatedAt != nil {
		// A probed stamp is only a reason to skip the chain when the evidence it
		// stamped actually describes tracks; a stamp with empty arrays is the
		// state an unadoptable write leaves behind and must be re-probed.
		row.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}}
		row.SubtitleTracks = []models.SubtitleTrack{{Index: 0, Codec: "subrip", Language: "eng"}}
	}
	row.ResolvedURL = probeChainProviderURL(row)
	return row
}

func probeChainStreams(row *models.MediaFile) []VirtualPlaybackStream {
	return []VirtualPlaybackStream{
		{URI: "virtual://movie/probe-chain?result=c5", OwnerInstallationID: 5, Resolution: "1080p"},
		{URI: row.FilePath, ProviderURL: probeChainProviderURL(row), OwnerInstallationID: row.VirtualOwnerInstallationID, Resolution: "2160p",
			AudioLanguages: []string{"eng"}, SubtitleLanguages: []string{"eng"}},
	}
}

func probeChainSourceRow() *models.MediaFile {
	return &models.MediaFile{
		ID: 300, ContentID: probeChainTestContent, FilePath: "virtual://movie/probe-chain",
		MediaFolderID: 77, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
	}
}

// blockedDefaultTarget gates the default-version re-read: the chain stays
// inside admitted work until released, so tests can hold the chain at that
// exact point and inspect admission behavior while it is live.
type blockedDefaultTarget struct {
	mu     sync.Mutex
	open   chan struct{}
	callN  int
	row    *models.MediaFile
	filter catalog.AccessFilter
}

func (b *blockedDefaultTarget) DefaultVirtualProbeTarget(ctx context.Context, contentID string, filter catalog.AccessFilter) (*models.MediaFile, error) {
	b.mu.Lock()
	b.callN++
	b.filter = filter
	b.mu.Unlock()
	select {
	case <-b.open:
		return b.row, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *blockedDefaultTarget) lastFilter() catalog.AccessFilter {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.filter
}

func (b *blockedDefaultTarget) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.callN
}

func (b *blockedDefaultTarget) releaseRow() {
	select {
	case <-b.open:
	default:
		close(b.open)
	}
}

// recordingProber records each probe's URL. The chain's probe tail persists
// through the queued evidence path; without a saver the write is refused
// harmlessly, so the stub only answers the enumeration itself.
type recordingProber struct {
	mu   sync.Mutex
	urls []string
	hits chan struct{}
}

func (r *recordingProber) probe(_ context.Context, sourceURL string, file *models.MediaFile) (*models.MediaFile, error) {
	r.mu.Lock()
	r.urls = append(r.urls, sourceURL)
	r.mu.Unlock()
	if r.hits != nil {
		select {
		case r.hits <- struct{}{}:
		default:
		}
	}
	return &models.MediaFile{FilePath: sourceURL, Duration: 5400, AudioTracks: []models.AudioTrack{{Codec: "eac3", Channels: 6}}}, nil
}

func (r *recordingProber) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.urls)
}

func (r *recordingProber) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.urls) == 0 {
		return ""
	}
	return r.urls[len(r.urls)-1]
}

func probeChainHandler(row *models.MediaFile) (*PlaybackHandler, *blockedDefaultTarget, *recordingProber) {
	target := &blockedDefaultTarget{open: make(chan struct{}), row: row}
	prober := &recordingProber{}
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		DefaultProbeTarget:          target,
		VirtualPlaybackSourceProber: prober.probe,
	}
	return h, target, prober
}

// probeChainWaitFor polls until the observable reaches the expected value
// (bounded); use for positive expectations like "the chain began its read".
func probeChainWaitFor(t *testing.T, want int, read func() int, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := read(); got == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("%s = %d, want %d", what, got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// probeChainStableWait gives a negative assertion a bounded burn-in: the
// expectation is "no new target read within this window", the only observable
// formulation of a spawn that must not have happened.
func probeChainStableWait(t *testing.T, want int, read func() int, what string) {
	t.Helper()
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		if got := read(); got != want {
			t.Fatalf("%s = %d, want stable %d", what, got, want)
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestScheduleDefaultFileProbeProbesTheDefaultRowsOwnBytes(t *testing.T) {
	row := probeChainRow(301, "virtual://movie/probe-chain?result=c1", nil)
	h, target, prober := probeChainHandler(row)
	target.releaseRow()

	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(row), catalog.AccessFilter{})
	h.probeChainWG.Wait()
	if prober.count() != 1 {
		t.Fatalf("probe calls = %d, want exactly 1", prober.count())
	}
	if got := prober.last(); got != probeChainProviderURL(row) {
		t.Fatalf("probed %q, want the default row's own resolved bytes %q", got, probeChainProviderURL(row))
	}
	if target.callCount() != 1 {
		t.Fatalf("target reads = %d, want 1", target.callCount())
	}
}

func TestNewVirtualDefaultProbeTargetUsesSelectedFile(t *testing.T) {
	defaultRow := probeChainRow(700, "virtual://movie/probe-chain?result=default", nil)
	selectedRow := probeChainRow(701, "virtual://movie/probe-chain?result=selected", nil)
	reader := &probeChainVersionReader{
		files: []*models.MediaFile{defaultRow, selectedRow},
		rows:  map[int]*models.MediaFile{defaultRow.ID: defaultRow, selectedRow.ID: selectedRow},
	}
	target := NewVirtualDefaultProbeTarget(reader, reader)

	got, err := target.DefaultVirtualProbeTarget(t.Context(), probeChainTestContent, catalog.AccessFilter{SelectedFileID: selectedRow.ID, AllowedLibraryIDs: []int{77}})
	if err != nil || got == nil || got.ID != selectedRow.ID || got.FilePath != selectedRow.FilePath {
		t.Fatalf("selected target = %+v, err = %v; want file %d", got, err, selectedRow.ID)
	}

	if got, err := target.DefaultVirtualProbeTarget(t.Context(), probeChainTestContent, catalog.AccessFilter{SelectedFileID: 999, AllowedLibraryIDs: []int{77}}); err != nil || got != nil {
		t.Fatalf("unknown selected target = %+v, err = %v; want no target", got, err)
	}

	selectedRow.MediaFolderID = 78
	reader.rows[selectedRow.ID] = selectedRow
	if got, err := target.DefaultVirtualProbeTarget(t.Context(), probeChainTestContent, catalog.AccessFilter{SelectedFileID: selectedRow.ID, AllowedLibraryIDs: []int{77}}); err != nil || got != nil {
		t.Fatalf("hidden selected target = %+v, err = %v; want no target", got, err)
	}
}

func TestScheduleDefaultFileProbeProbesPersistedSelectedWhenListingChanged(t *testing.T) {
	defaultRow := probeChainRow(730, "virtual://movie/probe-chain?result=default", nil)
	selectedRow := probeChainRow(731, "virtual://movie/probe-chain?profile=4K+Dolby+Vision&result=selected", nil)
	reader := &probeChainVersionReader{
		files: []*models.MediaFile{defaultRow, selectedRow},
		rows:  map[int]*models.MediaFile{defaultRow.ID: defaultRow, selectedRow.ID: selectedRow},
	}
	prober := &recordingProber{}
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		DefaultProbeTarget:          NewVirtualDefaultProbeTarget(reader, reader),
		VirtualPlaybackSourceProber: prober.probe,
	}
	// The fresh listing no longer names the selected row's URI (the provider
	// renumbered it), but the selected row itself is persisted and authorized.
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(defaultRow), catalog.AccessFilter{SelectedFileID: selectedRow.ID, AllowedLibraryIDs: []int{77}})
	h.probeChainWG.Wait()
	if prober.count() != 1 || prober.last() != probeChainProviderURL(selectedRow) {
		t.Fatalf("probes = %d, last = %q; want the persisted selected row %q", prober.count(), prober.last(), probeChainProviderURL(selectedRow))
	}
}

func TestScheduleDefaultFileProbePublishesAfterDurableEvidence(t *testing.T) {
	defaultRow := probeChainRow(720, "virtual://movie/probe-chain?result=default", nil)
	selectedRow := probeChainRow(721, "virtual://movie/probe-chain?result=selected", nil)
	reader := &probeChainVersionReader{
		files: []*models.MediaFile{defaultRow, selectedRow},
		rows:  map[int]*models.MediaFile{defaultRow.ID: defaultRow, selectedRow.ID: selectedRow},
	}
	bus := &recordingEventBus{}
	hub := notifications.NewHub("probe-chain-event", bus)
	prober := &recordingProber{}
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		EventsHub:                   hub.EventsHub(),
		DefaultProbeTarget:          NewVirtualDefaultProbeTarget(reader, reader),
		VirtualPlaybackSourceProber: prober.probe,
		VirtualFileMetadataSaver: func(context.Context, models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
			return VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}, nil
		},
	}
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(selectedRow), catalog.AccessFilter{SelectedFileID: selectedRow.ID, AllowedLibraryIDs: []int{77}})
	h.probeChainWG.Wait()
	payload := waitForRecordingEvent(t, bus, 2*time.Second, "catalog.item.changed", "versions_updated", probeChainTestContent)
	if payload == "" {
		t.Fatal("verified evidence did not publish versions_updated")
	}
}

func TestScheduleDefaultFileProbeProbesSelectedBytes(t *testing.T) {
	defaultRow := probeChainRow(710, "virtual://movie/probe-chain?result=default", nil)
	selectedRow := probeChainRow(711, "virtual://movie/probe-chain?result=selected", nil)
	reader := &probeChainVersionReader{
		files: []*models.MediaFile{defaultRow, selectedRow},
		rows:  map[int]*models.MediaFile{defaultRow.ID: defaultRow, selectedRow.ID: selectedRow},
	}
	prober := &recordingProber{}
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		DefaultProbeTarget:          NewVirtualDefaultProbeTarget(reader, reader),
		VirtualPlaybackSourceProber: prober.probe,
	}
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(selectedRow), catalog.AccessFilter{SelectedFileID: selectedRow.ID, AllowedLibraryIDs: []int{77}})
	h.probeChainWG.Wait()
	if prober.count() != 1 || prober.last() != probeChainProviderURL(selectedRow) {
		t.Fatalf("probes = %d, last = %q; want selected file %q", prober.count(), prober.last(), probeChainProviderURL(selectedRow))
	}
}

func TestScheduleDefaultFileProbeSecondCallCoalescesIntoLiveChain(t *testing.T) {
	row := probeChainRow(311, "virtual://movie/probe-chain?result=c11", nil)
	h, target, prober := probeChainHandler(row)

	// While the first chain sits inside admitted work, a second schedule of
	// the same item must return immediately and spawn no second read.
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(row), catalog.AccessFilter{})
	probeChainWaitFor(t, 1, target.callCount, "live chain target reads")

	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(row), catalog.AccessFilter{})
	probeChainStableWait(t, 1, target.callCount, "coalesced chain target reads")

	target.releaseRow()
	h.probeChainWG.Wait()
	if prober.count() != 1 {
		t.Fatalf("probe calls after two schedules = %d, want one chain", prober.count())
	}
}

func TestScheduleDefaultFileProbeCoalescesSelectedVersionAcrossSources(t *testing.T) {
	row := probeChainRow(312, "virtual://movie/probe-chain?result=c12", nil)
	h, target, _ := probeChainHandler(row)

	// Discovery persists one source group at a time. Each group must not spawn
	// its own chain for the same selected version: that exhausted the process
	// slots with duplicates and left the actual selected chain unscheduled.
	secondSource := probeChainSourceRow()
	secondSource.FilePath = "virtual://movie/probe-chain?profile=1080p"
	filter := catalog.AccessFilter{SelectedFileID: row.ID, AllowedLibraryIDs: []int{77}}

	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(row), filter)
	probeChainWaitFor(t, 1, target.callCount, "first source target reads")

	h.ScheduleDefaultFileProbe(probeChainTestContent, secondSource, probeChainStreams(row), filter)
	probeChainStableWait(t, 1, target.callCount, "coalesced second-source target reads")

	target.releaseRow()
	h.probeChainWG.Wait()
}

func TestScheduleDefaultFileProbeSkipsStampedRow(t *testing.T) {
	stampedAt := time.Now().Add(-time.Hour)
	row := probeChainRow(302, "virtual://movie/probe-chain?result=c2", &stampedAt)
	h, target, prober := probeChainHandler(row)
	target.releaseRow()

	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(row), catalog.AccessFilter{})
	h.probeChainWG.Wait()

	if prober.count() != 0 {
		t.Fatalf("stamped row was probed %d times; the evidence stamp must skip re-enumeration", prober.count())
	}
	if target.callCount() != 1 {
		t.Fatalf("target reads = %d, want 1", target.callCount())
	}
}

// TestScheduleDefaultFileProbeReprobesAStampedRowWithoutInventory pins the
// repair path for a row whose probe stamp outlived the evidence it stamped. A
// write that could not adopt its candidate (or an invalidation) can leave the
// stamp set with empty track arrays; treating that as "already probed" froze the
// row forever, which is what kept the played 4K file's menu empty while every
// chain skipped it.
func TestScheduleDefaultFileProbeReprobesAStampedRowWithoutInventory(t *testing.T) {
	stampedAt := time.Now().Add(-time.Hour)
	row := probeChainRow(304, "virtual://movie/probe-chain?result=c4", &stampedAt)
	row.AudioTracks = nil
	row.SubtitleTracks = nil
	h, target, prober := probeChainHandler(row)
	target.releaseRow()

	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(row), catalog.AccessFilter{})
	h.probeChainWG.Wait()

	if prober.count() != 1 {
		t.Fatalf("probe calls = %d, want 1: a stamp with no inventory is not evidence", prober.count())
	}
	if got := prober.last(); got != probeChainProviderURL(row) {
		t.Fatalf("probed %q, want the row's own bytes %q", got, probeChainProviderURL(row))
	}
}

func TestScheduleDefaultFileProbeSkipsNeutralGroupRow(t *testing.T) {
	neutral := probeChainRow(303, "virtual://movie/probe-chain", nil)
	h, target, prober := probeChainHandler(neutral)
	target.releaseRow()

	h.ScheduleDefaultFileProbe(probeChainTestContent, neutral, probeChainStreams(neutral), catalog.AccessFilter{})
	h.probeChainWG.Wait()

	if prober.count() != 0 {
		t.Fatalf("neutral group row was probed %d times; it may resolve to a sibling candidate", prober.count())
	}
}

func TestScheduleDefaultFileProbeSkipsDefaultBytesMissingFromListing(t *testing.T) {
	row := probeChainRow(304, "virtual://movie/probe-chain?result=c9", nil)
	h, target, prober := probeChainHandler(row)
	target.releaseRow()

	// The listing names other candidates; the default's own bytes are absent,
	// so a probe would enumerate unverified bytes.
	streams := []VirtualPlaybackStream{
		{URI: "virtual://movie/probe-chain?result=c5", OwnerInstallationID: 5},
	}
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), streams, catalog.AccessFilter{})
	h.probeChainWG.Wait()

	if prober.count() != 0 {
		t.Fatalf("unmatched default row was probed %d times; the chain must never guess bytes", prober.count())
	}
}

func TestScheduleDefaultFileProbeSkipsUnwiredParts(t *testing.T) {
	row := probeChainRow(305, "virtual://movie/probe-chain?result=c5", nil)

	// No target: the chain is inert.
	h := &PlaybackHandler{ServiceContext: context.Background()}
	h.ScheduleDefaultFileProbe(probeChainTestContent, row, probeChainStreams(row), catalog.AccessFilter{})
	h.probeChainWG.Wait()

	// No prober: similarly inert.
	target := &blockedDefaultTarget{open: make(chan struct{}), row: row}
	target.releaseRow()
	h2 := &PlaybackHandler{
		ServiceContext:     context.Background(),
		DefaultProbeTarget: target,
	}
	h2.ScheduleDefaultFileProbe(probeChainTestContent, row, probeChainStreams(row), catalog.AccessFilter{})
	h2.probeChainWG.Wait()
}

func TestScheduleDefaultFileProbeAdmissionBoundsSlotsAndReturnsImmediately(t *testing.T) {
	rowA := probeChainRow(306, "virtual://movie/probe-chain?result=cA", nil)
	rowB := probeChainRow(307, "virtual://movie/probe-chain2?result=cB", nil)
	rowD := probeChainRow(318, "virtual://movie/probe-chain4?result=cD", nil)
	contentB := "movie-probe-chain-b"
	contentD := "movie-probe-chain-d"

	// Targets exist before the chains start, so a release loop can never race
	// a lazily created target into a second blocked state.
	targetA := &blockedDefaultTarget{open: make(chan struct{}), row: rowA}
	targetB := &blockedDefaultTarget{open: make(chan struct{}), row: rowB}
	targetD := &blockedDefaultTarget{open: make(chan struct{}), row: rowD}
	targetD.releaseRow()
	targetsByContent := map[string]*blockedDefaultTarget{
		probeChainTestContent: targetA,
		contentB:              targetB,
		contentD:              targetD,
	}
	prober := &recordingProber{}
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		VirtualPlaybackSourceProber: prober.probe,
	}
	h.DefaultProbeTarget = VirtualDefaultProbeTargetFunc(func(ctx context.Context, contentID string, _ catalog.AccessFilter) (*models.MediaFile, error) {
		tgt := targetsByContent[contentID]
		if tgt == nil {
			return nil, fmt.Errorf("unexpected chain for %q", contentID)
		}
		return tgt.DefaultVirtualProbeTarget(ctx, contentID, catalog.AccessFilter{})
	})

	// Two items fill the slots; their chains sit inside admitted work.
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(rowA), catalog.AccessFilter{})
	h.ScheduleDefaultFileProbe(contentB, &models.MediaFile{ID: 307, ContentID: contentB, FilePath: "virtual://movie/probe-chain2"}, probeChainStreams(rowB), catalog.AccessFilter{})
	probeChainWaitFor(t, 1, targetA.callCount, "chain A entered its default-version read")
	probeChainWaitFor(t, 1, targetB.callCount, "chain B entered its default-version read")

	// The third item is refused: no goroutine spawns (the target would
	// answer "unexpected chain"), so its schedule returns immediately.
	dropped := make(chan struct{})
	go func() {
		h.ScheduleDefaultFileProbe("movie-probe-chain-c", &models.MediaFile{ID: 308, ContentID: "movie-probe-chain-c", FilePath: "virtual://movie/probe-chain3"}, probeChainStreams(rowA), catalog.AccessFilter{})
		close(dropped)
	}()
	select {
	case <-dropped:
	case <-time.After(2 * time.Second):
		t.Fatal("saturated admission blocked the discovery caller")
	}

	// Releasing both slots frees capacity: a later chain is admitted and
	// completes end to end.
	targetA.releaseRow()
	targetB.releaseRow()
	h.probeChainWG.Wait()
	prober4 := &recordingProber{hits: make(chan struct{}, 1)}
	h.VirtualPlaybackSourceProber = prober4.probe
	h.ScheduleDefaultFileProbe(contentD, &models.MediaFile{ID: 318, ContentID: contentD, FilePath: "virtual://movie/probe-chain4"}, probeChainStreams(rowD), catalog.AccessFilter{})
	select {
	case <-prober4.hits:
	case <-time.After(2 * time.Second):
		t.Fatal("a chain admitted after slots freed did not probe")
	}
	h.probeChainWG.Wait()
}

func TestScheduleDefaultFileProbeStallTeriminatesAndRestoresSlot(t *testing.T) {
	prevBudget := virtualDefaultProbeChainBudget
	virtualDefaultProbeChainBudget = 150 * time.Millisecond
	t.Cleanup(func() { virtualDefaultProbeChainBudget = prevBudget })

	row := probeChainRow(309, "virtual://movie/probe-chain?result=cS", nil)
	h, target, prober := probeChainHandler(row)

	// The target never opens its gate; it returns only when the chain context
	// deadline fires. The goroutine must actually terminate and free the slot,
	// not merely let a deadline lapse while the read continues.
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(row), catalog.AccessFilter{})
	h.probeChainWG.Wait()

	if target.callCount() != 1 {
		t.Fatalf("stalled chain target reads = %d, want 1", target.callCount())
	}
	if prober.count() != 0 {
		t.Fatalf("stalled chain probed %d times", prober.count())
	}

	// The slot is free again: a well-behaved retry is admitted immediately.
	row2 := probeChainRow(319, "virtual://movie/probe-chain?result=cR", nil)
	prober2 := &recordingProber{hits: make(chan struct{}, 1)}
	target.row = row2
	target.releaseRow()
	h.VirtualPlaybackSourceProber = prober2.probe
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(row2), catalog.AccessFilter{})
	select {
	case <-prober2.hits:
	case <-time.After(2 * time.Second):
		t.Fatal("a well-behaved chain after a stall did not probe")
	}
	h.probeChainWG.Wait()
	if got := prober2.last(); got != probeChainProviderURL(row2) {
		t.Fatalf("retry probed %q, want the default row's resolved bytes %q", got, probeChainProviderURL(row2))
	}
}

// TestListSourceSchedulesChainAfterPersistOnlyForDiscover pins both the
// scheduling point (successful persistence inside the detached shared work,
// never the request side) and the chaining eligibility (discovery only, never
// the operator-driven :refresh path).
func TestListSourceSchedulesChainAfterPersistOnlyForDiscover(t *testing.T) {
	row := probeChainRow(310, "virtual://movie/probe-chain?result=cD", nil)
	streams := probeChainStreams(row)

	fired := make(chan []VirtualPlaybackStream, 4)
	svc := &VirtualCandidatesRefreshService{
		ListCached: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return streams, nil
		}),
		ListFresh: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return streams, nil
		}),
		Persist: func(context.Context, *models.MediaFile, []VirtualPlaybackStream) error { return nil },
	}
	svc.ScheduleDefaultFileProbe = func(contentID string, source *models.MediaFile, got []VirtualPlaybackStream, _ catalog.AccessFilter) {
		passed := make([]VirtualPlaybackStream, len(got))
		copy(passed, got)
		fired <- passed
	}

	if _, err := svc.listSource(context.Background(), row, 2, "p-1", svc.ListCached, probeChainTestContent, catalog.AccessFilter{}); err != nil {
		t.Fatalf("discover listSource: %v", err)
	}
	select {
	case got := <-fired:
		if len(got) != 2 || got[1].URI != row.FilePath {
			t.Fatalf("probe chain received %d streams; want the persisted listing snapshot", len(got))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("discover persist did not schedule the probe chain")
	}

	if _, err := svc.listSource(context.Background(), row, 2, "p-1", svc.ListFresh, "", catalog.AccessFilter{}); err != nil {
		t.Fatalf("refresh listSource: %v", err)
	}
	select {
	case stray := <-fired:
		t.Fatalf("refresh persist scheduled a probe chain (%d streams); only discovery schedules", len(stray))
	default:
	}
}

// TestDiscoverScheduleSurvivesWaiterDisconnect proves the winner's success is
// the schedule trigger: when the originating request's context is already
// dead, the detached persistence still lands and still schedules a chain.
func TestDiscoverScheduleSurvivesWaiterDisconnect(t *testing.T) {
	row := probeChainRow(312, "virtual://movie/probe-chain?result=cW", nil)
	streams := probeChainStreams(row)

	h, target, prober := probeChainHandler(row)
	target.releaseRow()

	svc := &VirtualCandidatesRefreshService{
		ListCached: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return streams, nil
		}),
		Persist: func(context.Context, *models.MediaFile, []VirtualPlaybackStream) error { return nil },
	}
	svc.ScheduleDefaultFileProbe = h.ScheduleDefaultFileProbe

	deadCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := svc.listSource(deadCtx, probeChainSourceRow(), 2, "p-1", svc.ListCached, probeChainTestContent, catalog.AccessFilter{}); err != nil {
		t.Fatalf("listSource with a dead caller context: %v", err)
	}
	h.probeChainWG.Wait()
	if prober.count() != 1 {
		t.Fatalf("probe calls after waiter disconnect = %d, want 1 (chain runs detached from the request)", prober.count())
	}
}

func TestDefaultProbeChainAdmissionIsAtomicWithoutGoroutines(t *testing.T) {
	var admission defaultProbeChainAdmission
	releaseA, refusal, _ := admission.acquire("movie-a", false)
	if refusal != defaultProbeChainRefusalNone {
		t.Fatalf("empty admission refused a fresh chain: %q", refusal)
	}
	if _, refusal, _ := admission.acquire("movie-a", false); refusal != defaultProbeChainRefusalLive {
		t.Fatalf("a live chain did not deduplicate its item: %q", refusal)
	}
	releaseB, refusal, _ := admission.acquire("movie-b", false)
	if refusal != defaultProbeChainRefusalNone || releaseB == nil {
		t.Fatalf("second distinct item did not admit within the bound: %q", refusal)
	}
	releaseA()
	releaseC, refusal, _ := admission.acquire("movie-c", false)
	if refusal != defaultProbeChainRefusalNone {
		t.Fatalf("a released chain kept its slot reserved: %q", refusal)
	}
	// A double release must not hand out the same slot twice: with both slots
	// legitimately live (b and c), nothing else is admissible.
	releaseA()
	if _, refusal, _ := admission.acquire("movie-d", false); refusal != defaultProbeChainRefusalCapacity {
		t.Fatalf("a double release freed an extra slot: %q", refusal)
	}
	releaseC()
	releaseD, refusal, _ := admission.acquire("movie-d", false)
	if refusal != defaultProbeChainRefusalNone {
		t.Fatalf("a released slot was not reusable: %q", refusal)
	}
	releaseB()
	releaseD()
}

// TestScheduleDefaultFileProbeSurvivesPanickingWork pins the panic guard at
// the detached goroutine boundary: this is optional best-effort work, so an
// unexpected panic — in the default-version read or in the probe itself —
// unwinds into a logged observation and releases every shared slot, and a
// later chain still runs.
func TestScheduleDefaultFileProbeSurvivesPanickingWork(t *testing.T) {
	prevBudget := virtualDefaultProbeChainBudget
	virtualDefaultProbeChainBudget = time.Second
	t.Cleanup(func() { virtualDefaultProbeChainBudget = prevBudget })

	// Panic inside the default-version read.
	row := probeChainRow(320, "virtual://movie/probe-chain?result=cP1", nil)
	panickingTarget := VirtualDefaultProbeTargetFunc(func(context.Context, string, catalog.AccessFilter) (*models.MediaFile, error) {
		panic("target blew up")
	})
	prober := &recordingProber{}
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		DefaultProbeTarget:          panickingTarget,
		VirtualPlaybackSourceProber: prober.probe,
	}
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(row), catalog.AccessFilter{})
	h.probeChainWG.Wait()

	// The slot is reusable regardless of the panic.
	reworked := &blockedDefaultTarget{open: make(chan struct{}), row: row}
	reworked.releaseRow()
	h.DefaultProbeTarget = reworked
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(row), catalog.AccessFilter{})
	probeChainWaitFor(t, 1, prober.count, "chain after a panicking target")

	// Panic inside the probe: the chain must contain it, and both the
	// admission slot and the shared probe gate must come back. The target
	// answers the same row the listing names, so the chain actually reaches
	// the prober this time.
	probingRow := probeChainRow(321, "virtual://movie/probe-chain?result=cP2", nil)
	probedTarget := &blockedDefaultTarget{open: make(chan struct{}), row: probingRow}
	probedTarget.releaseRow()
	h.probeChainWG.Wait()
	proberEntered := &atomic.Int32{}
	explodingProber := func(context.Context, string, *models.MediaFile) (*models.MediaFile, error) {
		proberEntered.Add(1)
		panic("prober blew up")
	}
	h.DefaultProbeTarget = probedTarget
	h.VirtualPlaybackSourceProber = explodingProber
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(probingRow), catalog.AccessFilter{})
	h.probeChainWG.Wait()
	if proberEntered.Load() != 1 {
		t.Fatalf("panicking prober entered %d times, want exactly 1", proberEntered.Load())
	}
	// The crashing chain must not have kept a gate slot: after it settles,
	// every slot of the shared gate is free again.
	gate := h.detachedGate()
	for i := 0; i < gate.capacity(); i++ {
		if !gate.tryAcquire() {
			t.Fatalf("probe gate kept %d of %d slots free after a panicking chain", i, gate.capacity())
		}
	}
	for i := 0; i < gate.capacity(); i++ {
		gate.release()
	}
	// A retry on the same item then runs normally.
	h.VirtualPlaybackSourceProber = prober.probe
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(probingRow), catalog.AccessFilter{})
	probeChainWaitFor(t, 2, prober.count, "chain after a panicking prober")
	h.probeChainWG.Wait()
}

// TestScheduleDefaultFileProbeSnapshotsMutableStreams pins the input
// snapshot: mutating the caller's request-header map after scheduling must
// not change what the chain's probe reads, because the chain deep-copies the
// mutable members of every listing entry it hands off.
func TestScheduleDefaultFileProbeSnapshotsMutableStreams(t *testing.T) {
	row := probeChainRow(322, "virtual://movie/probe-chain?result=cH", nil)
	target := &blockedDefaultTarget{open: make(chan struct{}), row: row}
	target.releaseRow()

	var seenHeaders map[string]string
	headerProber := func(_ context.Context, sourceURL string, file *models.MediaFile, headers map[string]string) (*models.MediaFile, error) {
		seenHeaders = map[string]string{}
		maps.Copy(seenHeaders, headers)
		return &models.MediaFile{FilePath: sourceURL, Duration: 5400}, nil
	}
	h := &PlaybackHandler{
		ServiceContext:                         context.Background(),
		DefaultProbeTarget:                     target,
		VirtualPlaybackSourceProberWithHeaders: headerProber,
	}

	streams := probeChainStreams(row)
	streams[1].RequestHeaders = map[string]string{"User-Agent": "discovery-snapshotted"}
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), streams, catalog.AccessFilter{})
	// The caller mutates its map after the hand-off; the chain must still
	// probe with the value scheduled.
	streams[1].RequestHeaders["User-Agent"] = "mutated-after-hand-off"
	h.probeChainWG.Wait()

	if got := seenHeaders["User-Agent"]; got != "discovery-snapshotted" {
		t.Fatalf("prober headers = %q, want the value captured at scheduling", got)
	}
}

// probeChainVersionReader is the production target's catalog seam for these
// tests. It applies the real access filter to its file rows — the way
// DetailService.GetItemVersions does before returning versions — and records
// every filter it was handed and every file row it was asked to read.
type probeChainVersionReader struct {
	mu      sync.Mutex
	files   []*models.MediaFile
	rows    map[int]*models.MediaFile
	filters []catalog.AccessFilter
	reads   []int
}

func (r *probeChainVersionReader) GetItemVersions(_ context.Context, _ string, filter catalog.AccessFilter) ([]catalog.FileVersion, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.filters = append(r.filters, filter)
	visible := catalog.FilterMediaFilesByAccess(r.files, filter)
	versions := make([]catalog.FileVersion, 0, len(visible))
	for _, file := range visible {
		versions = append(versions, catalog.FileVersion{FileID: file.ID})
	}
	return versions, nil
}

func (r *probeChainVersionReader) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads = append(r.reads, id)
	return r.rows[id], nil
}

func (r *probeChainVersionReader) snapshot() ([]catalog.AccessFilter, []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.filters), slices.Clone(r.reads)
}

// TestScheduleDefaultFileProbeHonorsTheDiscoveryAccessFilter pins the target
// lookup to the caller's access filter. The catalog read lists the hidden
// library's version first; with the filter threaded through, the target
// resolves and probes the visible default and never even reads the hidden
// row's file. An unfiltered lookup — the bug this guards — would probe the
// hidden version instead.
func TestScheduleDefaultFileProbeHonorsTheDiscoveryAccessFilter(t *testing.T) {
	const visibleLibrary, hiddenLibrary = 77, 78
	visible := &models.MediaFile{
		ID: 330, ContentID: probeChainTestContent, FilePath: "virtual://movie/probe-chain?result=visible",
		MediaFolderID: visibleLibrary, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
		UpdatedAt: time.Now(),
	}
	hidden := &models.MediaFile{
		ID: 331, ContentID: probeChainTestContent, FilePath: "virtual://movie/probe-chain?result=hidden",
		MediaFolderID: hiddenLibrary, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
		UpdatedAt: time.Now(),
	}
	visible.ResolvedURL = probeChainProviderURL(visible)
	hidden.ResolvedURL = probeChainProviderURL(hidden)
	reader := &probeChainVersionReader{
		// The hidden version sorts first, so an unfiltered read would pick it.
		files: []*models.MediaFile{hidden, visible},
		rows:  map[int]*models.MediaFile{visible.ID: visible, hidden.ID: hidden},
	}
	prober := &recordingProber{}
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		DefaultProbeTarget:          NewVirtualDefaultProbeTarget(reader, reader),
		VirtualPlaybackSourceProber: prober.probe,
	}
	filter := catalog.AccessFilter{
		AllowedLibraryIDs:         []int{visibleLibrary},
		DisabledLibraryIDs:        []int{99},
		PresentationLibraryID:     new(visibleLibrary),
		MetadataLanguageOverrides: map[string]string{"en": "fr"},
		MaturityLimits:            access.MaturityLimits{MaxContentRating: "PG-13"},
	}
	streams := []VirtualPlaybackStream{
		{URI: hidden.FilePath, OwnerInstallationID: 5},
		{URI: visible.FilePath, OwnerInstallationID: 5},
	}

	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), streams, filter)
	h.probeChainWG.Wait()

	filters, reads := reader.snapshot()
	if len(reads) != 1 || reads[0] != visible.ID {
		t.Fatalf("file rows read = %v, want only the visible version %d", reads, visible.ID)
	}
	if prober.count() != 1 || prober.last() != probeChainProviderURL(visible) {
		t.Fatalf("probed %q (%d probes), want the visible default %q", prober.last(), prober.count(), probeChainProviderURL(visible))
	}
	if len(filters) != 1 {
		t.Fatalf("catalog version reads = %d, want 1", len(filters))
	}
	got := filters[0]
	if !slices.Equal(got.AllowedLibraryIDs, filter.AllowedLibraryIDs) ||
		!slices.Equal(got.DisabledLibraryIDs, filter.DisabledLibraryIDs) ||
		got.PresentationLibraryID == nil || *got.PresentationLibraryID != visibleLibrary ||
		got.MetadataLanguageOverrides["en"] != "fr" ||
		got.MaxContentRating != "PG-13" {
		t.Fatalf("catalog read filter = %+v, want the discovery request's filter %+v", got, filter)
	}
}

// A selected profile variant that vanished from the fresh listing is probed
// from its own persisted candidate. That candidate can carry an expired signed
// URL copied from the selected row; it must be resolved afresh rather than
// probing the expired transport and marking the candidate failed.
func TestVirtualChainProbeURLRefreshesExpiredSelectedCandidate(t *testing.T) {
	const freshURL = "https://probe.example/fresh-selected"
	past := time.Now().Add(-time.Hour)
	row := probeChainRow(7001, "virtual://movie/probe-chain?result=selected", nil)
	row.ResolvedURL = "https://probe.example/expired-selected"
	row.ResolvedURLExpiresAt = &past
	cand := virtualPlaybackStreamFromFile(row)
	if cand == nil {
		t.Fatal("selected candidate was not built")
	}
	calls := 0
	h := &PlaybackHandler{VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
		calls++
		return ResolvedVirtualMedia{URL: freshURL}, nil
	})}

	url, _, ok := h.virtualChainProbeURL(context.Background(), row, *cand, catalog.AccessFilter{UserID: 1, ProfileID: "profile-1"})
	if !ok {
		t.Fatal("expired selected candidate produced no probe URL")
	}
	if url != freshURL {
		t.Fatalf("probe URL = %q, want the fresh resolve %q", url, freshURL)
	}
	if calls != 1 {
		t.Fatalf("fresh resolver calls = %d, want 1", calls)
	}
}

// A stored URL whose own expiry has passed must not be probed: the chain
// resolves a fresh token and records it through the ordinary stored-URL
// refresh write, so the next chain or play reuses it instead of paying another
// provider round-trip (or letting the provider fall back to title recovery on
// the dead token).
func TestVirtualChainProbeURLResolvesAndPersistsExpiredStoredURL(t *testing.T) {
	const freshURL = "https://probe.example/fresh-stored"
	row := probeChainRow(7002, "virtual://movie/probe-chain?result=stored", nil)
	past := time.Now().Add(-time.Hour)
	row.ResolvedURL = "https://probe.example/expired-stored"
	row.ResolvedURLExpiresAt = &past
	cand := VirtualPlaybackStream{URI: row.FilePath, OwnerInstallationID: row.VirtualOwnerInstallationID}

	newExpiry := time.Now().Add(30 * time.Minute)
	resolveCalls := 0
	var saved []models.VirtualFilePersistArgs
	h := &PlaybackHandler{
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			resolveCalls++
			return ResolvedVirtualMedia{
				URL: freshURL, URI: row.FilePath, CandidateID: virtualResultCandidateID(row.FilePath),
				ExpiresAt: newExpiry,
			}, nil
		}),
		VirtualFileMetadataSaver: func(_ context.Context, args models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
			saved = append(saved, args)
			return VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}, nil
		},
	}

	url, _, ok := h.virtualChainProbeURL(context.Background(), row, cand, catalog.AccessFilter{UserID: 1, ProfileID: "profile-1"})
	if !ok {
		t.Fatal("an expired stored URL produced no probe URL")
	}
	if url != freshURL {
		t.Fatalf("probe URL = %q, want the fresh resolve %q", url, freshURL)
	}
	if resolveCalls != 1 {
		t.Fatalf("fresh resolver calls = %d, want 1", resolveCalls)
	}
	if len(saved) != 1 {
		t.Fatalf("stored-URL writes = %d, want the fresh token persisted once", len(saved))
	}
	if saved[0].FileID != row.ID || saved[0].ExpectedFilePath != row.FilePath {
		t.Fatalf("persisted row = id %d path %q, want id %d path %q",
			saved[0].FileID, saved[0].ExpectedFilePath, row.ID, row.FilePath)
	}
	if saved[0].ResolvedURL != freshURL {
		t.Fatalf("persisted URL = %q, want %q", saved[0].ResolvedURL, freshURL)
	}
	if saved[0].ResolvedURLExpiresAt == nil || !saved[0].ResolvedURLExpiresAt.Equal(newExpiry) {
		t.Fatalf("persisted expiry = %v, want %v", saved[0].ResolvedURLExpiresAt, newExpiry)
	}
}

// A stored URL that is still inside its own expiry is used as-is: the chain
// must not spend a provider round-trip refreshing a token nobody doubts.
func TestVirtualChainProbeURLUsesFreshStoredURLWithoutResolving(t *testing.T) {
	row := probeChainRow(7003, "virtual://movie/probe-chain?result=fresh", nil)
	future := time.Now().Add(time.Hour)
	row.ResolvedURL = "https://probe.example/fresh-stored"
	row.ResolvedURLExpiresAt = &future
	cand := VirtualPlaybackStream{URI: row.FilePath, OwnerInstallationID: row.VirtualOwnerInstallationID}

	h := &PlaybackHandler{
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			t.Fatal("a fresh stored URL must not trigger a resolve")
			return ResolvedVirtualMedia{}, nil
		}),
	}

	url, _, ok := h.virtualChainProbeURL(context.Background(), row, cand, catalog.AccessFilter{})
	if !ok {
		t.Fatal("a fresh stored URL produced no probe URL")
	}
	if url != row.ResolvedURL {
		t.Fatalf("probe URL = %q, want the stored URL %q", url, row.ResolvedURL)
	}
}

// The infinite-trust regression: a row that carries no expiry at all is not a
// row that never expires. An old one must be re-resolved and the new token
// persisted, exactly like an expired one.
func TestVirtualChainProbeURLResolvesNilExpiryStoredURLPastAgeBound(t *testing.T) {
	const freshURL = "https://probe.example/fresh-ageless"
	row := probeChainRow(7004, "virtual://movie/probe-chain?result=ageless", nil)
	row.ResolvedURL = "https://probe.example/ancient-stored"
	row.ResolvedURLExpiresAt = nil
	row.UpdatedAt = time.Now().Add(-virtualChainStoredURLMaxAge - time.Minute)
	cand := VirtualPlaybackStream{URI: row.FilePath, OwnerInstallationID: row.VirtualOwnerInstallationID}

	var saved []models.VirtualFilePersistArgs
	resolveCalls := 0
	h := &PlaybackHandler{
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			resolveCalls++
			return ResolvedVirtualMedia{URL: freshURL, URI: row.FilePath, CandidateID: virtualResultCandidateID(row.FilePath)}, nil
		}),
		VirtualFileMetadataSaver: func(_ context.Context, args models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
			saved = append(saved, args)
			return VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}, nil
		},
	}

	url, _, ok := h.virtualChainProbeURL(context.Background(), row, cand, catalog.AccessFilter{})
	if !ok {
		t.Fatal("an over-age nil-expiry stored URL produced no probe URL")
	}
	if url != freshURL {
		t.Fatalf("probe URL = %q, want the fresh resolve %q", url, freshURL)
	}
	if resolveCalls != 1 {
		t.Fatalf("fresh resolver calls = %d, want 1", resolveCalls)
	}
	if len(saved) != 1 || saved[0].ResolvedURL != freshURL {
		t.Fatalf("persisted writes = %d (%v), want the fresh token recorded once", len(saved), saved)
	}
}

// Inside the age bound a nil-expiry token is still trusted, so a browse-then-
// play right after a listing does not pay a second provider listing.
func TestVirtualChainProbeURLUsesNilExpiryStoredURLInsideAgeBound(t *testing.T) {
	row := probeChainRow(7005, "virtual://movie/probe-chain?result=ageless-fresh", nil)
	row.ResolvedURL = "https://probe.example/recent-stored"
	row.ResolvedURLExpiresAt = nil
	row.UpdatedAt = time.Now().Add(-virtualChainStoredURLMaxAge / 2)
	cand := VirtualPlaybackStream{URI: row.FilePath, OwnerInstallationID: row.VirtualOwnerInstallationID}

	h := &PlaybackHandler{
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			t.Fatal("a stored URL inside the age bound must not trigger a resolve")
			return ResolvedVirtualMedia{}, nil
		}),
	}

	url, _, ok := h.virtualChainProbeURL(context.Background(), row, cand, catalog.AccessFilter{})
	if !ok {
		t.Fatal("a recent nil-expiry stored URL produced no probe URL")
	}
	if url != row.ResolvedURL {
		t.Fatalf("probe URL = %q, want the stored URL %q", url, row.ResolvedURL)
	}
}

// A matched listing entry whose own URL has already lapsed is evidence that
// the token minted from that listing is dead, so the row's nil-expiry stored
// URL is stale too even though the row itself looks recent.
func TestVirtualChainProbeURLTreatsExpiredListingAsStoredURLStale(t *testing.T) {
	const freshURL = "https://probe.example/fresh-listing"
	row := probeChainRow(7006, "virtual://movie/probe-chain?result=listing", nil)
	row.ResolvedURL = "https://probe.example/stored-listing"
	row.ResolvedURLExpiresAt = nil
	past := time.Now().Add(-time.Minute)
	cand := VirtualPlaybackStream{
		URI: row.FilePath, OwnerInstallationID: row.VirtualOwnerInstallationID,
		ProviderURL: "https://probe.example/expired-listing", ProviderExpiresAt: &past,
	}

	resolveCalls := 0
	h := &PlaybackHandler{
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			resolveCalls++
			return ResolvedVirtualMedia{URL: freshURL}, nil
		}),
	}

	url, _, ok := h.virtualChainProbeURL(context.Background(), row, cand, catalog.AccessFilter{})
	if !ok {
		t.Fatal("an expired listing produced no probe URL")
	}
	if url != freshURL {
		t.Fatalf("probe URL = %q, want the fresh resolve %q", url, freshURL)
	}
	if resolveCalls != 1 {
		t.Fatalf("fresh resolver calls = %d, want 1", resolveCalls)
	}
}

// A persisted selected candidate built from the row carries the row's URL, so
// it obeys the same age bound: a row past the bound must not keep a
// long-lived token alive through the candidate's ProviderURL.
func TestVirtualChainProbeURLResolvesPersistedSelectedCandidatePastAgeBound(t *testing.T) {
	const freshURL = "https://probe.example/fresh-selected-old"
	row := probeChainRow(7007, "virtual://movie/probe-chain?result=selected-old", nil)
	row.ResolvedURL = "https://probe.example/ancient-selected"
	row.ResolvedURLExpiresAt = nil
	row.UpdatedAt = time.Now().Add(-virtualChainStoredURLMaxAge - time.Hour)
	cand := virtualPlaybackStreamFromFile(row)
	if cand == nil {
		t.Fatal("selected candidate was not built")
	}

	resolveCalls := 0
	h := &PlaybackHandler{
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			resolveCalls++
			return ResolvedVirtualMedia{URL: freshURL}, nil
		}),
	}

	url, _, ok := h.virtualChainProbeURL(context.Background(), row, *cand, catalog.AccessFilter{})
	if !ok {
		t.Fatal("an over-age persisted selected candidate produced no probe URL")
	}
	if url != freshURL {
		t.Fatalf("probe URL = %q, want the fresh resolve %q", url, freshURL)
	}
	if resolveCalls != 1 {
		t.Fatalf("fresh resolver calls = %d, want 1", resolveCalls)
	}
}

// TestVirtualChainStoredURLTrusted pins the staleness rule itself at its
// boundaries, including the nil-expiry cases the production bug lived in.
func TestVirtualChainStoredURLTrusted(t *testing.T) {
	now := time.Now()
	const storedURL = "https://probe.example/stored"
	future := now.Add(time.Hour)
	boundary := now.Add(-virtualChainStoredURLMaxAge)

	cases := []struct {
		name           string
		row            *models.MediaFile
		listingExpired bool
		want           bool
	}{
		{name: "nil row", row: nil, want: false},
		{name: "no stored URL", row: &models.MediaFile{UpdatedAt: now}, want: false},
		{
			name: "future expiry",
			row:  &models.MediaFile{ResolvedURL: storedURL, ResolvedURLExpiresAt: &future, UpdatedAt: now},
			want: true,
		},
		{
			name: "past expiry",
			row:  &models.MediaFile{ResolvedURL: storedURL, ResolvedURLExpiresAt: timePtr(now.Add(-time.Second)), UpdatedAt: now},
			want: false,
		},
		{
			name: "expiry equals now",
			row:  &models.MediaFile{ResolvedURL: storedURL, ResolvedURLExpiresAt: &now, UpdatedAt: now},
			want: false,
		},
		{
			name: "nil expiry exactly at the age bound",
			row:  &models.MediaFile{ResolvedURL: storedURL, UpdatedAt: boundary},
			want: true,
		},
		{
			name: "nil expiry one nanosecond past the age bound",
			row:  &models.MediaFile{ResolvedURL: storedURL, UpdatedAt: boundary.Add(-time.Nanosecond)},
			want: false,
		},
		{
			name: "nil expiry with no row timestamp",
			row:  &models.MediaFile{ResolvedURL: storedURL},
			want: false,
		},
		{
			name:           "known-expired listing makes a fresh nil-expiry URL stale",
			row:            &models.MediaFile{ResolvedURL: storedURL, UpdatedAt: now},
			listingExpired: true,
			want:           false,
		},
		{
			name:           "known-expired listing leaves a future expiry expired anyway",
			row:            &models.MediaFile{ResolvedURL: storedURL, ResolvedURLExpiresAt: timePtr(now.Add(-time.Second)), UpdatedAt: now},
			listingExpired: true,
			want:           false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := virtualChainStoredURLTrusted(tc.row, now, tc.listingExpired); got != tc.want {
				t.Fatalf("virtualChainStoredURLTrusted = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestScheduleDefaultFileProbeSnapshotsAccessFilter pins the filter snapshot:
// the request that scheduled the chain may reuse or mutate its filter after
// the hand-off, and the detached read must still see the constraints that
// authorized the discovery run.
func TestScheduleDefaultFileProbeSnapshotsAccessFilter(t *testing.T) {
	row := probeChainRow(332, "virtual://movie/probe-chain?result=cAF", nil)
	h, target, _ := probeChainHandler(row)

	filter := catalog.AccessFilter{
		AllowedLibraryIDs:         []int{76},
		MetadataLanguageOverrides: map[string]string{"en": "fr"},
	}
	h.ScheduleDefaultFileProbe(probeChainTestContent, probeChainSourceRow(), probeChainStreams(row), filter)
	probeChainWaitFor(t, 1, target.callCount, "chain entered its default-version read")

	filter.AllowedLibraryIDs[0] = 99
	filter.MetadataLanguageOverrides["en"] = "de"
	target.releaseRow()
	h.probeChainWG.Wait()

	got := target.lastFilter()
	if len(got.AllowedLibraryIDs) != 1 || got.AllowedLibraryIDs[0] != 76 {
		t.Fatalf("chain read library scope = %v, want the value captured at scheduling", got.AllowedLibraryIDs)
	}
	if got.MetadataLanguageOverrides["en"] != "fr" {
		t.Fatalf("chain read language overrides = %v, want the value captured at scheduling", got.MetadataLanguageOverrides)
	}
}

// TestScheduleSelectedFileProbeProbesTheSelectedRow pins probe-on-select: an
// explicit selection schedules the shared chain for the row it names, never the
// resolved default, and a row that already carries probe evidence schedules no
// probe at all.
func TestScheduleSelectedFileProbeProbesTheSelectedRow(t *testing.T) {
	defaultRow := probeChainRow(740, "virtual://movie/probe-chain?result=default", nil)
	selectedRow := probeChainRow(741, "virtual://movie/probe-chain?result=selected", nil)
	reader := &probeChainVersionReader{
		files: []*models.MediaFile{defaultRow, selectedRow},
		rows:  map[int]*models.MediaFile{defaultRow.ID: defaultRow, selectedRow.ID: selectedRow},
	}
	prober := &recordingProber{}
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		DefaultProbeTarget:          NewVirtualDefaultProbeTarget(reader, reader),
		VirtualPlaybackSourceProber: prober.probe,
	}

	h.ScheduleSelectedFileProbe(probeChainTestContent, selectedRow.ID, catalog.AccessFilter{AllowedLibraryIDs: []int{77}})
	h.probeChainWG.Wait()

	if prober.count() != 1 || prober.last() != probeChainProviderURL(selectedRow) {
		t.Fatalf("probes = %d, last = %q; want exactly the selected row %q", prober.count(), prober.last(), probeChainProviderURL(selectedRow))
	}
	if _, reads := reader.snapshot(); len(reads) != 1 || reads[0] != selectedRow.ID {
		t.Fatalf("file rows read = %v, want only the selected row %d", reads, selectedRow.ID)
	}

	// Fresh inventory: the same selection must schedule nothing to probe. The
	// stamp is only evidence when it describes the tracks the chain measured.
	probedAt := time.Now()
	selectedRow.ProbeUpdatedAt = &probedAt
	selectedRow.AudioTracks = []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}}
	h.ScheduleSelectedFileProbe(probeChainTestContent, selectedRow.ID, catalog.AccessFilter{AllowedLibraryIDs: []int{77}})
	h.probeChainWG.Wait()
	if prober.count() != 1 {
		t.Fatalf("probes after a selected row with fresh inventory = %d, want no new probe", prober.count())
	}
}

// TestScheduleSelectedFileProbePublishesInventoryWithoutAPlayback pins the
// browse-then-select path end to end: the only event that happened is a
// watch-detail read naming one version, the chain verified the row's bytes, and
// the inventory reaches the open item page through the versions_updated publish
// the evidence write already triggers. Nothing here can have committed a play:
// the handler has no session manager, so there is no playback session to write,
// and the chain is not part of any playback path (it is scheduled from the read
// that renders the menu).
//
// This is the delivery half of the item-page latency measurement: the selection
// probe's own result has to reach the browse surface when it verifies, rather
// than waiting for a post-commit probe minutes later.
func TestScheduleSelectedFileProbePublishesInventoryWithoutAPlayback(t *testing.T) {
	defaultRow := probeChainRow(760, "virtual://movie/probe-chain?result=default", nil)
	selectedRow := probeChainRow(761, "virtual://movie/probe-chain?result=selected", nil)
	reader := &probeChainVersionReader{
		files: []*models.MediaFile{defaultRow, selectedRow},
		rows:  map[int]*models.MediaFile{defaultRow.ID: defaultRow, selectedRow.ID: selectedRow},
	}
	bus := &recordingEventBus{}
	hub := notifications.NewHub("probe-chain-selection-event", bus)
	prober := &recordingProber{}
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		EventsHub:                   hub.EventsHub(),
		DefaultProbeTarget:          NewVirtualDefaultProbeTarget(reader, reader),
		VirtualPlaybackSourceProber: prober.probe,
		VirtualFileMetadataSaver: func(context.Context, models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
			return VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}, nil
		},
	}

	h.ScheduleSelectedFileProbe(probeChainTestContent, selectedRow.ID, catalog.AccessFilter{AllowedLibraryIDs: []int{77}})
	if payload := waitForRecordingEvent(t, bus, 2*time.Second, "catalog.item.changed", "versions_updated", probeChainTestContent); payload == "" {
		t.Fatal("a verified selection probe did not publish versions_updated to the open item page")
	}
	h.probeChainWG.Wait()
	if prober.count() != 1 || prober.last() != probeChainProviderURL(selectedRow) {
		t.Fatalf("probes = %d, last = %q; want exactly the selected row %q", prober.count(), prober.last(), probeChainProviderURL(selectedRow))
	}
}

// TestScheduleSelectedFileWarmStartsProviderFetchForProbedSelection pins
// warm-on-select: an explicit selection hands the row's virtual URI to the same
// fill playback start uses, even when the row already carries probe evidence,
// and the probe chain stays untouched.
func TestScheduleSelectedFileWarmStartsProviderFetchForProbedSelection(t *testing.T) {
	probedAt := time.Now()
	selectedRow := probeChainRow(742, "virtual://movie/probe-chain?result=warm", &probedAt)
	reader := &probeChainVersionReader{
		files: []*models.MediaFile{selectedRow},
		rows:  map[int]*models.MediaFile{selectedRow.ID: selectedRow},
	}
	prober := &recordingProber{}
	var mu sync.Mutex
	var warmCalls []recordedWarmCall
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		DefaultProbeTarget:          NewVirtualDefaultProbeTarget(reader, reader),
		VirtualPlaybackSourceProber: prober.probe,
		VirtualReleaseCacheWarmup: VirtualReleaseCacheFillFunc(func(_ context.Context, virtualURI string, ownerID int, _ func(playback.DownloadProgressPayload)) error {
			mu.Lock()
			warmCalls = append(warmCalls, recordedWarmCall{virtualURI: virtualURI, ownerID: ownerID})
			mu.Unlock()
			return nil
		}),
	}

	h.ScheduleSelectedFileWarm(probeChainTestContent, selectedRow.ID, catalog.AccessFilter{AllowedLibraryIDs: []int{77}})
	h.warmChainWG.Wait()

	mu.Lock()
	got := slices.Clone(warmCalls)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("warm-ups = %d, want exactly 1", len(got))
	}
	if got[0].virtualURI != selectedRow.FilePath || got[0].ownerID != selectedRow.VirtualOwnerInstallationID {
		t.Fatalf("warm-up = %+v, want the selected row's URI %q and owner %d",
			got[0], selectedRow.FilePath, selectedRow.VirtualOwnerInstallationID)
	}
	if prober.count() != 0 {
		t.Fatalf("probes = %d, want none: a probed row warms without re-probing", prober.count())
	}
}

// TestScheduleSelectedFileWarmCoalescesLiveWarmUps pins the admission contract:
// a second explicit selection while the first warm-up is live is dropped rather
// than spawning a second target read or a second fill.
func TestScheduleSelectedFileWarmCoalescesLiveWarmUps(t *testing.T) {
	selectedRow := probeChainRow(743, "virtual://movie/probe-chain?result=warm-live", nil)
	target := &blockedDefaultTarget{open: make(chan struct{}), row: selectedRow}
	var mu sync.Mutex
	warmCount := 0
	h := &PlaybackHandler{
		ServiceContext:     context.Background(),
		DefaultProbeTarget: target,
		VirtualReleaseCacheWarmup: VirtualReleaseCacheFillFunc(func(_ context.Context, _ string, _ int, _ func(playback.DownloadProgressPayload)) error {
			mu.Lock()
			warmCount++
			mu.Unlock()
			return nil
		}),
	}

	h.ScheduleSelectedFileWarm(probeChainTestContent, selectedRow.ID, catalog.AccessFilter{})
	probeChainWaitFor(t, 1, target.callCount, "target reads")
	h.ScheduleSelectedFileWarm(probeChainTestContent, selectedRow.ID, catalog.AccessFilter{})
	probeChainStableWait(t, 1, target.callCount, "target reads after a duplicate selection")

	target.releaseRow()
	h.warmChainWG.Wait()
	mu.Lock()
	defer mu.Unlock()
	if warmCount != 1 {
		t.Fatalf("warm-ups = %d, want exactly 1", warmCount)
	}
}

// TestScheduleDefaultFileWarmSharesSelectionAdmissionKey pins the overlap rule:
// a default warm and an explicit selection for the same row use the same
// admission key, so the second caller is a no-op instead of starting a second
// provider listing.
func TestScheduleDefaultFileWarmSharesSelectionAdmissionKey(t *testing.T) {
	row := probeChainRow(745, "virtual://movie/probe-chain?result=warm-default", nil)
	target := &blockedDefaultTarget{open: make(chan struct{}), row: row}
	var mu sync.Mutex
	warmCount := 0
	h := &PlaybackHandler{
		ServiceContext:     context.Background(),
		DefaultProbeTarget: target,
		VirtualReleaseCacheWarmup: VirtualReleaseCacheFillFunc(func(_ context.Context, _ string, _ int, _ func(playback.DownloadProgressPayload)) error {
			mu.Lock()
			warmCount++
			mu.Unlock()
			return nil
		}),
	}

	h.ScheduleSelectedFileWarm(probeChainTestContent, row.ID, catalog.AccessFilter{})
	probeChainWaitFor(t, 1, target.callCount, "target reads")
	h.ScheduleDefaultFileWarm(probeChainTestContent, row.ID, catalog.AccessFilter{})
	probeChainStableWait(t, 1, target.callCount, "target reads after the default warm")

	target.releaseRow()
	h.warmChainWG.Wait()
	mu.Lock()
	defer mu.Unlock()
	if warmCount != 1 {
		t.Fatalf("warm-ups = %d, want exactly 1 across both triggers", warmCount)
	}
}

// TestScheduleSelectedFileWarmSkipsWhenProbeChainIsLive pins the shared-fetch
// rule: a live pre-play probe already opens the provider URL, which starts the
// same fetch, so the warm-up is not scheduled on top of it.
func TestScheduleSelectedFileWarmSkipsWhenProbeChainIsLive(t *testing.T) {
	selectedRow := probeChainRow(744, "virtual://movie/probe-chain?result=warm-probe-live", nil)
	target := &blockedDefaultTarget{open: make(chan struct{}), row: selectedRow}
	var mu sync.Mutex
	warmCount := 0
	h := &PlaybackHandler{
		ServiceContext:     context.Background(),
		DefaultProbeTarget: target,
		VirtualReleaseCacheWarmup: VirtualReleaseCacheFillFunc(func(_ context.Context, _ string, _ int, _ func(playback.DownloadProgressPayload)) error {
			mu.Lock()
			warmCount++
			mu.Unlock()
			return nil
		}),
	}
	release, refusal, _ := h.defaultProbeChains.acquire(probeChainTestContent+"\x00"+strconv.Itoa(selectedRow.ID), true)
	if refusal != defaultProbeChainRefusalNone {
		t.Fatalf("probe admission refused: %s", refusal)
	}
	defer release()

	h.ScheduleSelectedFileWarm(probeChainTestContent, selectedRow.ID, catalog.AccessFilter{})
	h.warmChainWG.Wait()
	probeChainStableWait(t, 0, target.callCount, "target reads with a live probe chain")

	mu.Lock()
	defer mu.Unlock()
	if warmCount != 0 {
		t.Fatalf("warm-ups = %d, want none while the probe chain is live", warmCount)
	}
}

type recordedWarmCall struct {
	virtualURI string
	ownerID    int
}

type selectionProbeCall struct {
	contentID string
	fileID    int
	filter    catalog.AccessFilter
}

// recordingSelectionProber records what the watch-detail trigger hands to the
// probe and warm schedulers.
type recordingSelectionProber struct {
	mu        sync.Mutex
	calls     []selectionProbeCall
	warmCalls []selectionProbeCall
}

func (r *recordingSelectionProber) ScheduleSelectedFileProbe(contentID string, fileID int, filter catalog.AccessFilter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, selectionProbeCall{contentID: contentID, fileID: fileID, filter: filter})
}

func (r *recordingSelectionProber) ScheduleSelectedFileWarm(contentID string, fileID int, filter catalog.AccessFilter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warmCalls = append(r.warmCalls, selectionProbeCall{contentID: contentID, fileID: fileID, filter: filter})
}

func (r *recordingSelectionProber) snapshot() []selectionProbeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func (r *recordingSelectionProber) warmSnapshot() []selectionProbeCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.warmCalls)
}

// TestWatchDetailSelectionSchedulesProbeOnlyForUnprobedVirtualRow pins the
// trigger rule: only a read that names an explicit, virtual, still-unprobed
// version schedules a probe. A bare read, a local selection, and a selection
// with fresh inventory all schedule no probe. Every explicit virtual selection
// (probed or not) schedules a provider fetch warm-up, because probe evidence
// does not mean the provider has fetched the bytes.
func TestWatchDetailSelectionSchedulesProbeOnlyForUnprobedVirtualRow(t *testing.T) {
	prober := &recordingSelectionProber{}
	h := &ItemsHandler{virtualSelectionProber: prober}
	detail := &catalog.WatchDetail{
		ContentID: "episode-select",
		Type:      itemTypeEpisode,
		Versions: []catalog.FileVersion{
			{FileID: 21, FilePath: "/media/local/episode.mkv"},
			{FileID: 22, FilePath: "virtual://series/show/s01e01?result=lupins03e01hdr2160pwebh265edith"},
		},
	}

	// A bare read carries no selection: nothing is scheduled.
	h.scheduleSelectedVirtualProbe(detail.ContentID, detail.Type, detail.Versions, catalog.AccessFilter{})
	if got := prober.snapshot(); len(got) != 0 {
		t.Fatalf("bare read scheduled %d probes, want none", len(got))
	}
	if got := prober.warmSnapshot(); len(got) != 0 {
		t.Fatalf("bare read scheduled %d warm-ups, want none", len(got))
	}

	// An explicit selection of the virtual row schedules exactly that row's
	// probe and its warm-up.
	h.scheduleSelectedVirtualProbe(detail.ContentID, detail.Type, detail.Versions, catalog.AccessFilter{SelectedFileID: 22})
	got := prober.snapshot()
	if len(got) != 1 {
		t.Fatalf("explicit selection scheduled %d probes, want 1", len(got))
	}
	if got[0].contentID != detail.ContentID || got[0].fileID != 22 || got[0].filter.SelectedFileID != 22 {
		t.Fatalf("scheduled probe = %+v, want content %q file 22 with its selection", got[0], detail.ContentID)
	}
	warm := prober.warmSnapshot()
	if len(warm) != 1 {
		t.Fatalf("explicit selection scheduled %d warm-ups, want 1", len(warm))
	}
	if warm[0].contentID != detail.ContentID || warm[0].fileID != 22 || warm[0].filter.SelectedFileID != 22 {
		t.Fatalf("scheduled warm-up = %+v, want content %q file 22 with its selection", warm[0], detail.ContentID)
	}

	// Selecting a local row has no provider bytes to enumerate or fetch.
	h.scheduleSelectedVirtualProbe(detail.ContentID, detail.Type, detail.Versions, catalog.AccessFilter{SelectedFileID: 21})
	if got := prober.snapshot(); len(got) != 1 {
		t.Fatalf("local selection scheduled %d probes, want none", len(got)-1)
	}
	if got := prober.warmSnapshot(); len(got) != 1 {
		t.Fatalf("local selection scheduled %d warm-ups, want none", len(got)-1)
	}

	// A selected row with fresh inventory needs no probe, but still warms: the
	// bytes may be cold even though the inventory is known. The inventory has
	// to be there for the stamp to mean it: a stamp over empty track arrays is
	// the shape the selection gate re-probes (see
	// TestScheduleSelectedVirtualProbeRepairsAStampedEmptyRow).
	probedAt := time.Now()
	detail.Versions[1].ProbedAt = &probedAt
	detail.Versions[1].AudioTracks = []models.AudioTrack{{Index: 1, Codec: "ac3", Channels: 6}}
	h.scheduleSelectedVirtualProbe(detail.ContentID, detail.Type, detail.Versions, catalog.AccessFilter{SelectedFileID: 22})
	if got := prober.snapshot(); len(got) != 1 {
		t.Fatalf("fresh selection scheduled %d probes, want none", len(got)-1)
	}
	if got := prober.warmSnapshot(); len(got) != 2 {
		t.Fatalf("fresh selection scheduled %d warm-ups, want a second one", len(got))
	}
}

// TestPreprobeSelectedFileDeclinesWithoutAProbeTarget: the synchronous mode
// never invents work. A nil handler, an unknown row, or an unwired target
// reports declined so the caller answers declared metadata.
func TestPreprobeSelectedFileDeclinesWithoutAProbeTarget(t *testing.T) {
	var nilHandler *PlaybackHandler
	if got := nilHandler.PreprobeSelectedFile(t.Context(), "movie:1", 5, catalog.AccessFilter{}); got != VirtualPreprobeDeclined {
		t.Fatalf("nil handler preprobe = %q, want declined", got)
	}
	h := &PlaybackHandler{}
	if got := h.PreprobeSelectedFile(t.Context(), "", 5, catalog.AccessFilter{}); got != VirtualPreprobeDeclined {
		t.Fatalf("empty content preprobe = %q, want declined", got)
	}
	if got := h.PreprobeSelectedFile(t.Context(), "movie:1", 5, catalog.AccessFilter{}); got != VirtualPreprobeDeclined {
		t.Fatalf("unwired target preprobe = %q, want declined", got)
	}
}

// TestPreprobeSelectedFileReportsPendingWhenAChainIsLive: admission is shared
// with the detached chains, so a row another chain is already enumerating
// reports pending instead of opening a second probe on the same bytes.
func TestPreprobeSelectedFileReportsPendingWhenAChainIsLive(t *testing.T) {
	h := &PlaybackHandler{}
	release, refusal, _ := h.defaultProbeChains.acquire("movie:1\x005", true)
	if refusal != defaultProbeChainRefusalNone {
		t.Fatalf("admission refused: %s", refusal)
	}
	defer release()
	if got := h.PreprobeSelectedFile(t.Context(), "movie:1", 5, catalog.AccessFilter{}); got != VirtualPreprobePending {
		t.Fatalf("preprobe with a live chain = %q, want pending", got)
	}
}

// TestSelectionProbeAdmittedWhileBackgroundChainsAreFull pins the reserved lane:
// two discovery chains own the whole background budget, and the probe the viewer
// asked for by selecting a version is still admitted. The measured production case
// lost the selection probe to `concurrency_full` while a browse burst held every
// slot.
func TestSelectionProbeAdmittedWhileBackgroundChainsAreFull(t *testing.T) {
	backgroundA := probeChainRow(801, "virtual://movie/probe-lane-a?result=bg-a", nil)
	backgroundB := probeChainRow(802, "virtual://movie/probe-lane-b?result=bg-b", nil)
	selected := probeChainRow(803, "virtual://movie/probe-lane-sel?result=sel", nil)
	targetA := &blockedDefaultTarget{open: make(chan struct{}), row: backgroundA}
	targetB := &blockedDefaultTarget{open: make(chan struct{}), row: backgroundB}
	targetSel := &blockedDefaultTarget{open: make(chan struct{}), row: selected}
	targetSel.releaseRow()
	targets := map[string]*blockedDefaultTarget{
		"movie-probe-lane-a":   targetA,
		"movie-probe-lane-b":   targetB,
		"movie-probe-lane-sel": targetSel,
	}
	prober := &recordingProber{hits: make(chan struct{}, 1)}
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		VirtualPlaybackSourceProber: prober.probe,
	}
	h.DefaultProbeTarget = VirtualDefaultProbeTargetFunc(func(ctx context.Context, contentID string, _ catalog.AccessFilter) (*models.MediaFile, error) {
		target := targets[contentID]
		if target == nil {
			return nil, fmt.Errorf("unexpected chain for %q", contentID)
		}
		return target.DefaultVirtualProbeTarget(ctx, contentID, catalog.AccessFilter{})
	})

	h.ScheduleDefaultFileProbe("movie-probe-lane-a", probeChainSourceRow(), probeChainStreams(backgroundA), catalog.AccessFilter{})
	h.ScheduleDefaultFileProbe("movie-probe-lane-b", probeChainSourceRow(), probeChainStreams(backgroundB), catalog.AccessFilter{})
	probeChainWaitFor(t, 1, targetA.callCount, "background chain A entered its read")
	probeChainWaitFor(t, 1, targetB.callCount, "background chain B entered its read")

	h.ScheduleSelectedFileProbe("movie-probe-lane-sel", selected.ID, catalog.AccessFilter{})
	probeChainWaitFor(t, 1, targetSel.callCount, "selection chain entered its read")
	select {
	case <-prober.hits:
	case <-time.After(2 * time.Second):
		t.Fatal("the selection probe never reached the provider while both background slots were held")
	}

	targetA.releaseRow()
	targetB.releaseRow()
	h.probeChainWG.Wait()
}

// TestSelectionProbeIgnoresTheFailureDamper pins the second admission rule: the
// recently_failed damper keeps background re-probes off a failing provider, but a
// fresh selection is the viewer asking now. The measured case had the selection
// refused as recently_failed while the picker's menu waited.
func TestSelectionProbeIgnoresTheFailureDamper(t *testing.T) {
	selected := probeChainRow(811, "virtual://movie/probe-damper?result=sel", nil)
	key := virtualProbeFailureKey(selected.FilePath, selected.VirtualOwnerInstallationID)
	virtualProbeFailures.markWithReason(key, "context deadline exceeded")
	t.Cleanup(func() { virtualProbeFailures.clear(key) })
	if !virtualProbeBackedOff(key) {
		t.Fatal("fixture did not arm the damper")
	}

	reader := &probeChainVersionReader{
		files: []*models.MediaFile{selected},
		rows:  map[int]*models.MediaFile{selected.ID: selected},
	}
	prober := &recordingProber{}
	h := &PlaybackHandler{
		ServiceContext:              context.Background(),
		DefaultProbeTarget:          NewVirtualDefaultProbeTarget(reader, reader),
		VirtualPlaybackSourceProber: prober.probe,
	}

	h.ScheduleSelectedFileProbe(probeChainTestContent, selected.ID, catalog.AccessFilter{AllowedLibraryIDs: []int{77}})
	h.probeChainWG.Wait()
	if prober.count() != 1 {
		t.Fatalf("probes = %d, want the selection to probe despite a recently_failed mark", prober.count())
	}
}

// TestSelectionProbeRetriesOnceAndReportsItsReason pins the retry inside the
// selection's own bound and the reason it leaves behind: one provider failure earns
// a second attempt, and the final state names the cause instead of a generic
// "unavailable".
func TestSelectionProbeRetriesOnceAndReportsItsReason(t *testing.T) {
	prevDelay := virtualSelectionProbeRetryDelay
	virtualSelectionProbeRetryDelay = time.Millisecond
	t.Cleanup(func() { virtualSelectionProbeRetryDelay = prevDelay })

	selected := probeChainRow(821, "virtual://movie/probe-retry?result=sel", nil)
	key := virtualProbeFailureKey(selected.FilePath, selected.VirtualOwnerInstallationID)
	virtualProbeFailures.clear(key)
	t.Cleanup(func() { virtualProbeFailures.clear(key) })

	reader := &probeChainVersionReader{
		files: []*models.MediaFile{selected},
		rows:  map[int]*models.MediaFile{selected.ID: selected},
	}
	var calls int
	var mu sync.Mutex
	h := &PlaybackHandler{
		ServiceContext:     context.Background(),
		DefaultProbeTarget: NewVirtualDefaultProbeTarget(reader, reader),
		VirtualPlaybackSourceProber: func(context.Context, string, *models.MediaFile) (*models.MediaFile, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			return nil, errors.New("ffprobe failed: exit status 1")
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if got := h.PreprobeSelectedFile(ctx, probeChainTestContent, selected.ID, catalog.AccessFilter{AllowedLibraryIDs: []int{77}}); got != VirtualPreprobePending {
		t.Fatalf("preprobe with a failing provider = %q, want pending", got)
	}
	mu.Lock()
	attempts := calls
	mu.Unlock()
	if attempts != virtualSelectionProbeAttempts {
		t.Fatalf("attempts = %d, want %d inside the selection bound", attempts, virtualSelectionProbeAttempts)
	}
	if reason := virtualProbeFailures.reason(key); !strings.Contains(reason, "exit status 1") {
		t.Fatalf("recorded failure reason = %q, want the provider's own error", reason)
	}
}
