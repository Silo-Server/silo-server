package playback

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type stubSubtitleInventoryResolver struct {
	file          *models.MediaFile
	additional    []SubtitleInventoryEntryV3
	err           error
	additionalErr error
	features      []string
	featuresErr   error
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

func (s *stubSubtitleInventoryResolver) SessionClientFeatures(context.Context, string) ([]string, error) {
	return s.features, s.featuresErr
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
	NewSubtitleReadyNotifier(sessions, hub, resolver).SubtitleReady(context.Background(), 100, 88, "es", "Spanish")
	var payload SubtitleReadyPayload
	if err := json.Unmarshal(conn.messages[0].(EventEnvelope).Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Track == nil || payload.Track.CombinedIndex != 1 {
		t.Fatalf("track = %#v, want exact row 88 at ordinal 1", payload.Track)
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

// A realtime event must publish the generated track under the same URL the
// session's plans use, so a session that negotiated subrip_sidecar_v1 sees the
// stored SRT here too.
func TestSubtitleReadyNotifierUsesTheSessionSidecarRepresentation(t *testing.T) {
	for name, tc := range map[string]struct {
		features []string
		want     string
	}{
		"negotiated":     {[]string{FeatureSubripSidecarV3}, "/subtitles/0.srt?file_id=100&original=1&downloaded_subtitle_id=77"},
		"not negotiated": {nil, "/subtitles/0.vtt?file_id=100&downloaded_subtitle_id=77"},
	} {
		t.Run(name, func(t *testing.T) {
			resolver := &stubSubtitleInventoryResolver{
				file:       &models.MediaFile{ID: 100},
				additional: []SubtitleInventoryEntryV3{{CombinedIndex: 0, Codec: "srt", Source: SubtitleSourceDownloadedV3, DownloadedSubtitleID: 77}},
				features:   tc.features,
			}
			notifier := &SubtitleReadyNotifier{inventory: resolver}
			track := notifier.resolveTrack(t.Context(), "sess", 100, downloadedTrack(77))
			if track == nil || track.URL != "/stream/sess"+tc.want {
				t.Fatalf("track = %#v, want URL /stream/sess%s", track, tc.want)
			}
		})
	}
}

// When the session's negotiated representation cannot be read, the event
// omits the track instead of guessing a URL; the client refetches its plan.
func TestSubtitleReadyNotifierOmitsTrackWhenSessionFeaturesAreUnknown(t *testing.T) {
	resolver := &stubSubtitleInventoryResolver{
		file:        &models.MediaFile{ID: 100},
		additional:  []SubtitleInventoryEntryV3{{CombinedIndex: 0, Codec: "srt", Source: SubtitleSourceDownloadedV3, DownloadedSubtitleID: 77}},
		featuresErr: errors.New("attempt store unavailable"),
	}
	notifier := &SubtitleReadyNotifier{inventory: resolver}
	if track := notifier.resolveTrack(t.Context(), "sess", 100, downloadedTrack(77)); track != nil {
		t.Fatalf("track = %#v, want it omitted while the representation is unknown", track)
	}
}

func TestSubtitleTimingChangedDeliversAcrossReplicasOnce(t *testing.T) {
	replica := func() (*SubtitleReadyNotifier, *dispatchTestConn) {
		sessions := NewSessionManager(0, 0)
		session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
		_ = sessions.SetRealtimeConnection(session.ID, true)
		hub := NewRealtimeHub()
		conn := &dispatchTestConn{}
		reg := hub.Register(session.ID, conn)
		t.Cleanup(func() { hub.Unregister(reg) })
		return NewSubtitleReadyNotifier(sessions, hub, nil), conn
	}
	bus := &subtitleTestBus{}
	local, localConn := replica()
	remote, remoteConn := replica()
	ctx := context.Background()
	for _, n := range []*SubtitleReadyNotifier{local, remote, local} {
		if err := n.UseEventBus(ctx, bus.publish, bus.subscribe); err != nil {
			t.Fatal(err)
		}
	}
	if len(bus.handlers) != 2 {
		t.Fatalf("subscriptions = %d, want 2", len(bus.handlers))
	}
	local.SubtitleTimingChanged(ctx, subtitles.SyncTarget{MediaFileID: 100, StoredID: 9})
	local.SubtitleTimingChanged(ctx, subtitles.SyncTarget{MediaFileID: 100, ExternalPath: "/media/film.en.srt"})
	if len(bus.events) != 2 {
		t.Fatalf("published events = %d, want 2 without rebroadcast", len(bus.events))
	}
	for name, conn := range map[string]*dispatchTestConn{"local": localConn, "remote": remoteConn} {
		if len(conn.messages) != 2 {
			t.Fatalf("%s messages = %d, want 2", name, len(conn.messages))
		}
		for i, want := range []SubtitleTimingChangedPayload{
			{SubtitleID: 9, SyncKey: "stored-9"},
			{SyncKey: subtitles.ExternalSyncKey("/media/film.en.srt")},
		} {
			event := conn.messages[i].(EventEnvelope)
			var payload SubtitleTimingChangedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil || event.Name != RealtimeEventSubtitleTimingChanged ||
				payload.SubtitleID != want.SubtitleID || payload.SyncKey != want.SyncKey || payload.FileID != 100 {
				t.Fatalf("%s event %d %+v payload %+v err %v", name, i, event, payload, err)
			}
		}
	}
}

// A server that predates sidecar sync publishes only the stored subtitle's
// ID; the event still names it by sync key.
func TestSubtitleTimingChangedAcceptsMessagesWithoutSyncKey(t *testing.T) {
	sessions := NewSessionManager(0, 0)
	session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
	_ = sessions.SetRealtimeConnection(session.ID, true)
	hub := NewRealtimeHub()
	conn := &dispatchTestConn{}
	reg := hub.Register(session.ID, conn)
	t.Cleanup(func() { hub.Unregister(reg) })
	bus := &subtitleTestBus{}
	if err := NewSubtitleReadyNotifier(sessions, hub, nil).UseEventBus(context.Background(), bus.publish, bus.subscribe); err != nil {
		t.Fatal(err)
	}
	for _, handler := range bus.handlers {
		handler(RealtimeEventSubtitleTimingChanged, `{"source_id":"older-server","file_id":100,"subtitle_id":9}`)
	}
	if len(conn.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(conn.messages))
	}
	var payload SubtitleTimingChangedPayload
	if err := json.Unmarshal(conn.messages[0].(EventEnvelope).Payload, &payload); err != nil || payload.SyncKey != "stored-9" || payload.SubtitleID != 9 {
		t.Fatalf("payload %+v err %v", payload, err)
	}
}

// subtitleTestBus is an in-memory event bus shared by notifier replicas.
type subtitleTestBus struct {
	handlers []func(RealtimeEventName, string)
	events   []RealtimeEventName
}

func (b *subtitleTestBus) publish(_ context.Context, event RealtimeEventName, payload string) error {
	b.events = append(b.events, event)
	for _, handler := range b.handlers {
		handler(event, payload)
	}
	return nil
}

func (b *subtitleTestBus) subscribe(_ context.Context, handler func(RealtimeEventName, string)) error {
	b.handlers = append(b.handlers, handler)
	return nil
}

func TestSubtitleSyncUpdatedReachesEveryReplicaOnce(t *testing.T) {
	replica := func() (*SubtitleReadyNotifier, *dispatchTestConn) {
		sessions := NewSessionManager(0, 0)
		session, _ := sessions.StartSession(1, "profile-a", 100, PlayDirect, false)
		_ = sessions.SetRealtimeConnection(session.ID, true)
		hub := NewRealtimeHub()
		conn := &dispatchTestConn{}
		reg := hub.Register(session.ID, conn)
		t.Cleanup(func() { hub.Unregister(reg) })
		return NewSubtitleReadyNotifier(sessions, hub, nil), conn
	}
	bus := &subtitleTestBus{}
	local, localConn := replica()
	remote, remoteConn := replica()
	for _, n := range []*SubtitleReadyNotifier{local, remote} {
		if err := n.UseEventBus(context.Background(), bus.publish, bus.subscribe); err != nil {
			t.Fatal(err)
		}
	}
	progress := 0.45
	key := subtitles.ExternalSyncKey("/media/film.en.srt")
	local.SubtitleSyncUpdated(context.Background(), SubtitleSyncUpdate{FileID: 100, SyncKey: key, Timing: SubtitleSyncTiming{Scale: 1},
		Job: SubtitleSyncJob{ID: "7", Status: "running", Trigger: "manual", Phase: "analyzing", Progress: &progress, CreatedAt: "2026-10-04T00:00:00.000Z"}})
	if len(bus.events) != 1 || bus.events[0] != RealtimeEventSubtitleSyncUpdated {
		t.Fatalf("published %v", bus.events)
	}
	for name, conn := range map[string]*dispatchTestConn{"local": localConn, "remote": remoteConn} {
		if len(conn.messages) != 1 {
			t.Fatalf("%s messages = %d, want 1", name, len(conn.messages))
		}
		event := conn.messages[0].(EventEnvelope)
		var payload SubtitleSyncUpdatedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil || event.Name != RealtimeEventSubtitleSyncUpdated ||
			payload.SyncKey != key || payload.SubtitleID != 0 || payload.Job.Phase != "analyzing" || *payload.Job.Progress != progress {
			t.Fatalf("%s event %+v payload %+v err %v", name, event, payload, err)
		}
	}
}
