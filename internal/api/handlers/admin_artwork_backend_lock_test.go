package handlers

import (
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// requireArtworkLockAnswer fails unless a settings save answered as wanted:
// accepted, or refused as a locked storage location.
func requireArtworkLockAnswer(t *testing.T, name string, err error, locked bool) {
	t.Helper()
	if !locked {
		if err != nil {
			t.Fatalf("%s: %v, want accepted", name, err)
		}
		return
	}
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.Status != 409 || apiErr.Code != "artwork_storage_locked" {
		t.Fatalf("%s: %v, want artwork_storage_locked", name, err)
	}
}

func TestArtworkBackendLocksOnceStorageIsRecorded(t *testing.T) {
	batch := func(store *fakeServerSettingsStore, values map[string]string) error {
		_, err := (&AdminHandler{SettingsRepo: store}).UpdateAdminSettings(t.Context(), values, nil)
		return err
	}
	single := func(store *fakeServerSettingsStore, key, value string) error {
		_, err := (&AdminHandler{SettingsRepo: store}).UpdateAdminSetting(t.Context(), key, value, nil)
		return err
	}

	// Before any artwork is stored the backend is free to change.
	open := &fakeServerSettingsStore{values: map[string]string{"s3.public_endpoint": "https://s3.example", "s3.public_bucket": "artwork"}}
	requireArtworkLockAnswer(t, "unlocked batch", batch(open, map[string]string{"artwork.storage_backend": "s3"}), false)

	locked := &fakeServerSettingsStore{values: map[string]string{
		"artwork.storage_backend":    "local",
		"s3.public_endpoint":         "https://s3.example",
		"s3.public_bucket":           "artwork",
		blobstore.IdentitySettingKey: "local|/var/lib/silo/artwork",
	}}
	requireArtworkLockAnswer(t, "batch", batch(locked, map[string]string{"artwork.storage_backend": "s3", "metadata.cache_images": "true"}), true)
	requireArtworkLockAnswer(t, "single", single(locked, "artwork.storage_backend", "s3"), true)
	if locked.values["artwork.storage_backend"] != "local" || locked.values["metadata.cache_images"] != "" {
		t.Fatalf("locked write mutated settings: %#v", locked.values)
	}
	// Re-saving the current value is a no-op, not a conflict, so forms that
	// submit every key keep working.
	requireArtworkLockAnswer(t, "same value", batch(locked, map[string]string{"artwork.storage_backend": "local", "metadata.cache_images": "true"}), false)

	// The recorded default is auto when the row was never written.
	defaulted := map[string]string{
		"s3.public_endpoint":         "https://s3.example",
		"s3.public_bucket":           "artwork",
		blobstore.IdentitySettingKey: "s3|https://s3.example|artwork|",
	}
	requireArtworkLockAnswer(t, "auto on an unset locked row", single(&fakeServerSettingsStore{values: cloneSettings(defaulted)}, "artwork.storage_backend", "auto"), false)
	requireArtworkLockAnswer(t, "local on a locked s3 store", single(&fakeServerSettingsStore{values: cloneSettings(defaulted)}, "artwork.storage_backend", "local"), true)
}

// Every setting the recorded identity is built from is locked, not only the
// backend selector: the local root, and for S3 the endpoint, bucket, and key
// prefix. An auto backend that resolved to local also may not gain a bucket,
// because that flips the resolution on restart.
func TestArtworkIdentityFieldsLockOnceStorageIsRecorded(t *testing.T) {
	batch := func(stored map[string]string, values map[string]string) error {
		_, err := (&AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: stored}}).UpdateAdminSettings(t.Context(), values, nil)
		return err
	}
	single := func(stored map[string]string, key, value string) error {
		_, err := (&AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: stored}}).UpdateAdminSetting(t.Context(), key, value, nil)
		return err
	}
	local := func() map[string]string {
		return map[string]string{"artwork.storage_backend": "local", blobstore.IdentitySettingKey: "local|/var/lib/silo/artwork"}
	}
	localPrivate := func() map[string]string {
		stored := local()
		stored["s3.private_endpoint"] = "https://private-s3.example"
		stored["s3.private_bucket"] = "private"
		return stored
	}
	withPrefix := localPrivate()
	withPrefix["s3.private_key_prefix"] = "ops"
	// A private bucket that holds data locks before any artwork is stored,
	// while the assets location stays free to choose.
	privateOnly := func() map[string]string {
		return map[string]string{
			"artwork.storage_backend":               "local",
			"s3.private_endpoint":                   "https://private-s3.example",
			"s3.private_bucket":                     "private",
			blobstore.OperationalIdentitySettingKey: "s3|https://private-s3.example|private|",
		}
	}
	s3 := func() map[string]string {
		return map[string]string{
			"artwork.storage_backend":    "s3",
			"s3.public_endpoint":         "https://s3.example",
			"s3.public_bucket":           "artwork",
			"s3.public_key_prefix":       "cache",
			"s3.private_endpoint":        "https://private-s3.example",
			"s3.private_bucket":          "private",
			"s3.private_key_prefix":      "operations",
			blobstore.IdentitySettingKey: "s3|https://s3.example|artwork|cache",
		}
	}

	for _, tc := range []struct {
		name   string
		err    error
		locked bool
	}{
		{"local path", single(local(), "artwork.local_path", "/srv/artwork"), true},
		{"same local path", single(local(), "artwork.local_path", "/var/lib/silo/artwork"), false},
		// An explicit local backend ignores the public bucket, so adding one
		// is allowed: the artwork location does not move.
		{"bucket under explicit local", batch(local(), map[string]string{"s3.public_endpoint": "https://s3.example", "s3.public_bucket": "media"}), false},
		// A private bucket owns avatars, diagnostics, and job artifacts on a
		// local backend too, so adding, changing, or removing one is a
		// managed transition.
		{"private bucket under local", batch(local(), map[string]string{"s3.private_endpoint": "https://private-s3.example", "s3.private_bucket": "private"}), true},
		{"changed private bucket under local", single(localPrivate(), "s3.private_bucket", "other-private"), true},
		{"legacy operational bucket under local", single(local(), "s3.operational_bucket", "legacy"), true},
		{"private credentials under local", single(localPrivate(), "s3.private_region", "eu-central-1"), false},
		// A leftover prefix with no bucket names no location, and a prefix
		// that normalizes to the same value names the same one.
		{"prefix without a bucket", single(local(), "s3.private_key_prefix", "ops"), false},
		{"equivalent prefix", single(withPrefix, "s3.private_key_prefix", "ops/"), false},
		{"changed prefix", single(localPrivate(), "s3.private_key_prefix", "other"), true},
		// Host and bucket case do not matter to S3.
		{"private endpoint host case", single(localPrivate(), "s3.private_endpoint", "https://PRIVATE-S3.example"), false},
		{"recorded private bucket", single(privateOnly(), "s3.private_bucket", "other-private"), true},
		{"unrecorded assets location", single(privateOnly(), "artwork.local_path", "/srv/artwork"), false},
		{"s3.public_endpoint", single(s3(), "s3.public_endpoint", "https://other.example"), true},
		{"s3.public_bucket", single(s3(), "s3.public_bucket", "other"), true},
		{"s3.public_key_prefix", single(s3(), "s3.public_key_prefix", "elsewhere"), true},
		{"s3.private_endpoint", single(s3(), "s3.private_endpoint", "https://other-private.example"), true},
		{"s3.private_bucket", single(s3(), "s3.private_bucket", "other-private"), true},
		{"s3.private_key_prefix", single(s3(), "s3.private_key_prefix", "other-operations"), true},
		// The identity does not include credentials or the read endpoint.
		{"read endpoint", single(s3(), "s3.public_read_endpoint", "https://cdn.example"), false},
		{"public endpoint host case", single(s3(), "s3.public_endpoint", "https://S3.example"), false},
		{"public bucket case", single(s3(), "s3.public_bucket", "Artwork"), false},
		{"same bucket", batch(s3(), map[string]string{"s3.public_endpoint": "https://s3.example", "s3.public_bucket": "artwork", "s3.public_key_prefix": "cache"}), false},
	} {
		requireArtworkLockAnswer(t, tc.name, tc.err, tc.locked)
	}

	autoLocal := &fakeServerSettingsStore{values: map[string]string{blobstore.IdentitySettingKey: "local|/var/lib/silo/artwork"}}
	_, err := (&AdminHandler{SettingsRepo: autoLocal}).UpdateAdminSettings(t.Context(), map[string]string{"s3.public_endpoint": "https://s3.example", "s3.public_bucket": "media"}, nil)
	requireArtworkLockAnswer(t, "bucket under auto-local", err, true)
	if autoLocal.values["s3.public_bucket"] != "" {
		t.Fatalf("rejected write persisted: %#v", autoLocal.values)
	}
	// Clearing the bucket would resolve the s3 backend to nothing.
	if err := single(s3(), "s3.public_bucket", ""); err == nil {
		t.Fatal("clearing the recorded bucket was accepted")
	}
}

// A location saved without a restart can differ from the one the running
// process then records (#4418). Writes that leave the location settings as they
// are must still save, and a write that puts them back on the recorded location
// must be accepted; anything else stays locked.
func TestArtworkLockWhenSavedLocationDiffersFromRecorded(t *testing.T) {
	pendingS3 := func() map[string]string {
		return map[string]string{
			"artwork.local_path":         "/var/lib/silo/artwork",
			"s3.public_endpoint":         "https://s3.example",
			"s3.public_bucket":           "artwork",
			"s3.public_key_prefix":       "silo",
			blobstore.IdentitySettingKey: "local|/var/lib/silo/artwork",
		}
	}
	pendingPrivate := func() map[string]string {
		stored := pendingS3()
		stored["artwork.storage_backend"] = "local"
		stored["s3.private_endpoint"] = "https://private-s3.example"
		stored["s3.private_bucket"] = "private"
		return stored
	}
	movedS3 := func() map[string]string {
		return map[string]string{
			"artwork.storage_backend":    "local",
			"artwork.local_path":         "/var/lib/silo/artwork",
			"s3.public_endpoint":         "https://s3.example",
			"s3.public_bucket":           "artwork",
			"s3.public_key_prefix":       "cache",
			blobstore.IdentitySettingKey: "s3|https://s3.example|artwork|cache",
		}
	}
	// Saved settings match the recorded rows; the bucket or private bucket
	// named by prefix is "alpha" under the key prefix "beta|cache".
	collidingS3 := func(prefix, identityKey string) map[string]string {
		stored := map[string]string{
			"artwork.storage_backend":    "s3",
			"s3.public_endpoint":         "https://s3.example",
			"s3.public_bucket":           "artwork",
			blobstore.IdentitySettingKey: "s3|https://s3.example|artwork|",
		}
		stored[prefix+"_endpoint"] = "https://s3.example"
		stored[prefix+"_bucket"] = "alpha"
		stored[prefix+"_key_prefix"] = "beta|cache"
		stored[identityKey] = "s3|https://s3.example|alpha|beta|cache"
		return stored
	}
	tenantS3 := map[string]string{
		"s3.public_endpoint":         "https://s3.example/tenant",
		"s3.public_bucket":           "artwork",
		blobstore.IdentitySettingKey: "s3|https://s3.example/tenant|artwork|",
	}
	for _, tc := range []struct {
		name   string
		stored map[string]string
		values map[string]string
		locked bool
	}{
		{"unrelated setting", pendingS3(), map[string]string{"branding.server_name": "Test"}, false},
		{"credentials of the pending bucket", pendingS3(), map[string]string{"s3.public_region": "garage"}, false},
		{"restore local backend", pendingS3(), map[string]string{"artwork.storage_backend": "local"}, false},
		{"clear the pending bucket", pendingS3(), map[string]string{"s3.public_endpoint": "", "s3.public_bucket": ""}, false},
		{"another bucket", pendingS3(), map[string]string{"s3.public_bucket": "other"}, true},
		{"another local root", pendingS3(), map[string]string{"artwork.storage_backend": "local", "artwork.local_path": "/srv/artwork"}, true},
		{"remove the pending private bucket", pendingPrivate(), map[string]string{"s3.private_endpoint": "", "s3.private_bucket": ""}, false},
		{"another private bucket", pendingPrivate(), map[string]string{"s3.private_bucket": "other-private"}, true},
		{"restore recorded s3 location", movedS3(), map[string]string{"artwork.storage_backend": "s3"}, false},
		{"restore recorded s3 with equivalent spelling", movedS3(), map[string]string{"artwork.storage_backend": "s3", "s3.public_endpoint": "https://S3.example", "s3.public_key_prefix": "/cache/"}, false},
		// An endpoint path addresses case-sensitive storage, so its case is a
		// different location even when the recorded path is lowercase.
		{"another endpoint path case", tenantS3, map[string]string{"s3.public_endpoint": "https://s3.example/Tenant"}, true},
		// The identity joins its parts with "|", so a bucket carrying one can
		// spell the recorded identity while naming another bucket.
		{"bucket that collides with the recorded identity", collidingS3("s3.public", blobstore.IdentitySettingKey), map[string]string{"s3.public_bucket": "alpha|beta", "s3.public_key_prefix": "cache"}, true},
		{"private bucket that collides with the recorded identity", collidingS3("s3.private", blobstore.OperationalIdentitySettingKey), map[string]string{"s3.private_bucket": "alpha|beta", "s3.private_key_prefix": "cache"}, true},
	} {
		_, err := (&AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: tc.stored}}).UpdateAdminSettings(t.Context(), tc.values, nil)
		requireArtworkLockAnswer(t, tc.name, err, tc.locked)
	}
	single := &fakeServerSettingsStore{values: pendingS3()}
	_, err := (&AdminHandler{SettingsRepo: single}).UpdateAdminSetting(t.Context(), "branding.server_name", "Test", nil)
	requireArtworkLockAnswer(t, "single unrelated setting", err, false)
}

func TestArtworkS3BackendRequiresPublicBucket(t *testing.T) {
	for _, single := range []bool{false, true} {
		empty := &fakeServerSettingsStore{values: map[string]string{}}
		err := saveAdminSetting(t, &AdminHandler{SettingsRepo: empty}, "artwork.storage_backend", "s3", single)
		requireAdminError(t, err, 400, "")
		if single && !strings.Contains(err.Error(), "s3.public_bucket") {
			t.Fatalf("refusal = %v, want it to name s3.public_bucket", err)
		}
		if empty.values["artwork.storage_backend"] != "" {
			t.Fatalf("single=%v: invalid write persisted: %#v", single, empty.values)
		}
	}
	withBucket := &fakeServerSettingsStore{values: map[string]string{"s3.public_endpoint": "https://s3.example", "s3.public_bucket": "artwork"}}
	if err := saveAdminSetting(t, &AdminHandler{SettingsRepo: withBucket}, "artwork.storage_backend", "s3", true); err != nil {
		t.Fatalf("s3 backend with a bucket: %v", err)
	}
	requireAdminError(t, saveAdminSetting(t, &AdminHandler{SettingsRepo: withBucket}, "s3.public_bucket", "", true), 400, "")
}

// Artwork caching works on local storage, so enabling it needs no bucket.
func TestCacheImagesEnableDoesNotRequireS3(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	if err := saveAdminSetting(t, &AdminHandler{SettingsRepo: settings}, "metadata.cache_images", "true", false); err != nil {
		t.Fatal(err)
	}
}
