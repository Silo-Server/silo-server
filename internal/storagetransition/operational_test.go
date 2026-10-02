package storagetransition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// A local root holds every blob kind under one namespace since operational
// storage became backend-neutral.
func localRootObjects() map[string][]byte {
	return map[string][]byte{
		"tmdb/movie/1/poster/a.webp":        []byte("artwork"),
		"branding/logo.webp":                []byte("logo"),
		"subtitles/7/en.srt":                []byte("subtitle"),
		"profile-avatars/1/avatar.webp":     []byte("avatar"),
		"diagnostics/1/report.tar.gz":       []byte("bundle"),
		"catalog-seeds/export/seed.json.gz": []byte("seed"),
	}
}

func stagedValues(t *testing.T, values map[string]string) *memorySettings {
	t.Helper()
	raw, err := json.Marshal(stagedTarget{Values: values})
	if err != nil {
		t.Fatal(err)
	}
	return &memorySettings{values: map[string]string{StagedTargetSettingKey: string(raw)}}
}

func assertObjects(t *testing.T, name string, store *memoryStore, present, absent []string) {
	t.Helper()
	for _, key := range present {
		if _, ok := store.objects[key]; !ok {
			t.Errorf("%s is missing %s", name, key)
		}
	}
	for _, key := range absent {
		if _, ok := store.objects[key]; ok {
			t.Errorf("%s unexpectedly holds %s", name, key)
		}
	}
}

func TestLocalToS3MigrateAllSplitsSharedRootByOwner(t *testing.T) {
	objects := localRootObjects()
	objects["diagnostics/2/large.tar.gz"] = []byte("a bundle larger than the small-object limit")
	source := &memoryStore{identity: "local|/srv/silo", objects: objects}
	target := &memoryStore{identity: "s3|https://s3|public|", objects: map[string][]byte{}}
	targetPrivate := &memoryStore{identity: "s3|https://s3|private|", objects: map[string][]byte{}}
	service := testService(stagedValues(t, map[string]string{"artwork.storage_backend": "s3", "s3.public_bucket": "public", "s3.private_bucket": "private"}), source)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	service.openPrivate = func(map[string]string) blobstore.Store { return targetPrivate }
	service.smallObjectBytes = 16

	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	assertObjects(t, "public target", target,
		[]string{"tmdb/movie/1/poster/a.webp", "branding/logo.webp", "subtitles/7/en.srt"},
		[]string{"profile-avatars/1/avatar.webp", "diagnostics/1/report.tar.gz", "catalog-seeds/export/seed.json.gz"})
	assertObjects(t, "private target", targetPrivate,
		[]string{"profile-avatars/1/avatar.webp", "diagnostics/1/report.tar.gz", "catalog-seeds/export/seed.json.gz"},
		[]string{"tmdb/movie/1/poster/a.webp", "branding/logo.webp", "subtitles/7/en.srt"})
	// Small objects go through Put, which records the checksum S3 compares in
	// Matches; only objects over the limit stream.
	if target.streams != 0 || targetPrivate.streams != 1 {
		t.Fatalf("streamed copies: public=%d private=%d, want only the large bundle", target.streams, targetPrivate.streams)
	}
}

func TestS3ToLocalMigrateAllBringsOperationalDataIntoRoot(t *testing.T) {
	source := &memoryStore{identity: "s3|https://s3|public|", objects: map[string][]byte{
		"tmdb/movie/1/poster/a.webp": []byte("artwork"),
		"subtitles/7/en.srt":         []byte("subtitle"),
	}}
	private := &memoryStore{identity: "s3|https://s3|private|", objects: map[string][]byte{
		"profile-avatars/1/avatar.webp":     []byte("avatar"),
		"diagnostics/1/report.tar.gz":       []byte("bundle"),
		"catalog-seeds/export/seed.json.gz": []byte("seed"),
	}}
	target := &memoryStore{identity: "local|/srv/silo", objects: map[string][]byte{}}
	service := New(nil, stagedValues(t, map[string]string{"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo"}), nil, source, private)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }

	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	assertObjects(t, "local target", target, []string{
		"tmdb/movie/1/poster/a.webp", "subtitles/7/en.srt",
		"profile-avatars/1/avatar.webp", "diagnostics/1/report.tar.gz", "catalog-seeds/export/seed.json.gz",
	}, nil)
}

func TestS3ToLocalPreserveUploadsLeavesArtifactsBehind(t *testing.T) {
	source := &memoryStore{identity: "s3|https://s3|public|", objects: map[string][]byte{
		"tmdb/movie/1/poster/a.webp": []byte("artwork"),
		"subtitles/7/en.srt":         []byte("subtitle"),
		"library-posters/3.webp":     []byte("poster"),
	}}
	private := &memoryStore{identity: "s3|https://s3|private|", objects: map[string][]byte{
		"profile-avatars/1/avatar.webp": []byte("avatar"),
		"diagnostics/1/report.tar.gz":   []byte("bundle"),
	}}
	target := &memoryStore{identity: "local|/srv/silo", objects: map[string][]byte{}}
	service := New(nil, stagedValues(t, map[string]string{"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo"}), nil, source, private)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	service.reconcile = testService(nil, source).reconcile

	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyPreserveUploads}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	assertObjects(t, "local target", target,
		[]string{"subtitles/7/en.srt", "library-posters/3.webp", "profile-avatars/1/avatar.webp"},
		[]string{"tmdb/movie/1/poster/a.webp", "diagnostics/1/report.tar.gz"})
}

// A local install adding a private bucket moves only operational data; the
// artwork root and its identity stay put.
func TestLocalPrivateOnlyTransitionMovesOperationalDataToBucket(t *testing.T) {
	source := &memoryStore{identity: "local|/srv/silo", objects: localRootObjects()}
	targetPrivate := &memoryStore{identity: "s3|https://s3|private|", objects: map[string][]byte{}}
	settings := stagedValues(t, map[string]string{"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo", "s3.private_bucket": "private"})
	service := testService(settings, source)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return source, nil }
	service.openPrivate = func(map[string]string) blobstore.Store { return targetPrivate }

	value, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {})
	if err != nil {
		t.Fatal(err)
	}
	assertObjects(t, "private target", targetPrivate,
		[]string{"profile-avatars/1/avatar.webp", "diagnostics/1/report.tar.gz", "catalog-seeds/export/seed.json.gz"},
		[]string{"tmdb/movie/1/poster/a.webp", "subtitles/7/en.srt"})
	var committed stagedTarget
	if err := json.Unmarshal([]byte(settings.values[StagedTargetSettingKey]), &committed); err != nil {
		t.Fatal(err)
	}
	if committed.PublicReconcile || committed.BrandingReconcile {
		t.Fatalf("private-only transition scheduled public reconciliation: %#v", committed)
	}
	if result := value.(Result); result.TargetIdentity != source.Identity() {
		t.Fatalf("target identity = %q, want unchanged %q", result.TargetIdentity, source.Identity())
	}
	if got := settings.values[blobstore.OperationalIdentitySettingKey]; got != targetPrivate.Identity() {
		t.Fatalf("recorded private identity = %q, want %q", got, targetPrivate.Identity())
	}
}

func TestEmptyPrivateOnlyTransitionLeavesArtworkLocationEditable(t *testing.T) {
	for _, assetWrittenBeforeCommit := range []bool{false, true} {
		name := "no asset write"
		if assetWrittenBeforeCommit {
			name = "asset write before commit"
		}
		t.Run(name, func(t *testing.T) {
			assets := &memoryStore{identity: "local|/srv/silo", objects: map[string][]byte{}}
			oldPrivate := &memoryStore{identity: "s3|https://s3|private-old|", objects: map[string][]byte{}}
			newPrivate := &memoryStore{identity: "s3|https://s3|private-new|", objects: map[string][]byte{}}
			stage := stagedTarget{
				ID:                  "empty-private-only",
				Policy:              PolicyMigrateAll,
				SourceIdentity:      assets.Identity(),
				SourcePrivateBucket: "private-old",
				TargetPrivateBucket: "private-new",
				Phase:               transitionPhaseStaged,
				Values: map[string]string{
					settingArtworkBackend:   blobstore.BackendLocal,
					settingArtworkLocalPath: "/srv/silo",
					settingPrivateBucket:    "private-new",
				},
			}
			raw, err := json.Marshal(stage)
			if err != nil {
				t.Fatal(err)
			}
			settings := &memorySettings{values: map[string]string{
				StagedTargetSettingKey:                  string(raw),
				settingArtworkBackend:                   blobstore.BackendLocal,
				settingArtworkLocalPath:                 "/srv/silo",
				settingPrivateBucket:                    "private-old",
				blobstore.OperationalIdentitySettingKey: oldPrivate.Identity(),
			}}
			service := New(nil, settings, nil, assets, oldPrivate)
			service.openPublic = func(map[string]string) (blobstore.Store, error) { return assets, nil }
			service.openPrivate = func(map[string]string) blobstore.Store { return newPrivate }
			if assetWrittenBeforeCommit {
				// The target probe runs after the transition is staged but before
				// its atomic commit. Model the first asset write in that window.
				newPrivate.probe = func(ctx context.Context) error {
					if err := assets.Put(ctx, "tmdb/movie/new.webp", []byte("artwork")); err != nil {
						return err
					}
					return settings.Set(ctx, blobstore.IdentitySettingKey, assets.Identity())
				}
			}

			if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{TransitionID: stage.ID, Policy: stage.Policy}, func(adminjob.StorageTransitionProgress) {}); err != nil {
				t.Fatal(err)
			}
			if got := settings.values[blobstore.OperationalIdentitySettingKey]; got != newPrivate.Identity() {
				t.Fatalf("private identity = %q, want %q", got, newPrivate.Identity())
			}
			wantAssetIdentity := ""
			if assetWrittenBeforeCommit {
				wantAssetIdentity = assets.Identity()
			}
			if got, present := settings.values[blobstore.IdentitySettingKey]; got != wantAssetIdentity || present != assetWrittenBeforeCommit {
				t.Fatalf("artwork identity after commit = %q (present %t), want %q (present %t)", got, present, wantAssetIdentity, assetWrittenBeforeCommit)
			}
			var committed stagedTarget
			if err := json.Unmarshal([]byte(settings.values[StagedTargetSettingKey]), &committed); err != nil {
				t.Fatal(err)
			}
			if committed.TargetIdentity != assets.Identity() {
				t.Fatalf("staged target identity = %q, want %q", committed.TargetIdentity, assets.Identity())
			}
			restarted := New(nil, settings, nil, assets, newPrivate)
			if err := restarted.FinalizeCommitted(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got, present := settings.values[blobstore.IdentitySettingKey]; got != wantAssetIdentity || present != assetWrittenBeforeCommit {
				t.Fatalf("artwork identity after restart = %q (present %t), want %q (present %t)", got, present, wantAssetIdentity, assetWrittenBeforeCommit)
			}
			if settings.values[StagedTargetSettingKey] != "" {
				t.Fatal("restart finalization retained the staged transition")
			}
		})
	}
}

func TestLocalRemovingPrivateBucketBringsDataIntoRoot(t *testing.T) {
	for _, test := range []struct {
		policy string
		want   []string
	}{
		{PolicyPreserveUploads, []string{"profile-avatars/1/avatar.webp"}},
		{PolicyMigrateAll, []string{"profile-avatars/1/avatar.webp", "diagnostics/1/report.tar.gz"}},
	} {
		t.Run(test.policy, func(t *testing.T) {
			source := &memoryStore{identity: "local|/srv/silo", objects: map[string][]byte{}}
			private := &memoryStore{identity: "s3|https://s3|private|", objects: map[string][]byte{
				"profile-avatars/1/avatar.webp": []byte("avatar"),
				"diagnostics/1/report.tar.gz":   []byte("bundle"),
			}}
			settings := stagedValues(t, map[string]string{"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo"})
			settings.values[blobstore.OperationalIdentitySettingKey] = private.Identity()
			service := New(nil, settings, nil, source, private)
			service.openPublic = func(map[string]string) (blobstore.Store, error) { return source, nil }

			if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: test.policy}, func(adminjob.StorageTransitionProgress) {}); err != nil {
				t.Fatal(err)
			}
			assertObjects(t, "local root", source, test.want, nil)
			if got := settings.values[blobstore.IdentitySettingKey]; got != source.Identity() {
				t.Fatalf("recorded local identity after copy = %q, want %q", got, source.Identity())
			}
			if got, ok := settings.values[blobstore.OperationalIdentitySettingKey]; !ok || got != "" {
				t.Fatalf("recorded private identity after removal = %q (set %v), want it cleared", got, ok)
			}
			if _, _, err := blobstore.Open(t.Context(), blobstore.Options{Backend: blobstore.BackendLocal, LocalPath: t.TempDir(), Settings: settings}); err == nil {
				t.Fatal("Open accepted another local root after the transition populated the original root")
			}
		})
	}
}

func TestLocalRemovingEmptyPrivateBucketLeavesRootEditable(t *testing.T) {
	source := &memoryStore{identity: "local|/srv/silo", objects: map[string][]byte{}}
	private := &memoryStore{identity: "s3|https://s3|private|", objects: map[string][]byte{}}
	settings := stagedValues(t, map[string]string{"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo"})
	settings.values[blobstore.OperationalIdentitySettingKey] = private.Identity()
	service := New(nil, settings, nil, source, private)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return source, nil }

	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	if got := settings.values[blobstore.IdentitySettingKey]; got != "" {
		t.Fatalf("empty local root gained an identity: %q", got)
	}
	if _, _, err := blobstore.Open(t.Context(), blobstore.Options{Backend: blobstore.BackendLocal, LocalPath: t.TempDir(), Settings: settings}); err != nil {
		t.Fatalf("Open rejected another local root after an empty transition: %v", err)
	}
}

// The shared local root is both the assets and the operational store. Its
// fence takes the whole semaphore, so fencing it twice would hang forever.
func TestSharedLocalRootIsFencedOnce(t *testing.T) {
	fs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := blobstore.WithMutationFence(fs)
	if err := source.Put(t.Context(), "diagnostics/1/report.tar.gz", []byte("bundle")); err != nil {
		t.Fatal(err)
	}
	target := &memoryStore{identity: "s3|https://s3|public|", objects: map[string][]byte{}}
	targetPrivate := &memoryStore{identity: "s3|https://s3|private|", objects: map[string][]byte{}}
	service := New(nil, stagedValues(t, map[string]string{"artwork.storage_backend": "s3", "s3.public_bucket": "public", "s3.private_bucket": "private"}), nil, source, nil)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	service.openPrivate = func(map[string]string) blobstore.Store { return targetPrivate }

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := service.ExecuteStorageTransition(ctx, adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatalf("transition over a shared local root: %v", err)
	}
	if _, ok := targetPrivate.objects["diagnostics/1/report.tar.gz"]; !ok {
		t.Fatal("diagnostic bundle was not copied to private storage")
	}
}

func TestCommitLeavesDiagnosticsEnabledOnLocalTarget(t *testing.T) {
	source := &memoryStore{identity: "s3|https://s3|public|", objects: map[string][]byte{}}
	settings := stagedLocal(t, t.TempDir())
	if _, err := testService(settings, source).ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyFresh}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	if value, ok := settings.values["diagnostics.uploads_enabled"]; ok {
		t.Fatalf("commit wrote diagnostics.uploads_enabled=%q; local storage keeps diagnostics", value)
	}
}

func TestOperationalBucketNamesLocalRoot(t *testing.T) {
	for _, tt := range []struct {
		identity, private, want string
	}{
		{"local|/srv/silo", "", blobstore.LocalBucket},
		{"local|/srv/silo", "private", "private"},
		{"s3|https://s3|public|", "private", "private"},
		{"s3|https://s3|public|", "", ""},
	} {
		if got := operationalBucket(tt.identity, tt.private); got != tt.want {
			t.Errorf("operationalBucket(%q, %q) = %q, want %q", tt.identity, tt.private, got, tt.want)
		}
	}
}

func TestStartKeepsPrivateBucketOnlyForLocalSource(t *testing.T) {
	for _, tt := range []struct {
		name        string
		current     map[string]string
		wantPrivate string
	}{
		{
			name:        "disabling S3 clears private storage",
			current:     map[string]string{"artwork.storage_backend": "s3", "s3.public_bucket": "public", "s3.private_bucket": "private"},
			wantPrivate: "",
		},
		{
			name:        "local install keeps its private bucket",
			current:     map[string]string{"artwork.storage_backend": "local", "artwork.local_path": "/srv/old", "s3.private_endpoint": "https://s3", "s3.private_bucket": "private"},
			wantPrivate: "private",
		},
		{
			name:        "local install keeps a legacy operational bucket",
			current:     map[string]string{"artwork.storage_backend": "local", "artwork.local_path": "/srv/old", "s3.operational_endpoint": "https://s3", "s3.operational_bucket": "legacy"},
			wantPrivate: "legacy",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			settings := &memorySettings{values: tt.current}
			source := &memoryStore{identity: "local|/srv/old", objects: map[string][]byte{}}
			service := New(nil, settings, memoryJobs{}, source, nil)
			if _, _, err := service.Start(t.Context(), 1, StartRequest{Policy: PolicyFresh, Values: map[string]string{
				"artwork.storage_backend": "local", "artwork.local_path": "/srv/new",
			}}); err != nil {
				t.Fatal(err)
			}
			var staged stagedTarget
			if err := json.Unmarshal([]byte(settings.values[StagedTargetSettingKey]), &staged); err != nil {
				t.Fatal(err)
			}
			if got := staged.Values[settingPrivateBucket]; got != tt.wantPrivate {
				t.Fatalf("staged private bucket = %q, want %q", got, tt.wantPrivate)
			}
		})
	}
}

func TestPreflightDescribesEachMove(t *testing.T) {
	const (
		localRoot = "local|/srv/silo"
		publicS3  = "s3|https://s3|public|"
		otherS3   = "s3|https://s3|other|"
		privateS3 = "s3|https://s3|private|"
	)
	local := locationOf(localRoot, "")
	localPrivate := locationOf(localRoot, privateS3)
	s3 := locationOf(publicS3, privateS3)
	s3Other := locationOf(otherS3, privateS3)
	s3NoPrivate := locationOf(publicS3, "")
	for _, tt := range []struct {
		name            string
		current, target storageLocation
		policy          string
		wantUploads     string
		wantSubtitles   string
		wantOperational string
	}{
		{"s3 to local migrate", s3, local, PolicyMigrateAll, "Copied to the new artwork store", "copied", "are copied to local disk"},
		{"local to s3 migrate", local, s3, PolicyMigrateAll, "Copied to the new artwork store", "copied", "are copied to the private bucket"},
		{"local to s3 preserve", local, s3, PolicyPreserveUploads, "profile avatars, and downloaded subtitles are copied", "copied", "remain on local disk"},
		{"artwork-only preserve", s3, s3Other, PolicyPreserveUploads, "profile avatars stay in their current storage", "copied", "stay in their current storage"},
		{"no operational source", s3NoPrivate, local, PolicyPreserveUploads, "and downloaded subtitles are copied.", "copied", "stay in their current storage"},
		{"s3 fresh", s3, s3Other, PolicyFresh, "cleared", "not copied", "stay in their current storage"},
		{"local adds private bucket", local, localPrivate, PolicyMigrateAll, "Profile avatars are copied", "stay in their current storage", "are copied to the private bucket"},
		{"local drops private bucket fresh", localPrivate, local, PolicyFresh, "Profile avatars are not copied", "stay in their current storage", "remain on the private bucket"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			preflight := describe(tt.current, tt.target, tt.policy)
			for field, pair := range map[string][2]string{
				"Uploads":      {preflight.Uploads, tt.wantUploads},
				"Subtitles":    {preflight.Subtitles, tt.wantSubtitles},
				"Diagnostics":  {preflight.Diagnostics, tt.wantOperational},
				"CatalogSeeds": {preflight.CatalogSeeds, tt.wantOperational},
			} {
				if !strings.Contains(pair[0], pair[1]) {
					t.Errorf("%s = %q, want %q", field, pair[0], pair[1])
				}
			}
		})
	}
}

func TestPreflightWarnsWhenSidecarArtworkIsNotCopied(t *testing.T) {
	const (
		oldAssets  = "s3|https://s3|old-public|"
		newAssets  = "s3|https://s3|new-public|"
		oldPrivate = "s3|https://s3|old-private|"
		newPrivate = "s3|https://s3|new-private|"
	)
	current := locationOf(oldAssets, oldPrivate)
	for _, tt := range []struct {
		name        string
		target      storageLocation
		policy      string
		wantWarning bool
	}{
		{"start fresh assets move", locationOf(newAssets, oldPrivate), PolicyFresh, true},
		{"preserve uploads assets move", locationOf(newAssets, oldPrivate), PolicyPreserveUploads, true},
		{"migrate all assets move", locationOf(newAssets, oldPrivate), PolicyMigrateAll, false},
		{"private-only move", locationOf(oldAssets, newPrivate), PolicyFresh, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			warnings := strings.Join(describe(current, tt.target, tt.policy).Warnings, " ")
			hasWarning := strings.Contains(warnings, "NFO/sidecar artwork")
			if hasWarning != tt.wantWarning {
				t.Fatalf("sidecar warning = %t, want %t; warnings: %s", hasWarning, tt.wantWarning, warnings)
			}
			if hasWarning && (!strings.Contains(warnings, "refresh metadata") || !strings.Contains(warnings, "Backfill Metadata Images does not restore")) {
				t.Errorf("sidecar warning omits recovery guidance: %s", warnings)
			}
		})
	}
}

func startStaged(t *testing.T, service *Service, settings *memorySettings, policy string, values map[string]string) (stagedTarget, error) {
	t.Helper()
	if _, _, err := service.Start(t.Context(), 1, StartRequest{Policy: policy, Values: values}); err != nil {
		return stagedTarget{}, err
	}
	var staged stagedTarget
	if err := json.Unmarshal([]byte(settings.values[StagedTargetSettingKey]), &staged); err != nil {
		t.Fatal(err)
	}
	return staged, nil
}

// A credential rotated or a read endpoint changed while a transition copies
// must survive its commit; only the location the copy verified is written.
func TestCommitKeepsSettingsSavedDuringTransition(t *testing.T) {
	settings := &memorySettings{values: map[string]string{
		"artwork.storage_backend": "s3", "s3.public_endpoint": "https://s3", "s3.public_bucket": "public",
		"s3.public_read_endpoint": "https://cdn-old", "s3.private_endpoint": "https://s3", "s3.private_bucket": "private",
		"s3.private_access_key": "OLD", "s3.private_secret_key": "OLD-SECRET",
	}}
	source := &memoryStore{identity: "s3|https://s3|public|", objects: map[string][]byte{}}
	private := &memoryStore{identity: "s3|https://s3|private|", objects: map[string][]byte{}}
	target := &memoryStore{identity: "s3|https://s3|public2|", objects: map[string][]byte{}}
	service := New(nil, settings, memoryJobs{}, source, private)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	service.openPrivate = func(map[string]string) blobstore.Store { return private }
	if _, err := startStaged(t, service, settings, PolicyFresh, map[string]string{"s3.public_bucket": "public2"}); err != nil {
		t.Fatal(err)
	}
	settings.values["s3.private_access_key"] = "NEW"
	settings.values["s3.public_read_endpoint"] = "https://cdn-new"

	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyFresh}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"s3.public_bucket":        "public2",
		"s3.private_access_key":   "NEW",
		"s3.private_secret_key":   "OLD-SECRET",
		"s3.public_read_endpoint": "https://cdn-new",
	} {
		if got := settings.values[key]; got != want {
			t.Errorf("%s = %q after commit, want %q", key, got, want)
		}
	}
}

func TestStartRejectsValuesTheSettingsAPIWould(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"unknown backend": {"artwork.storage_backend": "minio", "s3.public_endpoint": "https://s3", "s3.public_bucket": "b"},
		"relative path":   {"artwork.storage_backend": "local", "artwork.local_path": "artwork"},
		"endpoint scheme": {"s3.public_endpoint": "s3.example", "s3.public_bucket": "b"},
	} {
		t.Run(name, func(t *testing.T) {
			settings := &memorySettings{values: map[string]string{"artwork.storage_backend": "local", "artwork.local_path": "/srv/old"}}
			service := New(nil, settings, memoryJobs{}, &memoryStore{identity: "local|/srv/old", objects: map[string][]byte{}}, nil)
			_, err := startStaged(t, service, settings, PolicyFresh, values)
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("Start error = %v, want a validation error", err)
			}
			if settings.values[StagedTargetSettingKey] != "" {
				t.Fatal("rejected request staged a transition")
			}
		})
	}
}

func TestStartIgnoresUnrelatedStoredSettings(t *testing.T) {
	for name, unrelated := range map[string]map[string]string{
		"bootstrap Redis transport":  {"ratelimit.backend": "redis"},
		"legacy watch provider pair": {"watchsync.trakt.client_id": "legacy-client"},
	} {
		t.Run(name, func(t *testing.T) {
			settings := &memorySettings{values: map[string]string{
				"artwork.storage_backend": "local", "artwork.local_path": "/srv/old",
			}}
			for key, value := range unrelated {
				settings.values[key] = value
			}
			service := New(nil, settings, memoryJobs{}, &memoryStore{identity: "local|/srv/old", objects: map[string][]byte{}}, nil)
			staged, err := startStaged(t, service, settings, PolicyFresh, map[string]string{"artwork.local_path": "/srv/new"})
			if err != nil {
				t.Fatalf("Start rejected a valid storage target: %v", err)
			}
			if got := staged.Values["artwork.local_path"]; got != "/srv/new" {
				t.Fatalf("staged artwork path = %q, want /srv/new", got)
			}
		})
	}
}

func TestStartStillRejectsInvalidStoragePairs(t *testing.T) {
	for name, test := range map[string]struct {
		values  map[string]string
		message string
	}{
		"endpoint without bucket":          {map[string]string{"s3.public_endpoint": "https://s3"}, "public endpoint and bucket"},
		"access key without secret":        {map[string]string{"s3.public_access_key": "key"}, "public access key and secret key"},
		"public URL without read endpoint": {map[string]string{"s3.public_url_auth": "public"}, "s3.public_read_endpoint is required"},
	} {
		t.Run(name, func(t *testing.T) {
			settings := &memorySettings{values: map[string]string{
				"artwork.storage_backend": "local", "artwork.local_path": "/srv/old",
				"ratelimit.backend": "redis",
			}}
			service := New(nil, settings, memoryJobs{}, &memoryStore{identity: "local|/srv/old", objects: map[string][]byte{}}, nil)
			_, err := startStaged(t, service, settings, PolicyFresh, test.values)
			var validation *ValidationError
			if !errors.As(err, &validation) || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Start error = %v, want a storage validation error", err)
			}
			if settings.values[StagedTargetSettingKey] != "" {
				t.Fatal("rejected storage target was staged")
			}
		})
	}
}

func TestStartRejectsTargetEqualToActiveStorage(t *testing.T) {
	current := map[string]string{
		"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo",
		"s3.private_endpoint": "https://S3.example", "s3.private_bucket": "private", "s3.private_key_prefix": "ops",
	}
	for name, values := range map[string]map[string]string{
		"retyped bucket":        {"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo", "s3.private_bucket": "private "},
		"trailing prefix slash": {"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo", "s3.private_key_prefix": "ops/"},
		"endpoint host case":    {"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo", "s3.private_endpoint": "https://s3.example"},
	} {
		t.Run(name, func(t *testing.T) {
			settings := &memorySettings{values: clone(current)}
			source := &memoryStore{identity: "local|/srv/silo", objects: map[string][]byte{}}
			service := New(nil, settings, memoryJobs{}, source, openPrivateTarget(current))
			_, err := startStaged(t, service, settings, PolicyMigrateAll, values)
			if err == nil || !strings.Contains(err.Error(), "same as active storage") {
				t.Fatalf("Start error = %v, want the unchanged target rejected", err)
			}
		})
	}
}

// Removing a bucket removes the private location; the endpoint and prefix it
// leaves behind would otherwise fail validation or linger for the next bucket.
func TestStartClearsPrivateLocationWithItsBucket(t *testing.T) {
	current := map[string]string{
		"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo",
		"s3.private_endpoint": "https://s3", "s3.private_bucket": "private", "s3.private_key_prefix": "ops",
		"s3.private_access_key": "key",
	}
	settings := &memorySettings{values: clone(current)}
	source := &memoryStore{identity: "local|/srv/silo", objects: map[string][]byte{}}
	service := New(nil, settings, memoryJobs{}, source, openPrivateTarget(current))
	staged, err := startStaged(t, service, settings, PolicyFresh, map[string]string{
		"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo", "s3.private_bucket": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"s3.private_endpoint", "s3.private_bucket", "s3.private_key_prefix", "s3.private_access_key"} {
		if staged.Values[key] != "" {
			t.Errorf("staged %s = %q, want it cleared with the bucket", key, staged.Values[key])
		}
	}
}

// A private-only change on an explicit local backend must not touch the public
// S3 settings an administrator saved ahead of a later move.
func TestLocalPrivateOnlyKeepsSavedPublicSettings(t *testing.T) {
	settings := &memorySettings{values: map[string]string{
		"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo",
		"s3.public_endpoint": "https://s3", "s3.public_bucket": "media", "s3.public_access_key": "AKIA", "s3.public_secret_key": "secret",
	}}
	source := &memoryStore{identity: "local|/srv/silo", objects: map[string][]byte{}}
	service := New(nil, settings, memoryJobs{}, source, nil)
	staged, err := startStaged(t, service, settings, PolicyMigrateAll, map[string]string{
		"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo",
		"s3.private_endpoint": "https://s3", "s3.private_bucket": "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"s3.public_endpoint": "https://s3", "s3.public_bucket": "media", "s3.public_access_key": "AKIA", "s3.public_secret_key": "secret",
		"s3.private_bucket": "private",
	} {
		if staged.Values[key] != want {
			t.Errorf("staged %s = %q, want %q", key, staged.Values[key], want)
		}
	}
}

// Removing a local install's private bucket copies from the bucket only; the
// artwork root keeps accepting writes through the final pass and commit.
func TestPrivateOnlyTransitionLeavesArtworkWritable(t *testing.T) {
	fs, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := blobstore.WithMutationFence(fs)
	private := &fencedMemoryStore{memoryStore: &memoryStore{identity: "s3|https://s3|private|", objects: map[string][]byte{
		"profile-avatars/1/avatar.webp": []byte("avatar"),
	}}}
	service := New(nil, stagedValues(t, map[string]string{"artwork.storage_backend": "local", "artwork.local_path": "/srv/silo"}), nil, source, private)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return source, nil }

	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	if !private.fenced {
		t.Fatal("the private bucket being copied was not fenced")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := source.Put(ctx, "tmdb/movie/1/poster/a.webp", []byte("artwork")); err != nil {
		t.Fatalf("artwork write after a private-only transition: %v", err)
	}
}

// Installs upgraded through the settings migration keep s3.operational_* rows
// beside the canonical keys. A request that clears a canonical key must not
// be refilled from its alias.
func TestStartDoesNotRefillClearedKeysFromLegacyAliases(t *testing.T) {
	settings := &memorySettings{values: map[string]string{
		"artwork.storage_backend": "s3", "s3.public_endpoint": "https://s3", "s3.public_bucket": "public",
		"s3.private_endpoint": "https://s3", "s3.private_bucket": "silo", "s3.private_key_prefix": "ops",
		"s3.operational_endpoint": "https://s3", "s3.operational_bucket": "silo", "s3.operational_key_prefix": "ops",
	}}
	source := &memoryStore{identity: "s3|https://s3|public|", objects: map[string][]byte{}}
	service := New(nil, settings, memoryJobs{}, source, openPrivateTarget(map[string]string{
		"s3.private_endpoint": "https://s3", "s3.private_bucket": "silo", "s3.private_key_prefix": "ops",
	}))
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return source, nil }
	staged, err := startStaged(t, service, settings, PolicyFresh, map[string]string{
		"s3.private_bucket": "silo-private", "s3.private_key_prefix": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if staged.Values["s3.private_key_prefix"] != "" || staged.Values["s3.private_bucket"] != "silo-private" {
		t.Fatalf("staged private location = %q/%q, want silo-private with no prefix",
			staged.Values["s3.private_bucket"], staged.Values["s3.private_key_prefix"])
	}
}

// An auto backend whose public bucket exists only as a legacy alias runs on
// S3. A private-only request must stay an S3 transition, not become a move to
// local disk.
func TestStartResolvesAutoBackendThroughLegacyAliases(t *testing.T) {
	settings := &memorySettings{values: map[string]string{
		"artwork.storage_backend": "auto", "s3.operational_endpoint": "https://minio", "s3.operational_bucket": "legacy",
	}}
	source := &memoryStore{identity: "s3|https://minio|legacy|", objects: map[string][]byte{}}
	service := New(nil, settings, memoryJobs{}, source, nil)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return source, nil }
	service.openPrivate = openPrivateTarget
	staged, err := startStaged(t, service, settings, PolicyFresh, map[string]string{
		"s3.private_endpoint": "https://p", "s3.private_bucket": "newpriv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if backend := resolvedBackend(staged.Values); backend != blobstore.BackendS3 {
		t.Fatalf("staged backend = %q, want s3", backend)
	}
	if staged.Values["s3.public_bucket"] != "legacy" || staged.Values["s3.private_bucket"] != "newpriv" {
		t.Fatalf("staged buckets = %q/%q, want legacy/newpriv", staged.Values["s3.public_bucket"], staged.Values["s3.private_bucket"])
	}
}

// barrierStore holds each write until a second write is in flight, so it only
// completes when a page's objects copy concurrently.
type barrierStore struct {
	*memoryStore
	mu       sync.Mutex
	inFlight int
	both     chan struct{}
}

func (s *barrierStore) Put(ctx context.Context, key string, data []byte) error {
	s.mu.Lock()
	s.inFlight++
	if s.inFlight == 2 {
		close(s.both)
	}
	s.mu.Unlock()
	select {
	case <-s.both:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return errors.New("copies ran one at a time")
	}
	return s.memoryStore.Put(ctx, key, data)
}

func TestCopyPassCopiesAPageConcurrently(t *testing.T) {
	source := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{
		"tmdb/01.webp": []byte("one"), "tmdb/02.webp": []byte("two"), "tmdb/03.webp": []byte("three"),
	}}
	target := &barrierStore{memoryStore: &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{}}, both: make(chan struct{})}
	service := New(nil, &memorySettings{values: map[string]string{}}, nil, source, nil)
	copied, _, _, err := service.copyPrefixPass(t.Context(), "concurrent", "public:", source, target, "", func(int, int, string) {}, 0, "run", map[string]objectListing{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if copied != 3 {
		t.Fatalf("copied %d objects, want 3", copied)
	}
}

// Every progress call writes the job row and publishes an event, so a large
// page reports on an interval rather than once per object.
func TestCopyPassThrottlesProgress(t *testing.T) {
	source := &memoryStore{identity: "s3|source|public|", objects: map[string][]byte{}}
	for i := range 20 {
		source.objects[fmt.Sprintf("tmdb/%02d.webp", i)] = []byte(fmt.Sprintf("image-%d", i))
	}
	service := New(nil, &memorySettings{values: map[string]string{}}, nil, source, nil)
	service.progressInterval = time.Hour
	calls := 0
	if _, _, _, err := service.copyPrefixPass(t.Context(), "throttle", "public:", source, &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{}}, "", func(int, int, string) { calls++ }, 0, "run", map[string]objectListing{}, false); err != nil {
		t.Fatal(err)
	}
	if calls > 2 {
		t.Fatalf("progress reported %d times for one page, want the first object and the page total", calls)
	}
}

// A local listing cannot prove an object unchanged, so the fenced pass hashes
// the source again. The target copy this run already verified needs no
// second read.
func TestFencedPassSkipsTargetReadForSameRunCopies(t *testing.T) {
	source := &memoryStore{identity: "local|/srv/silo", objects: map[string][]byte{
		"tmdb/01.webp": []byte("one"), "tmdb/02.webp": []byte("two"),
	}}
	target := &memoryStore{identity: "s3|target|public|", objects: map[string][]byte{}}
	targetPrivate := &memoryStore{identity: "s3|target|private|", objects: map[string][]byte{}}
	service := testService(stagedValues(t, map[string]string{"artwork.storage_backend": "s3", "s3.public_bucket": "public", "s3.private_bucket": "private"}), source)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	service.openPrivate = func(map[string]string) blobstore.Store { return targetPrivate }
	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatal(err)
	}
	// One destination probe and one verification read per copied object,
	// none in the fenced pass.
	if target.gets != 3 {
		t.Fatalf("target reads = %d, want 3", target.gets)
	}
	if source.gets != 4 {
		t.Fatalf("source reads = %d, want a copy and a fenced revalidation per object", source.gets)
	}
}
