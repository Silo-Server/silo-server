package playback

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

type stubSubtitleInventoryResolver struct {
	file          *models.MediaFile
	additional    []SubtitleInventoryEntryV3
	err           error
	additionalErr error
	calls         int
}

func (s *stubSubtitleInventoryResolver) MediaFile(context.Context, int) (*models.MediaFile, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.file, nil
}

func (s *stubSubtitleInventoryResolver) AdditionalSubtitles(context.Context, *models.MediaFile) ([]SubtitleInventoryEntryV3, error) {
	return s.additional, s.additionalErr
}

// A generated track's realtime event carries the ordinal the next plan will
// publish, so the client selects it by identity instead of counting the tracks
// it can currently see.
func TestSubtitleReadyNotifierPublishesTheGeneratedTrackOrdinal(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(session.ID, true)

	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	reg := hub.Register(session.ID, conn)
	defer hub.Unregister(reg)

	file := &models.MediaFile{
		ID: 100,
		ExternalSubtitles: []models.ExternalSubtitle{
			{Path: "/media/movie.en.srt", Language: "en", Format: "srt"},
		},
		SubtitleTracks: []models.SubtitleTrack{
			{Index: 0, Language: "ja", Codec: "hdmv_pgs_subtitle"},
			// A burn-in-only track the client never sees in subtitle_urls. The
			// generated track still lands after it.
			{Index: 1, Language: "de", Codec: "dvd_subtitle"},
		},
	}
	resolver := &stubSubtitleInventoryResolver{
		file:       file,
		additional: []SubtitleInventoryEntryV3{{CombinedIndex: 3, Codec: "srt", Source: SubtitleSourceDownloadedV3, Language: "es", Label: "Spanish (AI)", DownloadedSubtitleID: 77}},
	}

	notifier := NewSubtitleReadyNotifier(sessions, hub, resolver)
	notifier.SubtitleReady(context.Background(), 100, 77, "es", "Spanish (AI)")

	if len(conn.messages) != 1 {
		t.Fatalf("messages = %d, want 1 subtitle ready event", len(conn.messages))
	}
	event, ok := conn.messages[0].(EventEnvelope)
	if !ok {
		t.Fatalf("message type = %T, want EventEnvelope", conn.messages[0])
	}

	var payload SubtitleReadyPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload): %v", err)
	}
	if payload.Track == nil {
		t.Fatal("payload.Track is nil; the event must name the new track's ordinal")
	}
	if payload.Track.CombinedIndex != 3 {
		t.Errorf("track combined_index = %d, want 3 (1 external + 2 embedded)", payload.Track.CombinedIndex)
	}
	if want := TrackIDV3(file.ID, "subtitle", 3); payload.Track.TrackID != want {
		t.Errorf("track_id = %q, want %q", payload.Track.TrackID, want)
	}
	if payload.Track.Delivery != SubtitleDeliverySidecarV3 {
		t.Errorf("track delivery = %q, want %q", payload.Track.Delivery, SubtitleDeliverySidecarV3)
	}
	if payload.Track.URL == "" {
		t.Error("a sidecar track must carry a session-scoped URL")
	}
}

func TestSubtitleReadyNotifierOmitsTrackWhenTheFileCannotBeResolved(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(session.ID, true)

	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	reg := hub.Register(session.ID, conn)
	defer hub.Unregister(reg)

	resolver := &stubSubtitleInventoryResolver{err: errors.New("file gone")}
	notifier := NewSubtitleReadyNotifier(sessions, hub, resolver)
	notifier.SubtitleReady(context.Background(), 100, 77, "es", "Spanish (AI)")

	if len(conn.messages) != 1 {
		t.Fatalf("messages = %d, want the event to be delivered without a track block", len(conn.messages))
	}
	var payload SubtitleReadyPayload
	if err := json.Unmarshal(conn.messages[0].(EventEnvelope).Payload, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload): %v", err)
	}
	if payload.Track != nil {
		t.Errorf("payload.Track = %+v, want nil when the file cannot be resolved", payload.Track)
	}
	if payload.SubtitleID != 77 || payload.Language != "es" {
		t.Errorf("payload lost its identifiers: %+v", payload)
	}
}

func TestSubtitleReadyNotifierOmitsTrackWhenDownloadedInventoryCannotBeResolved(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(session.ID, true)

	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	reg := hub.Register(session.ID, conn)
	defer hub.Unregister(reg)

	resolver := &stubSubtitleInventoryResolver{
		file:          &models.MediaFile{ID: 100},
		additionalErr: errors.New("subtitle repository unavailable"),
	}
	NewSubtitleReadyNotifier(sessions, hub, resolver).SubtitleReady(context.Background(), 100, 77, "es", "Spanish (AI)")

	if len(conn.messages) != 1 {
		t.Fatalf("messages = %d, want one metadata-only event", len(conn.messages))
	}
	var payload SubtitleReadyPayload
	if err := json.Unmarshal(conn.messages[0].(EventEnvelope).Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Track != nil {
		t.Fatalf("track = %#v, want nil when the ordinal inventory is unavailable", payload.Track)
	}
}

func TestSubtitleReadyNotifierWorksWithoutAnInventoryResolver(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(session.ID, true)

	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	reg := hub.Register(session.ID, conn)
	defer hub.Unregister(reg)

	notifier := NewSubtitleReadyNotifier(sessions, hub, nil)
	if notifier == nil {
		t.Fatal("a nil inventory resolver must not disable the notifier")
	}
	notifier.SubtitleReady(context.Background(), 100, 77, "es", "Spanish (AI)")

	if len(conn.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(conn.messages))
	}
}

func TestSubtitleReadyNotifierTranslationCompletedCarriesTheTrack(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(session.ID, true)

	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	reg := hub.Register(session.ID, conn)
	defer hub.Unregister(reg)

	resolver := &stubSubtitleInventoryResolver{
		file:       &models.MediaFile{ID: 100, SubtitleTracks: []models.SubtitleTrack{{Index: 0, Language: "en", Codec: "subrip"}}},
		additional: []SubtitleInventoryEntryV3{{CombinedIndex: 1, Codec: "srt", Source: SubtitleSourceDownloadedV3, Language: "fr", Label: "French (AI)", DownloadedSubtitleID: 55}},
	}

	notifier := NewSubtitleReadyNotifier(sessions, hub, resolver)
	notifier.TranslationCompleted(context.Background(), session.ID, 100, 9, "track-key", 55, "fr", "French (AI)")

	if len(conn.messages) != 1 {
		t.Fatalf("messages = %d, want 1 translation completed event", len(conn.messages))
	}
	var payload SubtitleTranslationCompletedPayload
	if err := json.Unmarshal(conn.messages[0].(EventEnvelope).Payload, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload): %v", err)
	}
	if payload.Track == nil || payload.Track.CombinedIndex != 1 {
		t.Fatalf("payload.Track = %+v, want the ordinal-1 generated track", payload.Track)
	}
	if payload.JobID != 9 || payload.TrackKey != "track-key" || payload.SubtitleID != 55 {
		t.Errorf("payload lost its identifiers: %+v", payload)
	}
}

func TestSubtitleReadyNotifierMatchesDownloadedRowIdentity(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(session.ID, true)
	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	reg := hub.Register(session.ID, conn)
	defer hub.Unregister(reg)

	resolver := &stubSubtitleInventoryResolver{
		file: &models.MediaFile{ID: 100},
		additional: []SubtitleInventoryEntryV3{
			{Codec: "srt", Source: SubtitleSourceDownloadedV3, Language: "es", Label: "Spanish", DownloadedSubtitleID: 77},
			{Codec: "srt", Source: SubtitleSourceDownloadedV3, Language: "es", Label: "Spanish", DownloadedSubtitleID: 88},
		},
	}
	NewSubtitleReadyNotifier(sessions, hub, resolver).SubtitleReady(context.Background(), 100, 77, "es", "Spanish")
	var payload SubtitleReadyPayload
	if err := json.Unmarshal(conn.messages[0].(EventEnvelope).Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Track == nil || payload.Track.CombinedIndex != 0 {
		t.Fatalf("track = %#v, want exact row 77 at ordinal 0", payload.Track)
	}
}

// Sessions without a realtime connection must not cost a repository lookup.
func TestSubtitleReadyNotifierSkipsSessionsWithoutRealtime(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	_ = session

	hub := NewRealtimeHub()
	resolver := &stubSubtitleInventoryResolver{file: &models.MediaFile{ID: 100}}
	notifier := NewSubtitleReadyNotifier(sessions, hub, resolver)
	notifier.SubtitleReady(context.Background(), 100, 77, "es", "Spanish (AI)")

	if resolver.calls != 0 {
		t.Errorf("resolver called %d times for a session with no realtime connection, want 0", resolver.calls)
	}
}
