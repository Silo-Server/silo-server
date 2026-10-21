package downloads

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestBulkQuality(t *testing.T) {
	cases := []struct {
		name string
		req  CreateRequest
		want string
		err  error
	}{
		{name: "empty is original", req: CreateRequest{}, want: QualityOriginal},
		{name: "v1 keeps original", req: CreateRequest{Quality: QualityOriginal, DeviceID: "device"}, want: QualityOriginal},
		{name: "v1 refuses a preset", req: CreateRequest{Quality: Quality5Mbps, DeviceID: "device"}, err: ErrBulkQualityUnavailable},
		{name: "native preset needs a device", req: CreateRequest{Quality: Quality5Mbps, BulkQuality: true}, err: ErrBulkQualityUnavailable},
		{name: "native preset", req: CreateRequest{Quality: Quality5Mbps, BulkQuality: true, DeviceID: "device"}, want: Quality5Mbps},
		{name: "unknown preset", req: CreateRequest{Quality: "4mbps", BulkQuality: true, DeviceID: "device"}, err: ErrInvalidQuality},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bulkQuality(tc.req)
			if !errors.Is(err, tc.err) || got != tc.want {
				t.Fatalf("bulkQuality = %q, %v; want %q, %v", got, err, tc.want, tc.err)
			}
		})
	}
}

func bulkQualityTestService(t *testing.T, user *models.User) (*Service, *PolicyUser, config.DownloadConfig) {
	t.Helper()
	svc := NewService(nil, nil, nil, nil, nil, nil, fakeUserRepo{user}, nil, nil, &config.DownloadConfig{Enabled: true, TranscodeEnabled: true})
	svc.SetArtifactManager(&ArtifactManager{})
	cfg, policyUser, err := svc.downloadConfigForUser(t.Context(), 1, "device")
	if err != nil {
		t.Fatal(err)
	}
	return svc, policyUser, cfg
}

func TestResolveItemDecisionsSkipsEpisodesThePresetCannotReach(t *testing.T) {
	svc, user, cfg := bulkQualityTestService(t, &models.User{DownloadAllowed: ptrBool(true), DownloadTranscodeAllowed: ptrBool(true)})
	hd := managedItem{episodeID: "hd", file: &models.MediaFile{ID: 1, CodecVideo: "h264", CodecAudio: "aac", Container: "mp4", Resolution: "1080p"}}
	// 4K sources need the 4K transcode setting, which this server cannot read.
	uhd := managedItem{episodeID: "uhd", file: &models.MediaFile{ID: 2, CodecVideo: "hevc", CodecAudio: "aac", Container: "mkv", Resolution: "2160p"}}

	kept, decisions, skipped, err := svc.resolveItemDecisions(t.Context(), Quality5Mbps, user, cfg, playback.ClientCapabilities{}, "device", []managedItem{hd, uhd}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].episodeID != "hd" || len(decisions) != 1 {
		t.Fatalf("kept %+v decisions %+v", kept, decisions)
	}
	if d := decisions[0]; d.EffectiveQuality != Quality5Mbps || !d.RequiresArtifact || d.TargetBitrateKbps != 5000 {
		t.Fatalf("decision %+v", d)
	}
	if len(skipped) != 1 || skipped[0].EpisodeID != "uhd" || skipped[0].Reason != SkipReasonQualityUnavailable {
		t.Fatalf("skipped %+v", skipped)
	}
}

func TestResolveItemDecisionsFailsTheBatchWhenTranscodingIsNotAllowed(t *testing.T) {
	svc, user, cfg := bulkQualityTestService(t, &models.User{DownloadAllowed: ptrBool(true), DownloadTranscodeAllowed: ptrBool(false)})
	hd := managedItem{episodeID: "hd", file: &models.MediaFile{ID: 1, CodecVideo: "h264", CodecAudio: "aac", Container: "mp4", Resolution: "1080p"}}
	if _, _, _, err := svc.resolveItemDecisions(t.Context(), Quality5Mbps, user, cfg, playback.ClientCapabilities{}, "device", []managedItem{hd}, false); !errors.Is(err, ErrDownloadNotAllowed) {
		t.Fatalf("err = %v, want ErrDownloadNotAllowed", err)
	}
}

func TestValidateMonitorQuality(t *testing.T) {
	allowed, user, cfg := bulkQualityTestService(t, &models.User{DownloadAllowed: ptrBool(true), DownloadTranscodeAllowed: ptrBool(true)})
	for requested, want := range map[string]string{"": QualityOriginal, QualityOriginal: QualityOriginal, Quality10Mbps: Quality10Mbps} {
		got, err := allowed.validateMonitorQuality(t.Context(), requested, user, cfg, "device")
		if err != nil || got != want {
			t.Fatalf("validateMonitorQuality(%q) = %q, %v; want %q", requested, got, err, want)
		}
	}
	if _, err := allowed.validateMonitorQuality(t.Context(), "4mbps", user, cfg, "device"); !errors.Is(err, ErrInvalidQuality) {
		t.Fatalf("unknown preset err = %v", err)
	}

	denied, user, cfg := bulkQualityTestService(t, &models.User{DownloadAllowed: ptrBool(true), DownloadTranscodeAllowed: ptrBool(false)})
	if _, err := denied.validateMonitorQuality(t.Context(), Quality10Mbps, user, cfg, "device"); !errors.Is(err, ErrDownloadNotAllowed) {
		t.Fatalf("transcode denied err = %v", err)
	}
	if got, err := denied.validateMonitorQuality(t.Context(), QualityOriginal, user, cfg, "device"); err != nil || got != QualityOriginal {
		t.Fatalf("original without transcode = %q, %v", got, err)
	}
}

func TestSubscriptionQualityPostgres(t *testing.T) {
	repo := subscriptionMutationTestRepo(t)
	legacy, err := repo.CreateOrGet(t.Context(), &Subscription{ID: "legacy", UserID: 1, ProfileID: "profile", DeviceID: "device", SeriesID: "legacy", Mode: SubModeAll})
	if err != nil || legacy.Quality != QualityOriginal {
		t.Fatalf("monitor without a quality = %+v, %v", legacy, err)
	}
	stored, err := repo.CreateOrGet(t.Context(), &Subscription{ID: "monitor", UserID: 1, ProfileID: "profile", DeviceID: "device", SeriesID: "series", Mode: SubModeFuture, Quality: Quality10Mbps})
	if err != nil || stored.Quality != Quality10Mbps {
		t.Fatalf("created %+v, %v", stored, err)
	}
	// A repeated create keeps the monitor's quality.
	replay, err := repo.CreateOrGet(t.Context(), &Subscription{ID: "retry", UserID: 1, ProfileID: "profile", DeviceID: "device", SeriesID: "series", Mode: SubModeFuture, Quality: Quality2Mbps})
	if err != nil || replay.ID != "monitor" || replay.Quality != Quality10Mbps {
		t.Fatalf("replay %+v, %v", replay, err)
	}
	updated, err := repo.Mutate(t.Context(), 1, "profile", "device", "monitor", false, func(row *Subscription) error { row.Quality = Quality2Mbps; return nil })
	if err != nil || updated.Quality != Quality2Mbps {
		t.Fatalf("updated %+v, %v", updated, err)
	}
	current, err := repo.GetByID(t.Context(), "monitor", 1, "profile", "device")
	if err != nil || current.Quality != Quality2Mbps {
		t.Fatalf("reread %+v, %v", current, err)
	}
}

func TestPaceToSlotsLimitsOnlyPreparedItems(t *testing.T) {
	items := []managedItem{{episodeID: "a"}, {episodeID: "b"}, {episodeID: "c"}, {episodeID: "d"}}
	decisions := map[ManagedEntryKey]QualityDecision{
		{EpisodeID: "a"}: {RequiresArtifact: true},
		{EpisodeID: "b"}: {},
		{EpisodeID: "c"}: {RequiresArtifact: true},
		{EpisodeID: "d"}: {RequiresArtifact: true},
	}
	ids := func(items []managedItem) string {
		out := ""
		for _, it := range items {
			out += it.episodeID
		}
		return out
	}
	notReady := func(managedItem) bool { return false }
	for slots, want := range map[int]string{-1: "abcd", 0: "b", 1: "ab", 2: "abc", 5: "abcd"} {
		if got := ids(paceToSlots(items, decisions, slots, notReady)); got != want {
			t.Errorf("slots %d: kept %q, want %q", slots, got, want)
		}
	}
	// A prepared file that is already ready takes no slot.
	cReady := func(it managedItem) bool { return it.episodeID == "c" }
	if got := ids(paceToSlots(items, decisions, 0, cReady)); got != "bc" {
		t.Errorf("no slots, c ready: kept %q, want %q", got, "bc")
	}
	if got := ids(paceToSlots(items, decisions, 1, cReady)); got != "abc" {
		t.Errorf("one slot, c ready: kept %q, want %q", got, "abc")
	}
}

func TestResolveItemDecisionsRetriesCapabilityFailuresLaterForMonitors(t *testing.T) {
	svc, user, cfg := bulkQualityTestService(t, &models.User{DownloadAllowed: ptrBool(true), DownloadTranscodeAllowed: ptrBool(true)})
	svc.artifacts.settings = failingDownloadSettings{err: errors.New("settings store down")}
	hd := managedItem{episodeID: "hd", file: &models.MediaFile{ID: 1, CodecVideo: "h264", CodecAudio: "aac", Container: "mp4", Resolution: "1080p"}}
	// A 4K source needs the tone-map settings, which fail to load.
	uhd := managedItem{episodeID: "uhd", file: &models.MediaFile{ID: 2, CodecVideo: "hevc", CodecAudio: "aac", Container: "mkv", Resolution: "2160p"}}
	if _, _, _, err := svc.resolveItemDecisions(t.Context(), Quality5Mbps, user, cfg, playback.ClientCapabilities{}, "device", []managedItem{hd, uhd}, false); !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("batch err = %v, want ErrCapabilityUnavailable", err)
	}
	kept, _, skipped, err := svc.resolveItemDecisions(t.Context(), Quality5Mbps, user, cfg, playback.ClientCapabilities{}, "device", []managedItem{hd, uhd}, true)
	if err != nil || len(kept) != 1 || kept[0].episodeID != "hd" || len(skipped) != 0 {
		t.Fatalf("monitor kept %+v skipped %+v err %v", kept, skipped, err)
	}
}

func TestMonitorPatchRechecksQualityOnlyWhenItChangesPostgres(t *testing.T) {
	subs := subscriptionMutationTestRepo(t)
	if _, err := subs.CreateOrGet(t.Context(), &Subscription{ID: "monitor", UserID: 1, ProfileID: "profile", DeviceID: "device", SeriesID: "series", Mode: SubModeFuture, Quality: Quality10Mbps}); err != nil {
		t.Fatal(err)
	}
	// Transcoding was allowed when the monitor chose 10 Mbps and is not now.
	svc := NewService(NewRepository(subs.pool), nil, nil, nil, nil, nil, fakeUserRepo{&models.User{DownloadAllowed: ptrBool(true), DownloadTranscodeAllowed: ptrBool(false)}}, &syncAccess{}, nil, &config.DownloadConfig{Enabled: true, TranscodeEnabled: true})
	svc.subRepo = subs
	svc.SetArtifactManager(&ArtifactManager{})
	anyVersion := func(*Subscription) error { return nil }

	paused, err := svc.UpdateSubscriptionMonitor(t.Context(), 1, "profile", "device", "monitor", SubscriptionPatch{Quality: new(Quality10Mbps), Active: new(false)}, catalog.AccessFilter{}, anyVersion)
	if err != nil || paused.Active || paused.Quality != Quality10Mbps {
		t.Fatalf("echoed quality: %+v %v", paused, err)
	}
	if _, err := svc.UpdateSubscriptionMonitor(t.Context(), 1, "profile", "device", "monitor", SubscriptionPatch{Quality: new(Quality5Mbps)}, catalog.AccessFilter{}, anyVersion); !errors.Is(err, ErrDownloadNotAllowed) {
		t.Fatalf("changed quality err = %v, want ErrDownloadNotAllowed", err)
	}
}

func TestReplacementAddsActive(t *testing.T) {
	cases := []struct {
		name   string
		status string
		ready  bool
		want   bool
	}{
		{"ready entry, target still to prepare", StatusReady, false, true},
		{"ready entry, target already prepared", StatusReady, true, false},
		{"failed entry, target still to prepare", StatusFailed, false, true},
		{"entry already active", StatusPreparing, false, false},
		{"entry downloading", StatusDownloading, false, false},
	}
	for _, tc := range cases {
		if got := replacementAddsActive(&Download{Status: tc.status}, tc.ready); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
