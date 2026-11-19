package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// pathLookupFileResolver resolves rows by id and returns a fixed error from the
// exact-path lookup, so tests can distinguish "no row owns this path" (a
// not-found sentinel) from "the lookup could not be answered" (a real error).
type pathLookupFileResolver struct {
	files   map[int]*models.MediaFile
	pathErr error
}

func (r pathLookupFileResolver) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	return r.files[id], nil
}

func (r pathLookupFileResolver) GetByPath(context.Context, string) (*models.MediaFile, error) {
	return nil, r.pathErr
}

// persistRotationResolver resolves rows both by id (the inventory build) and by
// exact path (the identity guard's ownership lookup), so one resolver serves the
// rotation write and the publish that follows it.
type persistRotationResolver struct {
	files  map[int]*models.MediaFile
	byPath map[string]*models.MediaFile
}

func (r persistRotationResolver) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	return r.files[id], nil
}

func (r persistRotationResolver) GetByPath(_ context.Context, path string) (*models.MediaFile, error) {
	return r.byPath[path], nil
}

// persistRotationEvidenceSaver records the catalog writes and the file ids the
// evidence pipeline published inventory for, so a rotation can be observed
// end to end: the write targets the owner row and the publish names it too.
type persistRotationEvidenceSaver struct {
	mu     sync.Mutex
	args   []models.VirtualFilePersistArgs
	result VirtualFileMetadataUpdateResult
}

func (s *persistRotationEvidenceSaver) save(_ context.Context, args models.VirtualFilePersistArgs) (VirtualFileMetadataUpdateResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.args = append(s.args, args)
	return s.result, nil
}

func (s *persistRotationEvidenceSaver) recorded() []models.VirtualFilePersistArgs {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]models.VirtualFilePersistArgs(nil), s.args...)
}

// probedDualAudioFile is the freshly probed inventory the fix must not lose:
// two audio tracks and one subtitle, unlike the pinned row's declared 1xAAC
// snapshot.
func probedDualAudioFile() *models.MediaFile {
	return &models.MediaFile{
		Resolution: "1080p", CodecVideo: "h264", CodecAudio: "eac3", Container: "mkv",
		VideoTracks:    []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}},
		AudioTracks:    []models.AudioTrack{{Codec: "eac3", Channels: 6, Language: "eng"}, {Codec: "aac", Channels: 2, Language: "deu"}},
		SubtitleTracks: []models.SubtitleTrack{{Codec: "subrip", Language: "eng"}},
	}
}

// TestPersistProbeEvidenceRotatesToSiblingOwnerRow pins the persist-path
// rotation: verified evidence for a URI a sibling row verifiably owns is written
// to that owner row, not attempted as a CAS adoption the SQL sibling fence would
// refuse. The owner's catalog identity is adopted while the freshly probed
// tracks are retained, the write is metadata-only against the owner's own path
// (no adoption fence needed), and the owner row's snapshot supplies the write
// generation.
func TestPersistProbeEvidenceRotatesToSiblingOwnerRow(t *testing.T) {
	const (
		neutral    = "virtual://movie/tt-persist-rotate"
		pinnedURI  = neutral + "?result=pinned"
		ownerURI   = neutral + "?result=owner"
		content    = "movie-persist-rotate"
		folderID   = 9
		ownerID    = 5
		pinnedFile = 601
		ownerFile  = 602
	)
	ownerUpdated := time.Now().Add(-time.Minute).UTC()
	pinnedRow := &models.MediaFile{
		ID: pinnedFile, ContentID: content, FilePath: pinnedURI,
		MediaFolderID: folderID, VirtualOwnerInstallationID: ownerID, ProbeSource: "virtual",
		ProviderVideoHash: "hash-pinned",
		UpdatedAt:         time.Now().UTC(),
	}
	ownerRow := &models.MediaFile{
		ID: ownerFile, ContentID: content, FilePath: ownerURI,
		MediaFolderID: folderID, VirtualOwnerInstallationID: ownerID, ProbeSource: "virtual",
		ProviderVideoHash: "hash-owner", UpdatedAt: ownerUpdated,
	}

	saver := &persistRotationEvidenceSaver{result: VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}}
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0), byPathPlaybackFileResolver{
		testPlaybackFileResolver: testPlaybackFileResolver{file: pinnedRow},
		byPath:                   map[string]*models.MediaFile{ownerURI: ownerRow},
	})
	h.VirtualFileMetadataSaver = saver.save
	h.VirtualFileSaver = func(context.Context, models.VirtualFilePersistArgs) (int64, error) { return 1, nil }

	h.persistVirtualProbeEvidence(context.Background(), pinnedRow, ownerURI, probedDualAudioFile(), noCandidateIdentity, true, true)
	h.stopVirtualEvidence()

	calls := saver.recorded()
	if len(calls) != 1 {
		t.Fatalf("persist writes = %d, want exactly 1 onto the owner row", len(calls))
	}
	got := calls[0]
	if got.FileID != ownerFile {
		t.Fatalf("write file_id = %d, want the owner row %d", got.FileID, ownerFile)
	}
	if got.ExpectedFilePath != ownerURI {
		t.Fatalf("write expected path = %q, want the owner row's own path %q", got.ExpectedFilePath, ownerURI)
	}
	if got.AdoptPath != "" || got.RequireAdopt {
		t.Fatalf("rotated write nominated an adoption fence: %+v", got)
	}
	if got.UpdatedAt != ownerUpdated {
		t.Fatalf("write generation = %v, want the owner row's snapshot %v", got.UpdatedAt, ownerUpdated)
	}
	if got.OwnerID != ownerID || got.LibraryID != folderID {
		t.Fatalf("write identity = (owner %d, library %d), want (%d, %d)", got.OwnerID, got.LibraryID, ownerID, folderID)
	}
	if got.ExpectedProviderVideoHash != "hash-owner" {
		t.Fatalf("write provider identity = %q, want the owner row's %q", got.ExpectedProviderVideoHash, "hash-owner")
	}
	// The freshly probed tracks must survive the rotation.
	var audio []models.AudioTrack
	if err := json.Unmarshal(got.AudioTracks, &audio); err != nil {
		t.Fatalf("unmarshal audio tracks: %v", err)
	}
	if len(audio) != 2 {
		t.Fatalf("audio tracks = %#v, want the two probed tracks retained", audio)
	}
	var subs []models.SubtitleTrack
	if err := json.Unmarshal(got.SubtitleTracks, &subs); err != nil {
		t.Fatalf("unmarshal subtitle tracks: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("subtitle tracks = %#v, want the probed subtitle retained", subs)
	}
}

// TestPersistProbeEvidenceRotationPublishesVerifiedInventory is the publish
// half: a session bound to the rotated owner row receives the inventory_updated
// event carrying the probed tracks the moment the rotated write commits, so the
// menus stop showing the stale declared snapshot.
func TestPersistProbeEvidenceRotationPublishesVerifiedInventory(t *testing.T) {
	const (
		neutral   = "virtual://movie/tt-persist-publish"
		pinnedURI = neutral + "?result=pinned"
		ownerURI  = neutral + "?result=owner"
		content   = "movie-persist-publish"
		ownerID   = 5
	)
	probedAt := time.Now().UTC()
	// Distinct durable identity: these are two releases (the alternate-version
	// shape this rotation exists for). Two rows the catalog groups as one release
	// keep the evidence on the requested row instead; see
	// TestProbeEvidenceLandsOnTheRequestedRowWhenASameReleaseDuplicateOwnsTheCandidate.
	ownerRow := &models.MediaFile{
		ID: 702, ContentID: content, FilePath: ownerURI,
		MediaFolderID: 9, VirtualOwnerInstallationID: ownerID, ProbeSource: "virtual",
		ProbeUpdatedAt: &probedAt, ProviderVideoHash: "hash-owner",
		AudioTracks:    probedDualAudioFile().AudioTracks,
		SubtitleTracks: probedDualAudioFile().SubtitleTracks,
	}
	pinnedRow := &models.MediaFile{
		ID: 701, ContentID: content, FilePath: pinnedURI,
		MediaFolderID: 9, VirtualOwnerInstallationID: ownerID, ProbeSource: "virtual",
		ProviderVideoHash: "hash-pinned",
		AudioTracks:       []models.AudioTrack{{Codec: "aac", Channels: 2, Language: "eng"}},
	}

	sessionMgr := playback.NewSessionManager(0, 0)
	session, err := sessionMgr.StartSession(1, "profile-1", pinnedRow.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	// The serve layer binds a rotated session to the row that owns the candidate
	// URI; model that here so the publish target is the owner row.
	if err := sessionMgr.SetEffectiveMediaFileID(session.ID, ownerRow.ID); err != nil {
		t.Fatalf("SetEffectiveMediaFileID: %v", err)
	}
	saver := &persistRotationEvidenceSaver{result: VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}}
	h := NewPlaybackHandler(sessionMgr, persistRotationResolver{
		files:  map[int]*models.MediaFile{pinnedRow.ID: pinnedRow, ownerRow.ID: ownerRow},
		byPath: map[string]*models.MediaFile{ownerURI: ownerRow},
	})
	h.RealtimeHub = playback.NewRealtimeHub()
	if err := sessionMgr.SetRealtimeConnection(session.ID, true); err != nil {
		t.Fatalf("SetRealtimeConnection: %v", err)
	}
	conn := &sourceCommittedTestConn{}
	registration := h.RealtimeHub.Register(session.ID, conn)
	if registration == nil {
		t.Fatal("expected a realtime registration")
	}
	defer h.RealtimeHub.Unregister(registration)

	h.VirtualFileMetadataSaver = saver.save
	h.VirtualFileSaver = func(context.Context, models.VirtualFilePersistArgs) (int64, error) { return 1, nil }

	h.persistVirtualProbeEvidence(context.Background(), pinnedRow, ownerURI, probedDualAudioFile(), noCandidateIdentity, true, false)
	h.stopVirtualEvidence()

	// The write landed on the owner row.
	calls := saver.recorded()
	if len(calls) != 1 || calls[0].FileID != ownerRow.ID {
		t.Fatalf("persist writes = %+v, want exactly one onto owner row %d", calls, ownerRow.ID)
	}
	// And the publish named the owner row, reaching the bound session.
	if len(conn.messages) != 1 {
		t.Fatalf("delivered %d events, want 1 inventory_updated", len(conn.messages))
	}
	event, ok := conn.messages[0].(playback.EventEnvelope)
	if !ok {
		t.Fatalf("message type = %T, want playback.EventEnvelope", conn.messages[0])
	}
	if event.Name != playback.RealtimeEventInventoryUpdated {
		t.Fatalf("event name = %q, want %q", event.Name, playback.RealtimeEventInventoryUpdated)
	}
	var payload playback.InventoryUpdatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.EffectiveMediaFileID != ownerRow.ID {
		t.Fatalf("effective media file id = %d, want the owner row %d", payload.EffectiveMediaFileID, ownerRow.ID)
	}
	if len(payload.AudioTracks) != 2 {
		t.Fatalf("payload audio tracks = %#v, want the probed dual audio inventory", payload.AudioTracks)
	}
	if len(payload.SubtitleInventory) == 0 {
		t.Fatalf("payload subtitle inventory = %#v, want the probed subtitle carried to the menu", payload.SubtitleInventory)
	}
}

// TestPersistProbeEvidenceRotationGuardRefusalKeepsOldBehavior pins the
// fail-closed half: when the identity guard cannot answer who owns the candidate
// path, the write is refused with a concrete owner_lookup_failed reason rather
// than adopting bytes whose owner is unknown.
func TestPersistProbeEvidenceRotationGuardRefusalKeepsOldBehavior(t *testing.T) {
	logs := captureHandlerLogs(t)
	const (
		neutral = "virtual://movie/tt-persist-guard"
		uri     = neutral + "?result=owner"
	)
	pinnedRow := &models.MediaFile{
		ID: 801, ContentID: "movie-persist-guard", FilePath: neutral + "?result=pinned",
		MediaFolderID: 9, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
	}
	saver := &persistRotationEvidenceSaver{result: VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}}
	h := &PlaybackHandler{
		fileResolver:             erroringPathFileResolver{err: errors.New("catalog unavailable")},
		VirtualFileMetadataSaver: saver.save,
		VirtualFileSaver:         func(context.Context, models.VirtualFilePersistArgs) (int64, error) { return 1, nil },
	}

	h.persistVirtualProbeEvidence(context.Background(), pinnedRow, uri, probedDualAudioFile(), noCandidateIdentity, true, true)
	h.stopVirtualEvidence()

	if calls := saver.recorded(); len(calls) != 0 {
		t.Fatalf("guard refusal still wrote %+v, want no write", calls)
	}
	logText := logs.String()
	if !strings.Contains(logText, `"reason":"`+virtualProbeRefusalOwnerLookupFailed+`"`) {
		t.Fatalf("guard refusal did not log the concrete owner_lookup_failed reason:\n%s", logText)
	}
	if !strings.Contains(logText, uri) {
		t.Fatalf("guard refusal did not log the candidate identity %q:\n%s", uri, logText)
	}
}

// TestVirtualProbeEvidenceRefusalReasonSplit pins the refusal taxonomy against
// the SQL fence's own precedence: a sibling path owner, a live failed verdict,
// and the remaining stale-snapshot case are named apart, so the next diagnosis
// does not have to read a lumped bucket.
func TestVirtualProbeEvidenceRefusalReasonSplit(t *testing.T) {
	const (
		neutral  = "virtual://movie/tt-refusal-split"
		uri      = neutral + "?result=owner"
		content  = "movie-refusal-split"
		folderID = 9
		ownerID  = 5
	)
	pinnedRow := &models.MediaFile{
		ID: 900, ContentID: content, FilePath: neutral + "?result=pinned",
		MediaFolderID: folderID, VirtualOwnerInstallationID: ownerID, ProbeSource: "virtual",
	}
	// Another content's row owns the candidate path: the identity guard will not
	// rotate to it, so the fence refusal is a sibling owner.
	unrelatedOwner := &models.MediaFile{
		ID: 901, ContentID: "different-content", FilePath: uri,
		MediaFolderID: folderID, VirtualOwnerInstallationID: ownerID,
	}
	sibling := &PlaybackHandler{
		fileResolver: byPathPlaybackFileResolver{byPath: map[string]*models.MediaFile{uri: unrelatedOwner}},
	}
	if got := sibling.virtualProbeEvidenceRefusalReason(context.Background(), pinnedRow, uri); got != virtualProbeRefusalSiblingOwner {
		t.Fatalf("sibling-owner reason = %q, want %q", got, virtualProbeRefusalSiblingOwner)
	}

	// No path owner, but the candidate identity carries a live failed verdict.
	failedAt := time.Now().UTC()
	failedRow := &models.MediaFile{
		ID: 902, ContentID: content, FilePath: uri,
		MediaFolderID: folderID, VirtualOwnerInstallationID: ownerID, FailedAt: &failedAt,
	}
	verdict := &PlaybackHandler{
		fileResolver: byPathPlaybackFileResolver{byPath: map[string]*models.MediaFile{}},
		VirtualFileLookup: func(context.Context, string) (*models.MediaFile, error) {
			return failedRow, nil
		},
	}
	if got := verdict.virtualProbeEvidenceRefusalReason(context.Background(), pinnedRow, uri); got != virtualProbeRefusalFailedVerdict {
		t.Fatalf("failed-verdict reason = %q, want %q", got, virtualProbeRefusalFailedVerdict)
	}

	// No path owner and no live verdict: the only remaining fence cause is a
	// stale CAS snapshot.
	plain := &PlaybackHandler{
		fileResolver: byPathPlaybackFileResolver{byPath: map[string]*models.MediaFile{}},
	}
	if got := plain.virtualProbeEvidenceRefusalReason(context.Background(), pinnedRow, uri); got != virtualProbeRefusalStaleSnapshot {
		t.Fatalf("stale-snapshot reason = %q, want %q", got, virtualProbeRefusalStaleSnapshot)
	}
}

// TestVirtualPathLookupRowTreatsNotFoundAsAbsence pins the defect-2 fix: a
// resolved candidate URI whose row stores the neutral path misses the exact
// lookup by design, and the production resolver reports that as a not-found
// sentinel. That is safe absence, not an unanswered ownership question, so the
// guard must not fail closed on it.
func TestVirtualPathLookupRowTreatsNotFoundAsAbsence(t *testing.T) {
	const uri = "virtual://movie/tt-absent?result=pick"
	for _, sentinel := range []error{scanner.ErrFileNotFound, ErrVirtualCandidateNotFound} {
		h := &PlaybackHandler{fileResolver: pathLookupFileResolver{pathErr: sentinel}}
		row, capable, err := h.virtualPathLookupRow(context.Background(), uri)
		if !capable {
			t.Fatalf("lookup of %v reported not capable, want capable", sentinel)
		}
		if err != nil {
			t.Fatalf("lookup of %v returned error %v, want safe absence", sentinel, err)
		}
		if row != nil {
			t.Fatalf("lookup of %v returned a row, want nil", sentinel)
		}
	}
}

// TestPersistProbeEvidenceRetriesAfterPathNotFound pins the strand remedy: when
// the candidate's exact path has no owner yet, the persisted write is admitted
// against the requested row (retried on the next evidence arrival) instead of
// being terminally refused as owner_lookup_failed.
func TestPersistProbeEvidenceRetriesAfterPathNotFound(t *testing.T) {
	logs := captureHandlerLogs(t)
	const (
		neutral   = "virtual://movie/tt-absent-owner"
		pinnedURI = neutral + "?result=pinned"
		candURI   = neutral + "?result=candidate"
		content   = "movie-absent-owner"
	)
	pinnedRow := &models.MediaFile{
		ID: 910, ContentID: content, FilePath: pinnedURI,
		MediaFolderID: 9, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
		UpdatedAt: time.Now().UTC(),
	}
	saver := &persistRotationEvidenceSaver{result: VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}}
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0), pathLookupFileResolver{
		files:   map[int]*models.MediaFile{pinnedRow.ID: pinnedRow},
		pathErr: scanner.ErrFileNotFound,
	})
	h.VirtualFileMetadataSaver = saver.save
	h.VirtualFileSaver = func(context.Context, models.VirtualFilePersistArgs) (int64, error) { return 1, nil }

	h.persistVirtualProbeEvidence(context.Background(), pinnedRow, candURI, probedDualAudioFile(), noCandidateIdentity, true, true)
	h.stopVirtualEvidence()

	calls := saver.recorded()
	if len(calls) != 1 {
		t.Fatalf("persist writes = %d, want 1: a not-found path must be retried, not refused", len(calls))
	}
	if calls[0].FileID != pinnedRow.ID {
		t.Fatalf("write file_id = %d, want the requested row %d", calls[0].FileID, pinnedRow.ID)
	}
	if logText := logs.String(); strings.Contains(logText, `"reason":"`+virtualProbeRefusalOwnerLookupFailed+`"`) {
		t.Fatalf("a not-found path was refused as owner_lookup_failed:\n%s", logText)
	}
}

// TestPersistProbeEvidenceRefusedOwnershipServesLiveSessionInMemory pins the
// fail-closed fallback: when the ownership lookup genuinely cannot be answered,
// the catalog write stays blocked but the live session bound to the requested
// row's candidate still receives the probed tracks in memory, so the menu does
// not stay stale. The inventory status must stay the session's real one: a
// refused, unpersisted probe cannot be labeled verified.
func TestPersistProbeEvidenceRefusedOwnershipServesLiveSessionInMemory(t *testing.T) {
	logs := captureHandlerLogs(t)
	const (
		neutral   = "virtual://movie/tt-refused-owner"
		pinnedURI = neutral + "?result=pinned"
		candURI   = neutral + "?result=candidate"
		content   = "movie-refused-owner"
	)
	pinnedRow := &models.MediaFile{
		ID: 920, ContentID: content, FilePath: pinnedURI,
		MediaFolderID: 9, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
	}
	sessionMgr := playback.NewSessionManager(0, 0)
	session, err := sessionMgr.StartSession(1, "profile-1", pinnedRow.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	// The serve layer binds a rotated session to the candidate it plays.
	if err := sessionMgr.SetEffectiveMediaFileID(session.ID, pinnedRow.ID); err != nil {
		t.Fatalf("SetEffectiveMediaFileID: %v", err)
	}
	if err := sessionMgr.SetVirtualSource(session.ID, candURI, 5); err != nil {
		t.Fatalf("SetVirtualSource: %v", err)
	}

	h := NewPlaybackHandler(sessionMgr, pathLookupFileResolver{
		files:   map[int]*models.MediaFile{pinnedRow.ID: pinnedRow},
		pathErr: errors.New("catalog unavailable"),
	})
	h.RealtimeHub = playback.NewRealtimeHub()
	if err := sessionMgr.SetRealtimeConnection(session.ID, true); err != nil {
		t.Fatalf("SetRealtimeConnection: %v", err)
	}
	conn := &sourceCommittedTestConn{}
	registration := h.RealtimeHub.Register(session.ID, conn)
	if registration == nil {
		t.Fatal("expected a realtime registration")
	}
	defer h.RealtimeHub.Unregister(registration)
	saver := &persistRotationEvidenceSaver{result: VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}}
	h.VirtualFileMetadataSaver = saver.save
	h.VirtualFileSaver = func(context.Context, models.VirtualFilePersistArgs) (int64, error) { return 1, nil }

	h.persistVirtualProbeEvidence(context.Background(), pinnedRow, candURI, probedDualAudioFile(), noCandidateIdentity, true, true)
	h.stopVirtualEvidence()

	if calls := saver.recorded(); len(calls) != 0 {
		t.Fatalf("guard refusal still wrote %+v, want no catalog write", calls)
	}
	if !strings.Contains(logs.String(), `"reason":"`+virtualProbeRefusalOwnerLookupFailed+`"`) {
		t.Fatalf("guard refusal did not log owner_lookup_failed:\n%s", logs.String())
	}
	if len(conn.messages) != 1 {
		t.Fatalf("delivered %d events, want 1 in-memory inventory_updated", len(conn.messages))
	}
	event, ok := conn.messages[0].(playback.EventEnvelope)
	if !ok {
		t.Fatalf("message type = %T, want playback.EventEnvelope", conn.messages[0])
	}
	if event.Name != playback.RealtimeEventInventoryUpdated {
		t.Fatalf("event name = %q, want %q", event.Name, playback.RealtimeEventInventoryUpdated)
	}
	var payload playback.InventoryUpdatedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.InventoryStatus == string(ProbeProvenanceVerified) {
		t.Fatalf("inventory status = %q, want the session's real status: a refused, unpersisted probe must not be verified", payload.InventoryStatus)
	}
	if len(payload.AudioTracks) != 2 {
		t.Fatalf("payload audio tracks = %#v, want the probed dual audio inventory", payload.AudioTracks)
	}
	if len(payload.SubtitleInventory) == 0 {
		t.Fatalf("payload subtitle inventory = %#v, want the probed subtitle", payload.SubtitleInventory)
	}
}

// TestPersistProbeEvidenceRotationDeliveryLoggedOnce pins the defect-1
// observability fix: a rotated write publishes exactly one inventory_updated to
// the owner row's session and logs the delivery with both ends of the rotation,
// so a live run can confirm the publish instead of inferring it.
func TestPersistProbeEvidenceRotationDeliveryLoggedOnce(t *testing.T) {
	logs := captureHandlerLogs(t)
	const (
		neutral   = "virtual://movie/tt-rotation-log"
		pinnedURI = neutral + "?result=pinned"
		ownerURI  = neutral + "?result=owner"
		content   = "movie-rotation-log"
	)
	probedAt := time.Now().UTC()
	// A genuinely different release, so the write rotates as this test pins.
	ownerRow := &models.MediaFile{
		ID: 932, ContentID: content, FilePath: ownerURI,
		MediaFolderID: 9, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
		ProbeUpdatedAt: &probedAt, ProviderVideoHash: "hash-owner",
		AudioTracks:    probedDualAudioFile().AudioTracks,
		SubtitleTracks: probedDualAudioFile().SubtitleTracks,
	}
	pinnedRow := &models.MediaFile{
		ID: 931, ContentID: content, FilePath: pinnedURI,
		MediaFolderID: 9, VirtualOwnerInstallationID: 5, ProbeSource: "virtual",
		ProviderVideoHash: "hash-pinned",
	}
	sessionMgr := playback.NewSessionManager(0, 0)
	session, err := sessionMgr.StartSession(1, "profile-1", pinnedRow.ID, playback.PlayDirect, false)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := sessionMgr.SetEffectiveMediaFileID(session.ID, ownerRow.ID); err != nil {
		t.Fatalf("SetEffectiveMediaFileID: %v", err)
	}
	h := NewPlaybackHandler(sessionMgr, persistRotationResolver{
		files:  map[int]*models.MediaFile{pinnedRow.ID: pinnedRow, ownerRow.ID: ownerRow},
		byPath: map[string]*models.MediaFile{ownerURI: ownerRow},
	})
	h.RealtimeHub = playback.NewRealtimeHub()
	if err := sessionMgr.SetRealtimeConnection(session.ID, true); err != nil {
		t.Fatalf("SetRealtimeConnection: %v", err)
	}
	conn := &sourceCommittedTestConn{}
	registration := h.RealtimeHub.Register(session.ID, conn)
	if registration == nil {
		t.Fatal("expected a realtime registration")
	}
	defer h.RealtimeHub.Unregister(registration)
	saver := &persistRotationEvidenceSaver{result: VirtualFileMetadataUpdateResult{MetadataUpdated: true, RowsAffected: 1}}
	h.VirtualFileMetadataSaver = saver.save
	h.VirtualFileSaver = func(context.Context, models.VirtualFilePersistArgs) (int64, error) { return 1, nil }

	h.persistVirtualProbeEvidence(context.Background(), pinnedRow, ownerURI, probedDualAudioFile(), noCandidateIdentity, true, false)
	h.stopVirtualEvidence()

	if len(conn.messages) != 1 {
		t.Fatalf("delivered %d events, want exactly 1 (no double-publish)", len(conn.messages))
	}
	logText := logs.String()
	if !strings.Contains(logText, "virtual probe evidence inventory delivered") {
		t.Fatalf("rotation delivery was not logged:\n%s", logText)
	}
	if !strings.Contains(logText, `"rotated_from_file_id":931`) {
		t.Fatalf("delivery log omitted the rotated-from row:\n%s", logText)
	}
	if !strings.Contains(logText, `"file_id":932`) {
		t.Fatalf("delivery log omitted the owner row:\n%s", logText)
	}
	if !strings.Contains(logText, `"sessions_notified":1`) {
		t.Fatalf("delivery log omitted the session count:\n%s", logText)
	}
}
