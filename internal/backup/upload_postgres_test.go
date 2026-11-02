package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// uploadNode is one API server: its own Service over the shared database
// and the shared operational store.
func uploadNode(pool *pgxpool.Pool, store blobstore.Store) *Service {
	return uploadNodeOn(pool, store, NewStoreDestination(store), Options{})
}

// uploadNodeOn is uploadNode writing through dest, with the upload
// deadlines in limits.
func uploadNodeOn(pool *pgxpool.Pool, store blobstore.Store, dest Destination, limits Options) *Service {
	return New(Options{
		Pool: pool, Stores: blobstore.Stores{Assets: store, Operational: store}, Destination: dest,
		Locker: PostgresLocker{Pool: pool}, Status: PostgresStatus{Pool: pool},
		ServerID:               func(context.Context) (string, error) { return "upload-node", nil },
		UploadCheckTimeout:     limits.UploadCheckTimeout,
		UploadPartWriteTimeout: limits.UploadPartWriteTimeout,
	})
}

// resetUploads clears the sessions earlier runs left in the shared test
// database; only this package's tests write them.
func resetUploads(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	dbExec(t, pool, `DELETE FROM backup_uploads`)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM backup_uploads`) })
}

// paddedRestoreSet is a valid restore set a little over one minimum part:
// the reader allows up to 1 MiB of padding after the tar end marker.
func paddedRestoreSet(t *testing.T) []byte {
	t.Helper()
	index, entries := testRestoreSet()
	return append(buildTar(t, index, entries), make([]byte, 1<<20)...)
}

func sha256Of(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func putPart(t *testing.T, svc *Service, id string, data []byte, partSize int64, n int) {
	t.Helper()
	start := int64(n) * partSize
	end := min(start+partSize, int64(len(data)))
	if _, err := svc.PutUploadPart(t.Context(), id, n, bytes.NewReader(data[start:end]), end-start); err != nil {
		t.Fatalf("part %d: %v", n, err)
	}
}

// upload sends data in order and completes it.
func upload(t *testing.T, svc *Service, data []byte) UploadSession {
	t.Helper()
	u, err := svc.CreateUpload(t.Context(), 0, int64(len(data)), "", MinUploadPartSize)
	if err != nil {
		t.Fatal(err)
	}
	for n := range u.PartCount {
		putPart(t, svc, u.ID, data, u.PartSize, n)
	}
	done, err := svc.CompleteUpload(t.Context(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return done
}

func uploadObjects(t *testing.T, store blobstore.Store, id string) []string {
	t.Helper()
	objects, _, err := store.List(t.Context(), uploadPrefix(id), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, o := range objects {
		keys = append(keys, o.Key)
	}
	return keys
}

// uploadNodes are two API servers over one test database and one store.
func uploadNodes(t *testing.T) (pool *pgxpool.Pool, store blobstore.Store, a, b *Service) {
	t.Helper()
	pool, _ = testDB(t)
	resetUploads(t, pool)
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return pool, store, uploadNode(pool, store), uploadNode(pool, store)
}

// TestUploadSessionsDB: an upload's parts reach any API server and arrive
// in any order; a part sent again replaces the first copy; parts of the
// wrong size or position are refused; and completion freezes the digest of
// the bytes read and is idempotent.
func TestUploadSessionsDB(t *testing.T) {
	_, store, a, b := uploadNodes(t)
	ctx := t.Context()
	data := paddedRestoreSet(t)
	digest := sha256Of(data)

	u, err := a.CreateUpload(ctx, 0, int64(len(data)), digest, MinUploadPartSize)
	if err != nil {
		t.Fatal(err)
	}
	if u.PartCount != 2 || u.Status != UploadUploading || u.ExpiresAt == nil {
		t.Fatalf("created = %+v", u)
	}
	last := data[MinUploadPartSize:]
	for _, c := range []struct {
		name string
		n    int
		body []byte
	}{
		{"past the last part", 2, last},
		{"longer than its position", 1, append(append([]byte{}, last...), 0)},
	} {
		if _, err := a.PutUploadPart(ctx, u.ID, c.n, bytes.NewReader(c.body), -1); !errors.As(err, new(*UploadPartError)) {
			t.Fatalf("%s: err = %v, want UploadPartError", c.name, err)
		}
	}
	if keys := uploadObjects(t, store, u.ID); len(keys) != 0 {
		t.Fatalf("refused parts left objects: %v", keys)
	}
	// Out of order, across servers, with part 0 sent twice.
	putPart(t, b, u.ID, data, u.PartSize, 1)
	putPart(t, a, u.ID, data, u.PartSize, 0)
	putPart(t, b, u.ID, data, u.PartSize, 0)
	if got, _ := a.Upload(ctx, u.ID); got.ReceivedParts != 2 || got.ReceivedBytes != int64(len(data)) {
		t.Fatalf("after every part = %+v", got)
	}
	if keys := uploadObjects(t, store, u.ID); len(keys) != 2 {
		t.Fatalf("part objects = %v, want one per part", keys)
	}

	done, err := b.CompleteUpload(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != UploadComplete || done.Digest != digest || done.Source == nil || done.Source.ServerID != "server-a" ||
		done.ExpiresAt == nil || !done.ExpiresAt.Equal(done.UpdatedAt.Add(CompletedUploadExpiry)) {
		t.Fatalf("completed = %+v (source %+v)", done, done.Source)
	}
	if _, err := a.PutUploadPart(ctx, u.ID, 0, bytes.NewReader(data[:MinUploadPartSize]), MinUploadPartSize); !errors.Is(err, ErrUploadNotOpen) {
		t.Fatalf("a part after completion = %v, want ErrUploadNotOpen", err)
	}
	if again, err := a.CompleteUpload(ctx, u.ID); err != nil || again.Digest != digest {
		t.Fatalf("completing again = %+v, %v", again, err)
	}
}

// TestUploadLimitAndSweepDB: a server keeps three unfinished uploads at
// once, across its API servers; uploads abandoned for a day are swept with
// their parts, as are part folders no session knows.
func TestUploadLimitAndSweepDB(t *testing.T) {
	pool, store, a, b := uploadNodes(t)
	ctx := t.Context()
	completed := upload(t, a, paddedRestoreSet(t))

	// A complete upload does not count against the limit.
	var open []string
	for range MaxActiveUploads {
		s, err := a.CreateUpload(ctx, 0, 10, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, s.ID)
	}
	if _, err := b.CreateUpload(ctx, 0, 10, "", 0); !errors.Is(err, ErrTooManyUploads) {
		t.Fatalf("a fourth upload = %v, want ErrTooManyUploads", err)
	}
	if _, err := a.CreateUpload(ctx, 0, MaxUploadBytes+1, "", 0); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("an oversized upload = %v, want ErrUploadTooLarge", err)
	}

	// Abandoned for a day: the next read sweeps it with its parts, which
	// frees a place.
	if _, err := a.PutUploadPart(ctx, open[0], 0, strings.NewReader("0123456789"), 10); err != nil {
		t.Fatal(err)
	}
	dbExec(t, pool, `UPDATE backup_uploads SET updated_at = now() - interval '25 hours' WHERE id = $1`, open[0])
	list, err := b.Uploads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == open[0] {
			t.Fatal("an abandoned upload is still listed")
		}
	}
	if keys := uploadObjects(t, store, open[0]); len(keys) != 0 {
		t.Fatalf("an abandoned upload's parts remain: %v", keys)
	}
	if _, err := b.CreateUpload(ctx, 0, 10, "", 0); err != nil {
		t.Fatalf("an upload after the sweep = %v", err)
	}

	// Part folders no session knows: the old one goes, a recent one stays.
	root := blobstoreRoot(t, store)
	old, recent := uuid.NewString(), uuid.NewString()
	for _, id := range []string{old, recent} {
		if err := store.Put(ctx, uploadPrefix(id)+"00000.x", []byte("left")); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(uploadPrefix(old)+"00000.x")), past, past); err != nil {
		t.Fatal(err)
	}
	swept, warnings := a.sweepUploads(ctx, true, nil)
	if len(warnings) != 0 || len(swept) != 1 || swept[0] != "uploads/"+old || len(uploadObjects(t, store, recent)) != 1 {
		t.Fatalf("orphan sweep = %v %v", swept, warnings)
	}

	if err := b.DeleteUpload(ctx, open[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Upload(ctx, open[1]); !errors.Is(err, ErrUploadNotFound) {
		t.Fatalf("a deleted upload = %v", err)
	}

	// A completed upload unused for a week expires, but only under the
	// subsystem lock, which restores are queued under: a restore of it
	// cannot be queued between the sweep's look at the queue and the delete.
	dbExec(t, pool, `UPDATE backup_uploads SET updated_at = now() - interval '8 days' WHERE id = $1`, completed.ID)
	held, err := b.opts.Locker.TryLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, warnings := a.sweepUploads(ctx, false, nil); len(warnings) != 0 {
		t.Fatal(warnings)
	}
	if _, err := a.Upload(ctx, completed.ID); err != nil {
		t.Fatalf("an expired upload was swept while another operation held the lock: %v", err)
	}
	held.Release(ctx)
	var queued *RestoreRequest
	a.SetRestoreTargets(RestoreTargets{ActiveRestore: func(ctx context.Context) (*RestoreRequest, error) {
		// A restore being queued now waits for the sweep.
		if held, err := b.opts.Locker.TryLock(ctx); !errors.Is(err, ErrBusy) {
			if held != nil {
				held.Release(ctx)
			}
			t.Errorf("queueing a restore during the sweep = %v, want ErrBusy", err)
		}
		return queued, nil
	}})
	queued = &RestoreRequest{UploadID: completed.ID}
	if _, warnings := a.sweepUploads(ctx, false, nil); len(warnings) != 0 {
		t.Fatal(warnings)
	}
	if _, err := a.Upload(ctx, completed.ID); err != nil {
		t.Fatalf("an expired upload a queued restore reads was swept: %v", err)
	}
	queued = nil
	if swept, warnings := a.sweepUploads(ctx, false, nil); len(warnings) != 0 || !slices.Contains(swept, "uploads/"+completed.ID) {
		t.Fatalf("expired upload sweep = %v, %v", swept, warnings)
	}
	if held, err := b.opts.Locker.TryLock(ctx); err != nil {
		t.Fatalf("the sweep kept the lock: %v", err)
	} else {
		held.Release(ctx)
	}
}

// TestUploadRejectsHostileArchivesDB: completion reads every upload through
// the restore-set reader. What it refuses is rejected with its reason and
// its parts deleted, and can never be previewed.
func TestUploadRejectsHostileArchivesDB(t *testing.T) {
	pool, _ := testDB(t)
	resetUploads(t, pool)
	ctx := t.Context()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := uploadNode(pool, store)
	index, entries := testRestoreSet()
	newer := index
	newer.Version = restoreSetVersion + 1
	cases := []struct {
		name, reason string
		data         []byte
		sha256       string
	}{
		{"armored age", RejectNotRestoreSet, []byte("-----BEGIN AGE ENCRYPTED FILE-----\nYWdl\n-----END AGE ENCRYPTED FILE-----\n"), ""},
		{"newer format", RejectNewerSilo, buildTar(t, newer, entries), ""},
		{"trailing data", RejectNotRestoreSet, append(buildTar(t, index, entries), bytes.Repeat([]byte{1}, 2<<20)...), ""},
		{"other bytes than declared", RejectChecksum, buildTar(t, index, entries), strings.Repeat("0", 64)},
	}
	for _, c := range cases {
		u, err := svc.CreateUpload(ctx, 0, int64(len(c.data)), c.sha256, MinUploadPartSize)
		if err != nil {
			t.Fatal(err)
		}
		for n := range u.PartCount {
			putPart(t, svc, u.ID, c.data, u.PartSize, n)
		}
		done, err := svc.CompleteUpload(ctx, u.ID)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if done.Status != UploadRejected || done.Rejection == nil || done.Rejection.Reason != c.reason || done.Digest != "" {
			t.Fatalf("%s: completed = %+v (rejection %+v), want rejected %s", c.name, done, done.Rejection, c.reason)
		}
		if keys := uploadObjects(t, store, u.ID); len(keys) != 0 {
			t.Fatalf("%s: a rejected upload's parts remain: %v", c.name, keys)
		}
		if _, err := svc.PreviewUpload(ctx, u.ID); !errors.Is(err, ErrUploadNotReady) {
			t.Fatalf("%s: preview of a rejected upload = %v", c.name, err)
		}
		// Rejected uploads do not count against the limit and go at once.
		if err := svc.DeleteUpload(ctx, u.ID); err != nil {
			t.Fatal(err)
		}
	}
	resetUploads(t, pool)

	// Completing before every part arrived is refused and changes nothing.
	data := paddedRestoreSet(t)
	u, err := svc.CreateUpload(ctx, 0, int64(len(data)), "", MinUploadPartSize)
	if err != nil {
		t.Fatal(err)
	}
	putPart(t, svc, u.ID, data, u.PartSize, 1)
	if _, err := svc.CompleteUpload(ctx, u.ID); !errors.As(err, new(*UploadIncompleteError)) {
		t.Fatalf("completing with a part missing = %v", err)
	}
	if got, _ := svc.Upload(ctx, u.ID); got.Status != UploadUploading {
		t.Fatalf("after a refused completion = %+v", got)
	}
}

// otherServer is a second server over the fixture's database and storage,
// with its own identity and SECRET_KEY. queued stands in for the restore
// job queued or running now.
type otherServer struct {
	svc      *Service
	serverID string
	queued   *RestoreRequest
}

func newOtherServer(t *testing.T, f *selectiveFixture) *otherServer {
	t.Helper()
	key := []byte("another-servers-secret-key-0123456789")
	cipher, err := secret.New(key)
	if err != nil {
		t.Fatal(err)
	}
	keyCheck, _ := secret.KeyCheckValue(key)
	o := &otherServer{serverID: "other-" + f.serverID}
	o.svc = New(Options{
		Pool: f.pool, DatabaseURL: f.svc.opts.DatabaseURL, Settings: catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(f.pool), cipher),
		Cipher: cipher, KeyCheck: keyCheck, Stores: f.svc.opts.Stores, Destination: f.svc.opts.Destination,
		Locker: PostgresLocker{Pool: f.pool}, Status: PostgresStatus{Pool: f.pool},
		ServerID:  func(context.Context) (string, error) { return o.serverID, nil },
		FindTools: f.svc.opts.FindTools,
	})
	targets := f.svc.targets
	targets.CreateJob = func(context.Context, int, RestoreRequest) (*models.AdminJob, error) {
		return &models.AdminJob{ID: "queued"}, nil
	}
	targets.ActiveRestore = func(context.Context) (*RestoreRequest, error) { return o.queued, nil }
	o.svc.SetRestoreTargets(targets)
	return o
}

func (o *otherServer) restore(t *testing.T, f *selectiveFixture, req RestoreRequest) (*SelectiveReport, error) {
	t.Helper()
	payload, _ := json.Marshal(req)
	result, err := o.svc.ExecuteRestore(t.Context(), RestoreRun{Payload: payload, Fenced: (&testFence{pool: f.pool}).run})
	report, _ := result.(*SelectiveReport)
	return report, err
}

func (o *otherServer) backups(t *testing.T) []folder {
	t.Helper()
	folders, err := o.svc.folders(t.Context(), target{Destination: o.svc.opts.Destination}, o.serverID)
	if err != nil {
		t.Fatal(err)
	}
	return folders
}

// downloadRestoreSet is what Download restore set gives for a backup.
func downloadRestoreSet(t *testing.T, svc *Service, id string) []byte {
	t.Helper()
	body, _, err := svc.OpenObject(t.Context(), id, RestoreSetName)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	var data bytes.Buffer
	if _, err := data.ReadFrom(body); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

// checkCrossServerPreview: another server id and SECRET_KEY, the library
// matched by its folders, and the encrypted setting not offered.
func checkCrossServerPreview(t *testing.T, preview *RestorePreview, u UploadSession, seed selectiveSeed) {
	t.Helper()
	if preview.SameServer || preview.KeyMatches || preview.Digest != u.Digest || preview.UploadID != u.ID || preview.BackupID != "" {
		t.Fatalf("preview source = same %v key %v digest %s upload %q backup %q", preview.SameServer, preview.KeyMatches, preview.Digest, preview.UploadID, preview.BackupID)
	}
	if item := previewItem(t, preview, SectionLibraries, strconv.Itoa(seed.libraryID)); item.Status != ItemNotOnThisServer || !item.Selectable {
		t.Fatalf("library by path = %+v", item)
	}
	settings := previewItem(t, preview, SectionSettings, "other").Settings
	for _, s := range settings {
		if s.Key == seed.secretKey && s.Status == ItemNeedsAnotherKey && s.Masked {
			return
		}
	}
	t.Fatalf("an encrypted setting under another key is offered: %+v", settings)
}

// tamper changes the last byte of an upload's first part.
func tamper(t *testing.T, store blobstore.Store, id string) {
	t.Helper()
	path := filepath.Join(blobstoreRoot(t, store), filepath.FromSlash(uploadObjects(t, store, id)[0]))
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stored[len(stored)-1] ^= 0xff
	if err := os.WriteFile(path, stored, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRestoreFromUploadDB: a restore set downloaded from one server and
// uploaded to another is previewed and restored there through the
// cross-server paths, bound to the digest frozen at completion. A queued
// or running restore keeps its upload from being deleted.
func TestRestoreFromUploadDB(t *testing.T) {
	f := newSelectiveFixture(t)
	resetUploads(t, f.pool)
	ctx := t.Context()
	seed := seedSelective(t, f.pool, f.settings, f.store)
	source, err := backupUnderLock(t, f.svc, KindManual)
	if err != nil {
		t.Fatal(err)
	}
	data := downloadRestoreSet(t, f.svc, source.ID)
	other := newOtherServer(t, f)
	u := upload(t, other.svc, data)
	if u.Status != UploadComplete || u.Digest != sha256Of(data) {
		t.Fatalf("upload = %+v", u)
	}
	f.lose(t, seed)
	preview, err := other.svc.PreviewUpload(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkCrossServerPreview(t, preview, u, seed)

	libraryRef := strconv.Itoa(seed.libraryID)
	req := RestoreRequest{UploadID: u.ID, Digest: strings.Repeat("0", 64), Selection: map[string][]string{SectionLibraries: {libraryRef}}}
	if _, err := other.svc.StartRestore(ctx, 1, req); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("queueing with another digest = %v, want ErrDigestMismatch", err)
	}
	if _, err := other.restore(t, f, req); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("restore with another digest = %v, want ErrDigestMismatch", err)
	}
	req.Digest = u.Digest
	if _, err := other.svc.StartRestore(ctx, 1, RestoreRequest{UploadID: u.ID, BackupID: source.ID, Digest: u.Digest, Selection: req.Selection}); !errors.As(err, new(*SelectionError)) {
		t.Fatalf("queueing with two sources = %v, want SelectionError", err)
	}
	if _, err := other.svc.StartRestore(ctx, 1, req); err != nil {
		t.Fatal(err)
	}
	report, err := other.restore(t, f, req)
	if err != nil {
		t.Fatal(err)
	}
	if report.UploadID != u.ID || reportOutcomes(report)[SectionLibraries+":"+libraryRef] != OutcomeRestored {
		t.Fatalf("restore report = %+v", report)
	}
	if backups := other.backups(t); len(backups) != 1 || backups[0].id != report.BeforeRestoreBackupID {
		t.Fatalf("the other server's backups = %+v, want its before-restore copy %q", backups, report.BeforeRestoreBackupID)
	}

	// A queued restore of the upload, or the lock a running one holds,
	// keeps it.
	other.queued = &RestoreRequest{UploadID: u.ID}
	if err := other.svc.DeleteUpload(ctx, u.ID); !errors.Is(err, ErrUploadInUse) {
		t.Fatalf("delete with a queued restore = %v, want ErrUploadInUse", err)
	}
	other.queued = nil
	held, err := other.svc.opts.Locker.TryLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.svc.DeleteUpload(ctx, u.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("delete while the lock is held = %v, want ErrBusy", err)
	}
	held.Release(ctx)

	// Bytes changed after completion: the restore, which never uses the
	// preview cache, refuses them before taking its backup.
	tamper(t, f.store, u.ID)
	if _, err := other.restore(t, f, req); err == nil {
		t.Fatal("a restore read changed bytes")
	}
	if backups := other.backups(t); len(backups) != 1 {
		t.Fatalf("a refused restore took a backup: %+v", backups)
	}
	if err := other.svc.DeleteUpload(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if keys := uploadObjects(t, f.store, u.ID); len(keys) != 0 {
		t.Fatalf("a deleted upload's parts remain: %v", keys)
	}
}
