package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/s3client"
	"github.com/Silo-Server/silo-server/internal/s3client/s3test"
)

// mapSettings is a SettingsReader and SettingsWriter over a map.
type mapSettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (m *mapSettings) Get(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.values[key], nil
}

func (m *mapSettings) Set(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = value
	return nil
}

const testBucket = "backups"

func s3TestConfig(srv *s3test.Server) S3Config {
	return S3Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: testBucket, KeyPrefix: "silo", AccessKey: "a", SecretKey: "s", PathStyle: true}
}

// s3Settings points backup.destination at the fake bucket.
func s3Settings(srv *s3test.Server, acknowledged string) *mapSettings {
	c := s3TestConfig(srv)
	return &mapSettings{values: map[string]string{
		config.BackupDestinationSettingKey: DestinationS3,
		config.BackupS3EndpointSettingKey:  c.Endpoint, config.BackupS3RegionSettingKey: c.Region,
		config.BackupS3BucketSettingKey: c.Bucket, config.BackupS3KeyPrefixSettingKey: c.KeyPrefix,
		config.BackupS3AccessKeySettingKey: c.AccessKey, config.BackupS3SecretKeySettingKey: c.SecretKey,
		config.BackupS3PathStyleSettingKey:       "true",
		config.BackupS3KeyAcknowledgedSettingKey: acknowledged,
	}}
}

// putEncryptedBackup stores a complete encrypted backup at the fake bucket,
// as a backup with key would have written it, and returns its manifest.
func putEncryptedBackup(t *testing.T, srv *s3test.Server, key *ArchiveKey, serverID, id string, restoreSet []byte) *Manifest {
	t.Helper()
	m := validTestManifest()
	m.ID, m.ServerID, m.Encrypted, m.KeyCheck = id, serverID, true, key.Check
	_, kind, _ := ParseFolderName(id)
	m.Kind = kind
	m.Objects = nil
	for name, plain := range map[string][]byte{DumpName: []byte("dump"), BlobsName: {}, RestoreSetName: restoreSet} {
		stored := encryptBytes(t, key, plain)
		srv.Put(testBucket, "silo/"+objectKey(serverID, id, name+EncryptedSuffix), stored)
		s := newSum()
		_, _ = s.Write(stored)
		m.Objects = append(m.Objects, s.entry(name+EncryptedSuffix))
	}
	m.RestoreSet = &RestoreSetSummary{FormatVersion: restoreSetVersion, Sections: map[string]int{}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(data); err != nil {
		t.Fatalf("the test manifest is not valid: %v", err)
	}
	srv.Put(testBucket, "silo/"+objectKey(serverID, id, ManifestName), data)
	return &m
}

func TestPartSizeGrowsWithThePreviousBackup(t *testing.T) {
	for _, c := range []struct{ previous, want int64 }{
		{1 << 30, defaultPartSize},
		{1 << 40, 234 << 20},
	} {
		// Twice the previous size must fit in fewer parts than S3 allows.
		if got := partSizeFor(c.previous); got != c.want || got*s3client.MaxUploadParts < 2*c.previous {
			t.Errorf("partSizeFor(%d) = %d, want %d", c.previous, got, c.want)
		}
	}
}

// TestRotationRecordsRefusedDeletes: a bucket that refuses deletes keeps
// every backup whole, and rotation records a warning instead of failing.
func TestRotationRecordsRefusedDeletes(t *testing.T) {
	srv := s3test.New(t, testBucket)
	key := testArchiveKey(t, "server-a")
	settings := s3Settings(srv, key.Check)
	svc := New(Options{Settings: settings, ArchiveKey: key, ServerID: func(context.Context) (string, error) { return "server", nil }})
	start := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC)
	for i := range config.DefaultBackupKeep + 2 {
		putEncryptedBackup(t, srv, key, "server", FolderName(start.Add(time.Duration(i)*24*time.Hour), KindNightly), []byte("set"))
	}
	ctx := t.Context()
	tgt, err := svc.preflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := srv.Keys(testBucket, "")
	srv.RefuseDeletes(true)
	rotated, _, warnings := svc.rotate(ctx, tgt, "server", nil, true)
	if len(rotated) != 0 || len(warnings) != 2 || !strings.Contains(warnings[0], "lifecycle rules") {
		t.Fatalf("rotation against a refusing bucket = rotated %v, warnings %v", rotated, warnings)
	}
	if after := srv.Keys(testBucket, ""); len(after) != len(before) {
		t.Fatalf("a refused rotation removed objects: %d of %d left", len(after), len(before))
	}
	srv.RefuseDeletes(false)
	if rotated, _, warnings := svc.rotate(ctx, tgt, "server", nil, true); len(rotated) != 2 || len(warnings) != 0 {
		t.Fatalf("rotation = rotated %v, warnings %v", rotated, warnings)
	}
}

// TestS3BackupsPauseUntilTheKeyIsConfirmed: an S3 destination writes nothing
// until the confirmed check value is this server's, and a changed
// SECRET_KEY pauses it again, with a warning on the page.
func TestS3BackupsPauseUntilTheKeyIsConfirmed(t *testing.T) {
	srv := s3test.New(t, testBucket)
	key := testArchiveKey(t, "server-a")
	settings := s3Settings(srv, "")
	svc := New(Options{Settings: settings, SettingsWriter: settings, ArchiveKey: key, ServerID: func(context.Context) (string, error) { return "server", nil }})
	ctx := t.Context()
	if _, err := svc.preflight(ctx); !errors.Is(err, ErrKeyNotConfirmed) {
		t.Fatalf("preflight before confirming = %v, want ErrKeyNotConfirmed", err)
	}
	listing, err := svc.List(ctx)
	if err != nil || !hasWarning(listing, WarningKeyNotConfirmed) || listing.KeyConfirmed || listing.KeyCheck != key.Check {
		t.Fatalf("listing before confirming = %+v, %v", listing, err)
	}
	if err := svc.AcknowledgeKey(ctx, "0000-0000-0000-0000"); !errors.Is(err, ErrKeyCheckChanged) {
		t.Fatalf("acknowledging another check value = %v", err)
	}
	if err := svc.AcknowledgeKey(ctx, key.Check); err != nil {
		t.Fatal(err)
	}
	if tgt, err := svc.preflight(ctx); err != nil || tgt.key != key {
		t.Fatalf("preflight after confirming = %+v, %v", tgt, err)
	}
	if listing, _ := svc.List(ctx); hasWarning(listing, WarningKeyNotConfirmed) || !listing.KeyConfirmed || !listing.Encrypted || !listing.DestinationAvailable {
		t.Fatalf("listing after confirming = %+v", listing)
	}
	// The server now runs with another SECRET_KEY.
	changed := New(Options{Settings: settings, ArchiveKey: testArchiveKey(t, "server-b"), ServerID: svc.opts.ServerID})
	if _, err := changed.preflight(ctx); !errors.Is(err, ErrKeyNotConfirmed) {
		t.Fatalf("preflight after the key changed = %v, want ErrKeyNotConfirmed", err)
	}
	if listing, _ := changed.List(ctx); !hasWarning(listing, WarningKeyNotConfirmed) {
		t.Fatalf("listing after the key changed has no warning: %+v", listing.Warnings)
	}
}

// TestDefaultDestinationOnAPrivateBucketIsEncrypted: the default
// destination is the operational store, which is the private bucket when one
// is configured. Backups there are encrypted like the dedicated bucket's:
// they need the confirmed key (the before-upgrade backup skips without it),
// and uploads take the adaptive part size. A local
// filesystem default stays plaintext and needs no confirmation.
func TestDefaultDestinationOnAPrivateBucketIsEncrypted(t *testing.T) {
	ctx := t.Context()
	srv := s3test.New(t, testBucket)
	key := testArchiveKey(t, "server-a")
	settings := &mapSettings{values: map[string]string{}}
	private := NewStoreDestination(blobstore.NewS3(s3TestConfig(srv).Client()))
	serverID := func(context.Context) (string, error) { return "server", nil }
	svc := New(Options{Settings: settings, SettingsWriter: settings, Destination: private, ArchiveKey: key, ServerID: serverID})

	if !private.Encrypted() || !svc.Configured() {
		t.Fatalf("private bucket: encrypted %v, configured %v", private.Encrypted(), svc.Configured())
	}
	if _, err := svc.preflight(ctx); !errors.Is(err, ErrKeyNotConfirmed) {
		t.Fatalf("preflight before confirming = %v, want ErrKeyNotConfirmed", err)
	}
	if reason, warn, err := svc.skipBeforeUpgrade(ctx, false); err != nil || !warn || reason == "" {
		t.Fatalf("before-upgrade skip = %q, %v, %v", reason, warn, err)
	}
	if err := svc.AcknowledgeKey(ctx, key.Check); err != nil {
		t.Fatal(err)
	}
	tgt, err := svc.preflight(ctx)
	if err != nil || tgt.key != key {
		t.Fatalf("preflight after confirming = %+v, %v", tgt, err)
	}

	// The parts are sized from the previous backup on a copy; the shared
	// default destination keeps its own.
	const partSize = 16 << 20
	sized, err := prepareTarget(ctx, tgt, []folder{{id: "a", at: time.Now(), manifest: &Manifest{Objects: []ObjectEntry{{Size: 9_000 * partSize / 2}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := sized.Destination.(StoreDestination).PartSize; got != partSize {
		t.Fatalf("part size = %d, want %d", got, partSize)
	}
	if svc.opts.Destination.(StoreDestination).PartSize != 0 {
		t.Fatal("sizing the parts changed the shared default destination")
	}
	objectKey := RootPrefix + "server/x/" + sized.name(DumpName)
	if _, err := sized.put(ctx, objectKey, bytes.NewReader(make([]byte, partSize+1<<20))); err != nil {
		t.Fatal(err)
	}
	if _, largest := srv.Parts(); largest != partSize {
		t.Fatalf("largest part = %d, want %d", largest, partSize)
	}
	if data, _ := srv.Object(testBucket, "silo/"+objectKey); !strings.HasSuffix(objectKey, EncryptedSuffix) || !isAgeHeader(data) {
		t.Fatalf("%s is stored unencrypted", objectKey)
	}

	// Without the archive key the bucket is refused, never written in
	// plaintext.
	noKey := New(Options{Settings: settings, Destination: private, ServerID: serverID})
	if _, err := noKey.target(ctx); !errors.Is(err, ErrNoArchiveKey) || noKey.Configured() {
		t.Fatalf("target without a key = %v, configured %v", err, noKey.Configured())
	}

	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	local := New(Options{Settings: &mapSettings{values: map[string]string{}}, Destination: NewStoreDestination(blobstore.WithMutationFence(store)), ArchiveKey: key, ServerID: serverID})
	if tgt, err := local.preflight(ctx); err != nil || tgt.key != nil {
		t.Fatalf("local default: target %+v, %v; want plaintext without confirmation", tgt, err)
	}
}

func hasWarning(l Listing, code string) bool {
	for _, w := range l.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

// TestEncryptedBackupReadsWithThisServersKeyOnly: this server previews,
// lists, and downloads its own encrypted backup decrypted, never a
// substituted object; a backup taken with another key is listed as such and
// refused before anything is read.
func TestEncryptedBackupReadsWithThisServersKeyOnly(t *testing.T) {
	srv := s3test.New(t, testBucket)
	key := testArchiveKey(t, "server-a")
	settings := s3Settings(srv, key.Check)
	serverID := "server"
	svc := New(Options{Settings: settings, ArchiveKey: key, ServerID: func(context.Context) (string, error) { return serverID, nil }})
	ctx := t.Context()
	mine := putEncryptedBackup(t, srv, key, serverID, "20261031T040000Z-manual", []byte("restore set bytes"))
	other := putEncryptedBackup(t, srv, testArchiveKey(t, "server-b"), serverID, "20261030T040000Z-manual", []byte("other"))

	listing, err := svc.List(ctx)
	if err != nil || len(listing.Backups) != 2 {
		t.Fatalf("listing = %+v, %v", listing.Backups, err)
	}
	for _, b := range listing.Backups {
		if !b.Encrypted || b.KeyMatches != (b.ID == mine.ID) {
			t.Fatalf("summary %s: encrypted %v, key matches %v", b.ID, b.Encrypted, b.KeyMatches)
		}
	}

	body, size, err := svc.OpenObject(ctx, mine.ID, RestoreSetName)
	if err != nil || size != int64(len("restore set bytes")) {
		t.Fatalf("open the restore set = %d, %v", size, err)
	}
	plain, _ := io.ReadAll(body)
	_ = body.Close()
	if string(plain) != "restore set bytes" {
		t.Fatalf("decrypted restore set = %q", plain)
	}
	// An object replaced under the same key decrypts fine, but its stored
	// bytes are not the manifest's: nothing of it is released.
	setKey := "silo/" + objectKey(serverID, mine.ID, RestoreSetName+EncryptedSuffix)
	original, _ := srv.Object(testBucket, setKey)
	srv.Put(testBucket, setKey, encryptBytes(t, key, []byte("substituted restore set")))
	if body, _, err := svc.OpenObject(ctx, mine.ID, RestoreSetName); !errors.Is(err, ErrStoredMismatch) || body != nil {
		t.Fatalf("open a substituted restore set = %v, want ErrStoredMismatch and no body", err)
	}
	srv.Put(testBucket, setKey, original)
	links, err := svc.DownloadLinks(ctx, mine.ID, ScopeRestoreSet, time.Minute)
	if err != nil || len(links) != 1 || links[0].Name != RestoreSetName || links[0].URL != "" {
		t.Fatalf("restore set links = %+v, %v; want one unsigned link this server streams", links, err)
	}
	digest, open, err := svc.locate(ctx, restoreSource{backupID: mine.ID})
	if err != nil {
		t.Fatal(err)
	}
	if entry, _ := mine.object(RestoreSetName); digest != entry.SHA256 {
		t.Fatalf("restore set digest %s, want the stored one %s", digest, entry.SHA256)
	}
	r, err := open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(r)
	if stored, err := r.storedEntry(RestoreSetName); err != nil || stored.SHA256 != digest {
		t.Fatalf("read digest = %+v, %v", stored, err)
	}

	var wrong *WrongKeyError
	if _, _, err := svc.locate(ctx, restoreSource{backupID: other.ID}); !errors.As(err, &wrong) || wrong.Backup != other.KeyCheck {
		t.Fatalf("preview of another key's backup = %v, want WrongKeyError", err)
	}
	if _, _, err := svc.OpenObject(ctx, other.ID, RestoreSetName); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("download of another key's restore set = %v", err)
	}
}

// TestS3ConnectionTestKeepsTheStoredSecretAtItsEndpoint: a test that leaves
// the secret key empty uses the stored one only for the stored endpoint and
// access key, so it cannot send the secret elsewhere.
func TestS3ConnectionTestKeepsTheStoredSecretAtItsEndpoint(t *testing.T) {
	stored := S3Config{Endpoint: "https://s3.example.com", AccessKey: "a", SecretKey: "secret"}
	for _, c := range []struct {
		cfg  S3Config
		same bool
	}{
		{S3Config{Endpoint: "https://s3.example.com/", AccessKey: "a"}, true},
		{S3Config{Endpoint: "https://attacker.example", AccessKey: "a"}, false},
		{S3Config{Endpoint: "https://s3.example.com", AccessKey: "b"}, false},
	} {
		if got := sameS3Account(stored, c.cfg); got != c.same {
			t.Errorf("sameS3Account(%+v) = %v", c.cfg, got)
		}
	}
}
