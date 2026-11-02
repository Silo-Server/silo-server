package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

// TestWriteBlobNeverReplacesAnExistingObject: a restore writes a blob only
// where nothing is, so an archive that names a live asset's key cannot
// replace it. An object with the same bytes is left as it is.
func TestWriteBlobNeverReplacesAnExistingObject(t *testing.T) {
	ctx := context.Background()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	extracted := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(extracted, []byte("from the backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	const key = "local/movies/m1/abcd1234/poster/original.r1.jpg"
	a := &applier{s: New(Options{Stores: blobstore.Stores{Assets: store}}), rs: &RestoreSet{blobs: map[string]string{key: extracted}}}
	read := func() string {
		body, _, err := store.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = body.Close() }()
		data := make([]byte, 64)
		n, _ := body.Read(data)
		return string(data[:n])
	}

	if err := store.Put(ctx, key, []byte("live asset")); err != nil {
		t.Fatal(err)
	}
	if err := a.writeBlob(ctx, key, key); !errors.Is(err, ErrBlobConflict) {
		t.Fatalf("writing over another object = %v, want ErrBlobConflict", err)
	}
	if got := read(); got != "live asset" {
		t.Fatalf("existing object became %q", got)
	}

	if err := store.Put(ctx, key, []byte("from the backup")); err != nil {
		t.Fatal(err)
	}
	if err := a.writeBlob(ctx, key, key); err != nil {
		t.Fatalf("writing over the same bytes = %v", err)
	}
}

// TestApplyBrandingChecksTheSettingAgain: a branding item becomes a
// settings write, so the apply step refuses anything but a branding asset
// setting naming its own blob, whatever the parser admitted.
func TestApplyBrandingChecksTheSettingAgain(t *testing.T) {
	entry := BrandingEntry{Key: "setup.completed", Ref: "evil.example", Blob: "branding/mark/evil.example"}
	a := &applier{s: New(Options{}), rs: &RestoreSet{Collections: CollectionsDocument{Branding: []BrandingEntry{entry}}}}
	if _, err := a.applyBranding(context.Background(), nil, entry.itemID()); err == nil {
		t.Fatalf("branding entry %+v was applied", entry)
	}
}

// mapSettings is a SettingsReader over a map.
type mapSettings struct{ values map[string]string }

func (m *mapSettings) Get(_ context.Context, key string) (string, error) { return m.values[key], nil }

// memLocker is an in-process subsystem lock.
type memLocker struct {
	mu   sync.Mutex
	held bool
}

func (l *memLocker) TryLock(context.Context) (Held, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held {
		return nil, ErrBusy
	}
	l.held = true
	return &memHeld{l: l, lost: make(chan struct{})}, nil
}

func (l *memLocker) Holder(context.Context) (LockSession, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return LockSession{PID: 1}, l.held, nil
}

func (l *memLocker) isHeld() bool {
	_, held, _ := l.Holder(context.Background())
	return held
}

type memHeld struct {
	l    *memLocker
	once sync.Once
	lost chan struct{}
}

func (h *memHeld) Lost() <-chan struct{} { return h.lost }
func (h *memHeld) Release(context.Context) {
	h.once.Do(func() { h.l.mu.Lock(); h.l.held = false; h.l.mu.Unlock() })
}
func (h *memHeld) Session() LockSession { return LockSession{PID: 1} }
func (h *memHeld) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

// putPlainBackup stores a complete unencrypted backup with a restore set
// and returns the restore set's digest.
func putPlainBackup(t *testing.T, store blobstore.Store, serverID, id string) string {
	t.Helper()
	ctx := t.Context()
	m := validTestManifest()
	m.ID, m.ServerID = id, serverID
	m.CreatedAt, m.Kind, _ = ParseFolderName(id)
	m.CompletedAt = m.CreatedAt
	m.Objects = nil
	for name, data := range map[string][]byte{DumpName: []byte("dump"), BlobsName: {}, RestoreSetName: []byte("set " + id)} {
		if err := store.Put(ctx, objectKey(serverID, id, name), data); err != nil {
			t.Fatal(err)
		}
		s := newSum()
		_, _ = s.Write(data)
		m.Objects = append(m.Objects, s.entry(name))
	}
	m.RestoreSet = &RestoreSetSummary{FormatVersion: restoreSetVersion, Sections: map[string]int{}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, objectKey(serverID, id, ManifestName), data); err != nil {
		t.Fatal(err)
	}
	entry, _ := m.object(RestoreSetName)
	return entry.SHA256
}

// TestQueuedRestoreKeepsItsSource: a restore is queued under the
// subsystem lock after its source is checked again, and backup deletion
// and rotation, which hold the same lock, keep the source of a queued or
// running restore. A delete racing the queueing waits for it instead of
// removing the source between the check and the job.
func TestQueuedRestoreKeepsItsSource(t *testing.T) {
	ctx := t.Context()
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	day := func(n int) time.Time { return time.Date(2026, 10, 20+n, 4, 0, 0, 0, time.UTC) }
	source, older := FolderName(day(1), KindNightly), FolderName(day(0), KindNightly)
	digest := putPlainBackup(t, store, "server", source)
	putPlainBackup(t, store, "server", older)
	putPlainBackup(t, store, "server", FolderName(day(2), KindNightly))

	locker := &memLocker{}
	var (
		mu        sync.Mutex
		queued    *RestoreRequest
		activeErr error
		raced     error
	)
	svc := New(Options{Destination: NewStoreDestination(store), Locker: locker,
		Settings: &mapSettings{values: map[string]string{config.BackupKeepSettingKey: "1"}},
		ServerID: func(context.Context) (string, error) { return "server", nil }, Now: func() time.Time { return day(3) }})
	svc.SetRestoreTargets(RestoreTargets{
		CreateJob: func(ctx context.Context, _ int, req RestoreRequest) (*models.AdminJob, error) {
			if !locker.isHeld() {
				t.Error("the restore job was created outside the subsystem lock")
			}
			// A delete of the source arriving now must not slip in.
			raced = svc.Delete(ctx, req.BackupID)
			mu.Lock()
			queued = &req
			mu.Unlock()
			return &models.AdminJob{ID: "job"}, nil
		},
		ActiveRestore: func(context.Context) (*RestoreRequest, error) {
			mu.Lock()
			defer mu.Unlock()
			return queued, activeErr
		},
	})
	req := RestoreRequest{BackupID: source, Digest: digest, Selection: map[string][]string{SectionSettings: {pageAppearance}}}

	// Another operation holds the lock: nothing is queued.
	other, err := locker.TryLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartRestore(ctx, 1, req); !errors.Is(err, ErrBusy) || queued != nil {
		t.Fatalf("restore while the lock is held = %v, queued %v", err, queued)
	}
	other.Release(ctx)

	if _, err := svc.StartRestore(ctx, 1, req); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(raced, ErrBusy) {
		t.Fatalf("a delete racing the queueing = %v, want ErrBusy", raced)
	}
	if locker.isHeld() {
		t.Fatal("queueing a restore kept the lock")
	}
	if err := svc.Delete(ctx, source); !errors.Is(err, ErrBackupInUse) {
		t.Fatalf("deleting a queued restore's source = %v, want ErrBackupInUse", err)
	}

	tgt, err := svc.target(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rotated, _, warnings := svc.rotate(ctx, tgt, "server", nil)
	if !slices.Equal(rotated, []string{older}) || len(warnings) != 0 {
		t.Fatalf("rotation with a queued restore = %v, %v; want only %s", rotated, warnings, older)
	}
	if _, err := store.Stat(ctx, objectKey("server", source, ManifestName)); err != nil {
		t.Fatalf("the queued restore's source is gone: %v", err)
	}

	// When the queue cannot be read, nothing is deleted.
	activeErr = errors.New("database unavailable")
	if rotated, _, warnings := svc.rotate(ctx, tgt, "server", nil); len(rotated) != 0 || len(warnings) != 1 {
		t.Fatalf("rotation without the queue = %v, %v", rotated, warnings)
	}
	if err := svc.Delete(ctx, source); err == nil || errors.Is(err, ErrBackupInUse) {
		t.Fatalf("delete without the queue = %v", err)
	}
	activeErr, queued = nil, nil
	if err := svc.Delete(ctx, source); err != nil {
		t.Fatalf("deleting a backup no restore reads = %v", err)
	}
}
