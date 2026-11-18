package storagetransition

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/metadata"
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
	return stagedSettings(t, stagedTarget{Values: values})
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
	// Unencrypted local backups and an upload's parts never move.
	objects["backups/server-1/2026-11-01T040000Z-nightly/db.dump"] = []byte("dump")
	objects["backups/uploads/u1/00001"] = []byte("part")
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
	backups := []string{"backups/server-1/2026-11-01T040000Z-nightly/db.dump", "backups/uploads/u1/00001"}
	assertObjects(t, "public target", target,
		[]string{"tmdb/movie/1/poster/a.webp", "branding/logo.webp", "subtitles/7/en.srt"},
		append([]string{"profile-avatars/1/avatar.webp", "diagnostics/1/report.tar.gz", "catalog-seeds/export/seed.json.gz"}, backups...))
	assertObjects(t, "private target", targetPrivate,
		[]string{"profile-avatars/1/avatar.webp", "diagnostics/1/report.tar.gz", "catalog-seeds/export/seed.json.gz"},
		append([]string{"tmdb/movie/1/poster/a.webp", "branding/logo.webp", "subtitles/7/en.srt"}, backups...))
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
		"profile-avatars/1/avatar.webp":                           []byte("avatar"),
		"diagnostics/1/report.tar.gz":                             []byte("bundle"),
		"catalog-seeds/export/seed.json.gz":                       []byte("seed"),
		"backups/server-1/2026-11-01T040000Z-nightly/db.dump.age": []byte("encrypted dump"),
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
	}, []string{"backups/server-1/2026-11-01T040000Z-nightly/db.dump.age"})
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

// The active store can keep the identity recorded under another path to its
// directory. A private-only transition opens the artwork target afresh, by
// its resolved path; that is still the active location, so the artwork
// identity and objects stay as they are.
func TestPrivateOnlyTransitionKeepsAliasedArtworkIdentity(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "config", "silo")
	if err := os.MkdirAll(filepath.Join(state, "artwork"), 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(base, "var-lib-silo")
	if err := os.Symlink(state, linked); err != nil {
		t.Fatal(err)
	}
	source := &memoryStore{identity: "local|" + filepath.Join(linked, "artwork"), objects: localRootObjects()}
	reopened := &memoryStore{identity: "local|" + filepath.Join(state, "artwork"), objects: map[string][]byte{}}
	targetPrivate := &memoryStore{identity: "s3|https://s3|private|", objects: map[string][]byte{}}
	settings := stagedValues(t, map[string]string{"artwork.storage_backend": "local", "artwork.local_path": filepath.Join(linked, "artwork"), "s3.private_bucket": "private"})
	service := testService(settings, source)
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return reopened, nil }
	service.openPrivate = func(map[string]string) blobstore.Store { return targetPrivate }

	value, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {})
	if err != nil {
		t.Fatal(err)
	}
	if result := value.(Result); result.TargetIdentity != source.Identity() {
		t.Fatalf("target identity = %q, want the recorded %q", result.TargetIdentity, source.Identity())
	}
	if len(reopened.objects) != 0 {
		t.Fatalf("artwork copied onto its own directory: %v", reopened.objects)
	}
	if got := settings.values[blobstore.IdentitySettingKey]; got != "" && got != source.Identity() {
		t.Fatalf("recorded artwork identity = %q, want it left as %q", got, source.Identity())
	}
}

// The active store keeps the identity recorded under a link. If the link is
// later pointed at another directory, a transition to that directory is a
// real move: the artwork is still where the store opened it.
func TestTransitionToRetargetedLinkCopiesArtwork(t *testing.T) {
	base := t.TempDir()
	first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(dir, "artwork"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	linked := filepath.Join(base, "var-lib-silo")
	if err := os.Symlink(first, linked); err != nil {
		t.Fatal(err)
	}
	recorded := &memorySettings{values: map[string]string{blobstore.IdentitySettingKey: "local|" + filepath.Join(linked, "artwork")}}
	stores, _, err := blobstore.Open(t.Context(), blobstore.Options{Backend: blobstore.BackendLocal, LocalPath: filepath.Join(linked, "artwork"), Settings: recorded})
	if err != nil {
		t.Fatal(err)
	}
	if err := stores.Assets.Put(t.Context(), "tmdb/movie/1/poster/a.webp", []byte("artwork")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(linked); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, linked); err != nil {
		t.Fatal(err)
	}
	target, err := blobstore.NewFilesystem(filepath.Join(second, "artwork"))
	if err != nil {
		t.Fatal(err)
	}
	settings := stagedValues(t, map[string]string{"artwork.storage_backend": "local", "artwork.local_path": filepath.Join(second, "artwork")})
	service := New(nil, settings, nil, stores.Assets, nil)
	service.reconcile = func(context.Context, blobstore.Store, func(float64, string)) (metadata.ArtworkReconcileStats, error) {
		return metadata.ArtworkReconcileStats{}, nil
	}
	service.openPublic = func(map[string]string) (blobstore.Store, error) { return target, nil }
	service.openPrivate = func(map[string]string) blobstore.Store { return nil }

	if _, err := service.ExecuteStorageTransition(t.Context(), adminjob.StorageTransitionRequest{Policy: PolicyMigrateAll}, func(adminjob.StorageTransitionProgress) {}); err != nil {
		t.Fatalf("transition to the directory the link now names failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(second, "artwork", "tmdb", "movie", "1", "poster", "a.webp")); err != nil {
		t.Fatalf("artwork not copied to the new directory: %v", err)
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

// The active store can carry the identity recorded under another path to the
// same directory. Naming that directory by its current path is still the
// active location, not a move onto itself.
func TestStartRejectsActiveDirectoryReachedByAnotherPath(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "config", "silo")
	if err := os.MkdirAll(filepath.Join(state, "artwork"), 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(base, "var-lib-silo")
	if err := os.Symlink(state, linked); err != nil {
		t.Fatal(err)
	}
	current := map[string]string{"artwork.storage_backend": "local", "artwork.local_path": filepath.Join(linked, "artwork")}
	settings := &memorySettings{values: clone(current)}
	source := &memoryStore{identity: "local|" + filepath.Join(linked, "artwork"), objects: map[string][]byte{}}
	service := New(nil, settings, memoryJobs{}, source, nil)
	_, err := startStaged(t, service, settings, PolicyMigrateAll, map[string]string{"artwork.storage_backend": "local", "artwork.local_path": filepath.Join(state, "artwork")})
	if err == nil || !strings.Contains(err.Error(), "same as active storage") {
		t.Fatalf("Start error = %v, want the same directory rejected as unchanged", err)
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
