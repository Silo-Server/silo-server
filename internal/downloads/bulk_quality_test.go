package downloads

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

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
