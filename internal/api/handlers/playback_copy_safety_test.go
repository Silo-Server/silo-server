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
	files []int
}

func (r *recordingCopySafetyRacer) RaceScanForPlan(fileID int, _ *playback.PlanV3) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files = append(r.files, fileID)
}

func (r *recordingCopySafetyRacer) raced() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.files...)
}

// Starting playback must never wait on the H.264 copy-safety scan: it takes the
// cached-only ensure, and the plan it issues is handed to the racer that
// resolves the verdict behind it.
func TestStartPlaybackV2DoesNotBlockOnCopySafetyScan(t *testing.T) {
	sessionMgr := playback.NewSessionManager(0, 0)
	file := v3HandlerFixtureFile(t)
	ensurer := &blockingProbeEnsurer{t: t}
	racer := &recordingCopySafetyRacer{}

	handler := newKeyedPlaybackHandler(sessionMgr, testPlaybackFileResolver{file: file})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.ProbeEnsurer = ensurer
	handler.CopySafetyRacer = racer

	req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start",
		strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest()))).WithContext(newAuthorizedPlaybackContext())
	rr := httptest.NewRecorder()
	serveStartPlaybackV2(t, handler, rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if ensurer.calls() == 0 {
		t.Fatal("start did not resolve the cached copy-safety verdict")
	}
	files := racer.raced()
	if len(files) != 1 || files[0] != file.ID {
		t.Fatalf("raced files = %v, want the planned file %d handed to the racer", files, file.ID)
	}
}
