package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// blockingProbeEnsurer fails the test if the start path asks for anything but
// the cached-only ensure — the other variants can run the multi-second
// bitstream scan, which must never happen on a request path.
type blockingProbeEnsurer struct {
	t           *testing.T
	mu          sync.Mutex
	cachedCalls int
}

func (e *blockingProbeEnsurer) EnsureProbeOnly(_ context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	e.t.Helper()
	e.t.Fatal("playback start called EnsureProbeOnly; it must resolve a known copy-safety verdict")
	return file, nil
}

func (e *blockingProbeEnsurer) EnsureCopySafetyCached(_ context.Context, file *models.MediaFile) (*models.MediaFile, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cachedCalls++
	return file, nil
}

func (e *blockingProbeEnsurer) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cachedCalls
}

type recordingCopySafetyRacer struct {
	mu    sync.Mutex
	plans []playback.DeliveryV3
	files []int
	// bare records the files handed to RaceScan — the revival path, which has
	// no plan to hand over.
	bare []int
	// knownUnsafe stands in for a verdict this replica holds without the row
	// carrying it: an unsafe scan whose write to media_files failed.
	knownUnsafe bool
}

func (r *recordingCopySafetyRacer) RaceScanForPlan(fileID int, plan *playback.PlanV3) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files = append(r.files, fileID)
	if plan != nil {
		r.plans = append(r.plans, plan.Delivery)
	}
}

func (r *recordingCopySafetyRacer) RaceScan(fileID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bare = append(r.bare, fileID)
}

func (r *recordingCopySafetyRacer) VideoCopyUnsafeKnown(context.Context, *models.MediaFile) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.knownUnsafe
}

func (r *recordingCopySafetyRacer) raced() ([]int, []playback.DeliveryV3) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.files...), append([]playback.DeliveryV3(nil), r.plans...)
}

func (r *recordingCopySafetyRacer) bareRaces() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.bare...)
}

// Starting playback must never wait on the H.264 copy-safety scan: it takes the
// cached-only ensure, and the plan it issues is handed to the racer that
// resolves the verdict behind it.
func TestHandleStartPlaybackV3DoesNotBlockOnCopySafetyScan(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := v3HandlerFixtureFile(t)
	ensurer := &blockingProbeEnsurer{t: t}
	racer := &recordingCopySafetyRacer{}

	handler := NewPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.ProbeEnsurer = ensurer
	handler.CopySafetyRacer = racer

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest()))).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	handler.HandleStartPlayback(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if ensurer.calls() == 0 {
		t.Fatal("start did not resolve the cached copy-safety verdict")
	}
	files, _ := racer.raced()
	if len(files) != 1 || files[0] != file.ID {
		t.Fatalf("raced files = %v, want the planned file %d handed to the racer", files, file.ID)
	}
}

// The route test is the racer's; the handler's job is only to hand it the plan
// it issued, for every route, and to stay silent when no racer is wired.
func TestRaceCopySafetyV3HandsThePlanToTheRacer(t *testing.T) {
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	racer := &recordingCopySafetyRacer{}
	handler.CopySafetyRacer = racer

	handler.raceCopySafetyV3(42, &playback.PlanV3{PlanID: "plan-1", Delivery: playback.DeliveryRemuxHLSV3})
	handler.raceCopySafetyV3(0, &playback.PlanV3{PlanID: "plan-2", Delivery: playback.DeliveryRemuxHLSV3})
	handler.raceCopySafetyV3(43, nil)

	files, deliveries := racer.raced()
	if len(files) != 1 || files[0] != 42 {
		t.Fatalf("raced files = %v, want only the valid file/plan pair", files)
	}
	if len(deliveries) != 1 || deliveries[0] != playback.DeliveryRemuxHLSV3 {
		t.Fatalf("raced deliveries = %v, want the issued plan's delivery", deliveries)
	}

	without := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	without.raceCopySafetyV3(42, &playback.PlanV3{PlanID: "plan-1", Delivery: playback.DeliveryRemuxHLSV3})
}
