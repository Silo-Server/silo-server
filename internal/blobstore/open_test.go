package blobstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/s3client"
)

type testSettings struct {
	values map[string]string
	writes int
}

type flakySettings struct {
	testSettings
	fail bool
}

func (s *flakySettings) SetIfAbsent(ctx context.Context, key, value string) (bool, error) {
	if s.fail {
		s.fail = false
		return false, context.Canceled
	}
	return s.testSettings.SetIfAbsent(ctx, key, value)
}

func (s *testSettings) Get(_ context.Context, key string) (string, error) { return s.values[key], nil }
func (s *testSettings) Set(_ context.Context, key, value string) error {
	s.writes++
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}
func (s *testSettings) SetIfAbsent(_ context.Context, key, value string) (bool, error) {
	s.writes++
	if s.values[key] != "" {
		return false, nil
	}
	s.values[key] = value
	return true, nil
}

// newTestS3Client serves a bucket that accepts writes and reports every other
// key as absent. Open only needs the client's identity and a working Put.
func newTestS3Client(t *testing.T, bucket string) *s3client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("ETag", `"etag"`)
		case http.MethodGet, http.MethodHead:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
		default:
			t.Errorf("unexpected S3 request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return s3client.NewClient(s3client.BucketConfig{
		Endpoint: server.URL, Bucket: bucket, PathStyle: true, AccessKey: "test", SecretKey: "test",
	})
}

// A local backend has one root, so every caller shares the recorded store: a
// first write through Operational must record the identity just as one through
// Assets does.
func TestOpenLocalSharesOneRecordedStore(t *testing.T) {
	settings := &testSettings{values: map[string]string{}}
	stores, backend, err := Open(context.Background(), Options{Backend: BackendLocal, LocalPath: t.TempDir(), Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	if backend != BackendLocal {
		t.Fatalf("backend=%q", backend)
	}
	if !stores.Local() || stores.Operational != stores.Assets {
		t.Fatal("local backend did not share one store")
	}
	if err = stores.Operational.Put(context.Background(), "diagnostics/1/report.tar.gz", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if settings.values[IdentitySettingKey] != stores.Assets.Identity() {
		t.Fatalf("operational write did not record identity: %#v", settings.values)
	}
}

// recordingStore must forward every write method. A diagnostic bundle or job
// artifact is often the first thing a local install writes, and those arrive
// through PutStream; if it reached the embedded store directly the identity
// would never be recorded, the configured root would stay editable, and every
// key referencing it would be orphaned by the next change.
func TestOpenRecordsIdentityOnFirstStreamedWrite(t *testing.T) {
	for name, write := range map[string]func(Store) error{
		"put": func(s Store) error {
			return s.Put(context.Background(), "diagnostics/1/report.tar.gz", []byte("x"))
		},
		"put stream": func(s Store) error {
			return s.PutStream(context.Background(), "diagnostics/1/report.tar.gz", strings.NewReader("x"), "application/gzip")
		},
	} {
		t.Run(name, func(t *testing.T) {
			settings := &testSettings{values: map[string]string{}}
			stores, _, err := Open(context.Background(), Options{Backend: BackendLocal, LocalPath: t.TempDir(), Settings: settings})
			if err != nil {
				t.Fatal(err)
			}
			if err := write(stores.Operational); err != nil {
				t.Fatal(err)
			}
			if settings.values[IdentitySettingKey] != stores.Assets.Identity() {
				t.Fatalf("identity not recorded: %#v", settings.values)
			}
		})
	}
}

// The private bucket is a different location from the catalog's assets. Its
// identity must never be recorded, or the next start would refuse the real
// assets store as a mismatch.
func TestOpenS3KeepsBucketsSeparateAndLeavesPrivateUnrecorded(t *testing.T) {
	settings := &testSettings{values: map[string]string{}}
	public := newTestS3Client(t, "assets")
	private := newTestS3Client(t, "operational")
	stores, backend, err := Open(context.Background(), Options{
		Backend: BackendS3, S3: public, S3Private: private, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	if backend != BackendS3 {
		t.Fatalf("backend=%q", backend)
	}
	if stores.Local() {
		t.Fatal("s3 backend reported as local")
	}
	if stores.Operational == nil || stores.Operational == stores.Assets {
		t.Fatal("s3 backend did not open a separate operational store")
	}
	if !strings.Contains(stores.Operational.Identity(), "operational") {
		t.Fatalf("operational identity = %q", stores.Operational.Identity())
	}
	if err = stores.Operational.Put(context.Background(), "diagnostics/1/report.tar.gz", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if settings.values[IdentitySettingKey] != "" {
		t.Fatalf("private bucket write recorded an identity: %#v", settings.values)
	}
}

// Avatars have always lived in the private bucket, whatever backed artwork. An
// install that configured one and then moved artwork to disk must keep reading
// its existing profile-avatars keys, so a configured private bucket owns the
// operational store even on a local backend.
func TestOpenLocalStillPrefersAConfiguredPrivateBucket(t *testing.T) {
	private := newTestS3Client(t, "operational")
	stores, backend, err := Open(context.Background(), Options{
		Backend: BackendLocal, LocalPath: t.TempDir(), S3Private: private,
		Settings: &testSettings{values: map[string]string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if backend != BackendLocal {
		t.Fatalf("backend=%q", backend)
	}
	if stores.Operational == stores.Assets || stores.Local() {
		t.Fatal("local backend overrode the configured private bucket")
	}
	if !strings.Contains(stores.Operational.Identity(), "operational") {
		t.Fatalf("operational identity = %q", stores.Operational.Identity())
	}
}

// An S3 backend without a private bucket leaves Operational nil, which is how
// callers detect that diagnostics and job artifacts have nowhere to go.
func TestOpenS3WithoutPrivateBucketLeavesOperationalNil(t *testing.T) {
	stores, _, err := Open(context.Background(), Options{
		Backend: BackendS3, S3: newTestS3Client(t, "assets"), Settings: &testSettings{values: map[string]string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stores.Assets == nil || stores.Operational != nil {
		t.Fatal("missing private bucket did not leave operational nil")
	}
}

func TestOpenLocalRecordsBackendOnFirstPut(t *testing.T) {
	settings := &testSettings{values: map[string]string{}}
	stores, backend, err := Open(context.Background(), Options{Backend: "auto", LocalPath: filepath.Join(t.TempDir(), "artwork"), Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	store := stores.Assets
	if backend != BackendLocal {
		t.Fatalf("backend=%q", backend)
	}
	if settings.writes != 0 {
		t.Fatal("recorded before write")
	}
	if err = store.Put(context.Background(), "a.webp", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if settings.values[IdentitySettingKey] != store.Identity() || settings.writes != 1 {
		t.Fatalf("settings=%#v writes=%d", settings.values, settings.writes)
	}
}
func TestOpenRejectsRecordedStorageMismatch(t *testing.T) {
	root := t.TempDir()
	current, err := NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, recorded := range map[string]string{
		"other backend": BackendS3 + "|https://s3.example|artwork|",
		"other root":    BackendLocal + "|" + filepath.Join(root, "elsewhere"),
	} {
		settings := &testSettings{values: map[string]string{IdentitySettingKey: recorded}}
		if _, _, err := Open(context.Background(), Options{Backend: BackendLocal, LocalPath: root, Settings: settings}); err == nil {
			t.Fatalf("%s: mismatch accepted", name)
		}
	}
	settings := &testSettings{values: map[string]string{IdentitySettingKey: current.Identity()}}
	if _, _, err := Open(context.Background(), Options{Backend: BackendLocal, LocalPath: root, Settings: settings}); err != nil {
		t.Fatalf("same root rejected: %v", err)
	}
}

func TestOpenRetriesBackendRecordingAfterSettingsFailure(t *testing.T) {
	settings := &flakySettings{testSettings: testSettings{values: map[string]string{}}, fail: true}
	stores, _, err := Open(context.Background(), Options{Backend: BackendLocal, LocalPath: filepath.Join(t.TempDir(), "artwork"), Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	store := stores.Assets
	if err := store.Put(context.Background(), "a.webp", []byte("x")); err == nil {
		t.Fatal("write hid backend recording failure")
	}
	if err := store.Put(context.Background(), "a.webp", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if settings.values[IdentitySettingKey] != store.Identity() {
		t.Fatalf("settings = %#v", settings.values)
	}
}

// A release before the identity row lowercased the whole S3 endpoint. The
// migration carries that fingerprint over verbatim, so a mixed-case endpoint
// path must still open and the row is rewritten in the exact form.
func TestOpenUpgradesLegacyLowercasedS3Identity(t *testing.T) {
	const current = BackendS3 + "|https://gateway.example/TenantA|artwork|silo"
	settings := &testSettings{values: map[string]string{IdentitySettingKey: strings.ToLower(current)}}
	store := &identityStore{Store: &Filesystem{root: "/unused"}, identity: current}
	if _, _, err := openRecorded(context.Background(), store, settings); err != nil {
		t.Fatalf("legacy fingerprint rejected: %v", err)
	}
	if settings.values[IdentitySettingKey] != current {
		t.Fatalf("identity not upgraded: %q", settings.values[IdentitySettingKey])
	}
	// A genuinely different path is still a move.
	other := &identityStore{Store: store.Store, identity: BackendS3 + "|https://gateway.example/TenantB|artwork|silo"}
	if _, _, err := openRecorded(context.Background(), other, settings); err == nil {
		t.Fatal("different tenant accepted")
	}
	// The legacy fingerprint kept key-prefix case, so a case-only prefix
	// change is a real move and must not ride the endpoint upgrade.
	prefixCase := &identityStore{Store: store.Store, identity: BackendS3 + "|https://gateway.example/TenantA|artwork|Silo"}
	prefixSettings := &testSettings{values: map[string]string{IdentitySettingKey: BackendS3 + "|https://gateway.example/tenanta|artwork|silo"}}
	if _, _, err := openRecorded(context.Background(), prefixCase, prefixSettings); err == nil {
		t.Fatal("key-prefix case change accepted")
	}
	// A recorded bucket in mixed case never came from the legacy writer.
	bucketCase := &testSettings{values: map[string]string{IdentitySettingKey: BackendS3 + "|https://gateway.example/tenanta|Artwork|silo"}}
	if _, _, err := openRecorded(context.Background(), store, bucketCase); err == nil {
		t.Fatal("mixed-case recorded bucket accepted")
	}
	// Local identities never had a legacy form; case differences are moves.
	local := &identityStore{Store: store.Store, identity: BackendLocal + "|/srv/Art"}
	localSettings := &testSettings{values: map[string]string{IdentitySettingKey: BackendLocal + "|/srv/art"}}
	if _, _, err := openRecorded(context.Background(), local, localSettings); err == nil {
		t.Fatal("local case difference accepted")
	}
}

type identityStore struct {
	Store
	identity string
}

func (s *identityStore) Identity() string { return s.identity }
