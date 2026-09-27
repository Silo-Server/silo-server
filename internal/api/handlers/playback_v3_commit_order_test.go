package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

type observingSaveAttemptV3 struct {
	playback.PlanStoreV3
	save func(context.Context, playback.AttemptRecordV3) error
}

func (s observingSaveAttemptV3) SaveAttempt(ctx context.Context, record playback.AttemptRecordV3) error {
	return s.save(ctx, record)
}

func TestStartPlaybackV3PersistsBeforeTransportPublication(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "save failure", true: "shutdown after save"}[shutdown], func(t *testing.T) {
			file := v3HandlerFixtureFile(t)
			handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: file})
			handler.ItemAccess = allowAllPlaybackItemAccess{}
			handler.PlaybackConfig = playbackTestConfig(writePlaybackTestFFmpeg(t), t.TempDir())
			presetLocalRegistryV3(handler, playback.NewTransformationRegistryV3([]playback.TransformationSpecV3{{Name: "audio_to_aac", RecipeVersion: "2", Available: true}, {Name: "video_to_h264", RecipeVersion: "2", Available: true}}))
			start := v3HandlerStartRequest()
			start.ClientPlaybackContext.Deliveries = map[string]playback.DeliveryCapabilityV3{playback.DeliveryClassHLSV3: {Enabled: true, SupportedOnDevice: true}}
			underlying := handler.PlanStoreV3
			var sessionID string
			handler.PlanStoreV3 = observingSaveAttemptV3{PlanStoreV3: underlying, save: func(ctx context.Context, record playback.AttemptRecordV3) error {
				if record.SessionID == "" {
					return underlying.SaveAttempt(ctx, record)
				}
				sessionID = record.SessionID
				if handler.tm.GetTranscodeSession(sessionID) != nil {
					t.Error("transport published before durable attempt was saved")
				}
				if !shutdown {
					return errors.New("forced attempt persistence failure")
				}
				shutdownCtx, cancel := context.WithCancel(t.Context())
				done := handler.tm.StartShutdownCleanup(shutdownCtx)
				cancel()
				<-done
				return underlying.SaveAttempt(ctx, record)
			}}
			request := func() *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				handler.HandleStartPlayback(rr, httptest.NewRequest(http.MethodPost, "/api/v1/playback/start", strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext()))
				return rr
			}
			rr := request()
			if sessionID == "" {
				t.Fatalf("did not reach SaveAttempt: %d %s", rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), `"outcome":"playable"`) {
				t.Fatalf("failed start returned playable: %s", rr.Body.String())
			}
			if handler.tm.GetTranscodeSession(sessionID) != nil {
				t.Fatal("failed start leaked published transport")
			}
			if _, err := handler.sessionMgr.GetSession(sessionID); err == nil {
				t.Fatal("failed start retained live playback session")
			}
			if shutdown {
				rr = request()
				if strings.Contains(rr.Body.String(), `"outcome":"playable"`) || !strings.Contains(rr.Body.String(), "session_expired") {
					t.Fatalf("shutdown attempt replay: %d %s", rr.Code, rr.Body.String())
				}
			}
		})
	}
}
