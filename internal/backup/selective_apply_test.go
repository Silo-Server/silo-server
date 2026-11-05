package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/sections"
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
	fromBackup, live := testImage(t, "from the backup"), testImage(t, "live asset")
	if err := os.WriteFile(extracted, fromBackup, 0o600); err != nil {
		t.Fatal(err)
	}
	const key = "local/movies/m1/abcd1234/poster/original.r1.jpg"
	a := &applier{s: New(Options{Stores: blobstore.Stores{Assets: store}}), rs: &RestoreSet{blobs: map[string]string{key: extracted}}}
	read := func() []byte {
		body, _, err := store.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = body.Close() }()
		data, err := io.ReadAll(body)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	if err := store.Put(ctx, key, live); err != nil {
		t.Fatal(err)
	}
	if err := a.writeBlob(ctx, key, key); !errors.Is(err, ErrBlobConflict) {
		t.Fatalf("writing over another object = %v, want ErrBlobConflict", err)
	}
	if got := read(); !bytes.Equal(got, live) {
		t.Fatal("the existing object changed")
	}

	if err := store.Put(ctx, key, fromBackup); err != nil {
		t.Fatal(err)
	}
	if err := a.writeBlob(ctx, key, key); err != nil {
		t.Fatalf("writing over the same bytes = %v", err)
	}
}

// testImage is a small PNG whose pixels depend on seed, so two seeds make
// files with different bytes.
func testImage(t *testing.T, seed string) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	sum := sha256.Sum256([]byte(seed))
	for i := range img.Pix {
		img.Pix[i] = sum[i%len(sum)] | 1
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestRestoredFilesMeetTheUploadChecks: a restore writes only a file its
// kind's upload would accept, so a backup cannot place a file of another
// type or size where a server serves uploads.
func TestRestoredFilesMeetTheUploadChecks(t *testing.T) {
	img := testImage(t, "poster")
	webp, err := imageutil.EncodeWebPWidth(img, 4)
	if err != nil {
		t.Fatal(err)
	}
	oversized := append(slices.Clone(img), make([]byte, maxRestoredImage)...)
	for name, tc := range map[string]struct {
		key  string
		data []byte
		ok   bool
	}{
		"item artwork upload":         {key: "local/movies/m1/abcd1234/poster/original.r1.jpg", data: img, ok: true},
		"collection image":            {key: "collection-images/c1/poster/w300.abcd.webp", data: webp, ok: true},
		"branding mark":               {key: "branding/mark/0123456789abcdef.webp", data: webp, ok: true},
		"favicon":                     {key: "branding/favicon/0123456789abcdef.png", data: img, ok: true},
		"page served as an image":     {key: "library-posters/3.jpg", data: []byte("<html><script>alert(1)</script></html>")},
		"image over the upload limit": {key: "library-posters/3.jpg", data: oversized},
		"bytes that do not decode":    {key: "library-posters/3.jpg", data: img[:len(img)/2]},
		"branding stored unconverted": {key: "branding/mark/0123456789abcdef.png", data: img},
		"branding of another type":    {key: "branding/login-bg/0123456789abcdef.webp", data: img},
		"branding outside a kind":     {key: "branding/other/0123456789abcdef.webp", data: webp},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkRestoredBlob(tc.key, tc.data)
			if tc.ok != (err == nil) || err != nil && !errors.Is(err, ErrRestoredFileRefused) {
				t.Fatalf("checkRestoredBlob(%s) = %v, want ok %v", tc.key, err, tc.ok)
			}
		})
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

func (l *memLocker) Lock(ctx context.Context, _ time.Duration) (Held, error) { return l.TryLock(ctx) }

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

// putPlainBackup stores a complete unencrypted backup with a restore set,
// its manifest signed with testManifestKey, and returns the restore set's
// digest.
func putPlainBackup(t *testing.T, store blobstore.Store, serverID, id string, change ...func(*Manifest)) string {
	t.Helper()
	ctx := t.Context()
	m := validTestManifest()
	m.ID, m.ServerID = id, serverID
	m.CreatedAt, m.Kind, _ = ParseFolderName(id)
	m.CompletedAt = m.CreatedAt
	m.Objects = nil
	index, entries := testRestoreSet()
	index.ServerID, index.ServerName = serverID, id
	set := buildTar(t, index, entries)
	indexSum := sha256.Sum256(mustJSON(index))
	for name, data := range map[string][]byte{DumpName: []byte("dump"), BlobsName: {}, RestoreSetName: set} {
		if err := store.Put(ctx, objectKey(serverID, id, name), data); err != nil {
			t.Fatal(err)
		}
		s := newSum()
		_, _ = s.Write(data)
		m.Objects = append(m.Objects, s.entry(name))
	}
	m.RestoreSet = &RestoreSetSummary{FormatVersion: restoreSetVersion, Sections: map[string]int{}, IndexSHA256: hex.EncodeToString(indexSum[:])}
	m.KeyCheck = testKeyCheck
	for _, c := range change {
		c(&m)
	}
	if err := m.sign(testManifestKey); err != nil {
		t.Fatal(err)
	}
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
	svc := New(Options{Destination: NewStoreDestination(store), Locker: locker, KeyCheck: testKeyCheck, ManifestKey: testManifestKey,
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
	})
	svc.opts.ActiveRestore = func(context.Context) (*RestoreRequest, error) {
		mu.Lock()
		defer mu.Unlock()
		return queued, activeErr
	}
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
	rotated, _, warnings := svc.rotate(ctx, nil, tgt, "server", nil, false)
	if !slices.Equal(rotated, []string{older}) || len(warnings) != 0 {
		t.Fatalf("rotation with a queued restore = %v, %v; want only %s", rotated, warnings, older)
	}
	if _, err := store.Stat(ctx, objectKey("server", source, ManifestName)); err != nil {
		t.Fatalf("the queued restore's source is gone: %v", err)
	}

	// When the queue cannot be read, nothing is deleted.
	activeErr = errors.New("database unavailable")
	if rotated, _, warnings := svc.rotate(ctx, nil, tgt, "server", nil, false); len(rotated) != 0 || len(warnings) != 1 {
		t.Fatalf("rotation without the queue = %v, %v", rotated, warnings)
	}
	if err := svc.Delete(ctx, source); err == nil || errors.Is(err, ErrBackupInUse) {
		t.Fatalf("delete without the queue = %v", err)
	}
	// Nor when the service has no way to read it.
	active := svc.opts.ActiveRestore
	svc.opts.ActiveRestore = nil
	if rotated, _, warnings := svc.rotate(ctx, nil, tgt, "server", nil, false); len(rotated) != 0 || len(warnings) != 1 {
		t.Fatalf("rotation without a queue reader = %v, %v", rotated, warnings)
	}
	svc.opts.ActiveRestore = active
	activeErr, queued = nil, nil
	if err := svc.Delete(ctx, source); err != nil {
		t.Fatalf("deleting a backup no restore reads = %v", err)
	}
}

// TestSelectiveRestoreAuthenticatesTheManifest: a selective restore starts
// only from a backup whose manifest verifies under this server's
// SECRET_KEY, or, when it cannot be authenticated (no key check value or
// another key), with the admin's confirmation. One naming this key that
// does not verify is refused even with it.
func TestSelectiveRestoreAuthenticatesTheManifest(t *testing.T) {
	tests := []struct {
		name          string
		change        func(*Manifest)
		want          error // without confirmation
		wantConfirmed error
	}{
		{name: "matching", change: func(*Manifest) {}},
		{name: "wrong MAC", change: func(m *Manifest) { m.MAC = strings.Repeat("ab", 32) }, want: ErrManifestTampered, wantConfirmed: ErrManifestTampered},
		{name: "missing MAC", change: func(m *Manifest) { m.MAC = "" }, want: ErrManifestTampered, wantConfirmed: ErrManifestTampered},
		{name: "missing key check", change: func(m *Manifest) { m.KeyCheck = "" }, want: ErrUnauthenticated},
		{name: "other key", change: func(m *Manifest) { m.KeyCheck = "9999-8888-7777-6666" }, want: ErrUnauthenticated},
		// A manifest that does not record the index's digest cannot have
		// its restore set checked before it is parsed.
		{name: "no index digest", change: func(m *Manifest) {
			m.RestoreSet.IndexSHA256 = ""
			if err := m.sign(testManifestKey); err != nil {
				panic(err)
			}
		}, want: ErrUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			store, err := blobstore.NewFilesystem(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			id := FolderName(time.Date(2026, 10, 21, 4, 0, 0, 0, time.UTC), KindNightly)
			digest := putPlainBackup(t, store, "server", id)
			key := objectKey("server", id, ManifestName)
			body, _, err := store.Get(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(body)
			_ = body.Close()
			m, err := ParseManifest(data)
			if err != nil {
				t.Fatal(err)
			}
			tt.change(m)
			changed, _ := json.Marshal(m)
			if err := store.Put(ctx, key, changed); err != nil {
				t.Fatal(err)
			}
			svc := New(Options{Destination: NewStoreDestination(store), Locker: &memLocker{}, KeyCheck: testKeyCheck, ManifestKey: testManifestKey,
				ServerID: func(context.Context) (string, error) { return "server", nil }})
			queued := 0
			svc.SetRestoreTargets(RestoreTargets{CreateJob: func(context.Context, int, RestoreRequest) (*models.AdminJob, error) {
				queued++
				return &models.AdminJob{ID: "job"}, nil
			}})
			svc.opts.ActiveRestore = func(context.Context) (*RestoreRequest, error) { return nil, nil }
			req := RestoreRequest{BackupID: id, Digest: digest, Selection: map[string][]string{SectionSettings: {"appearance"}}}
			if _, err := svc.StartRestore(ctx, 1, req); !errors.Is(err, tt.want) || (tt.want == nil) != (err == nil) {
				t.Fatalf("StartRestore = %v, want %v", err, tt.want)
			}
			// A preview refuses the same way, before it reads the restore set.
			if _, _, err := svc.previewSet(ctx, restoreSource{backupID: id}, false); !errors.Is(err, tt.want) || (tt.want == nil) != (err == nil) {
				t.Fatalf("preview = %v, want %v", err, tt.want)
			}
			req.AcceptUnauthenticated = true
			if _, err := svc.StartRestore(ctx, 1, req); !errors.Is(err, tt.wantConfirmed) || (tt.wantConfirmed == nil) != (err == nil) {
				t.Fatalf("StartRestore confirmed = %v, want %v", err, tt.wantConfirmed)
			}
			if _, _, err := svc.previewSet(ctx, restoreSource{backupID: id}, true); !errors.Is(err, tt.wantConfirmed) || (tt.wantConfirmed == nil) != (err == nil) {
				t.Fatalf("preview confirmed = %v, want %v", err, tt.wantConfirmed)
			}
		})
	}
}

// TestRetryableItemError: an item starts again from its read after a
// deadlock, a serialization failure, or a section whose references changed
// under it, and fails on anything else.
func TestRetryableItemError(t *testing.T) {
	for err, want := range map[error]bool{
		fmt.Errorf("writing: %w", sections.ErrReferencesChanged): true,
		fmt.Errorf("settings: %w", errSettingsChanged):           true,
		&pgconn.PgError{Code: "40P01"}:                           true,
		fmt.Errorf("item: %w", &pgconn.PgError{Code: "40001"}):   true,
		&pgconn.PgError{Code: "23505"}:                           false,
		errors.New("boom"):                                       false,
	} {
		if got := retryableItemError(err); got != want {
			t.Errorf("retryableItemError(%v) = %v, want %v", err, got, want)
		}
	}
}

// TestCollectionMetadataBudget: once a restore's committed collections
// reach its budget, in all or in one library, the next is left out with
// its reason; another library still has room until the total is reached.
func TestCollectionMetadataBudget(t *testing.T) {
	a := &applier{}
	big := collectionCharge{library: 1, bytes: maxLibraryCollectionMetadata - 10}
	if _, over := a.overCollectionBudget(ReportItem{}, big); over {
		t.Fatal("the first collection is over the budget")
	}
	a.charge(big)
	if got, over := a.overCollectionBudget(ReportItem{}, collectionCharge{library: 1, bytes: 11}); !over || got.item.Reason != ReasonOverRestoreBudget {
		t.Fatalf("over the library's budget = %+v, %v", got.item, over)
	}
	if _, over := a.overCollectionBudget(ReportItem{}, collectionCharge{library: 2, bytes: 11}); over {
		t.Fatal("another library has room")
	}
	a.charge(collectionCharge{library: 2, bytes: maxRestoreCollectionMetadata - maxLibraryCollectionMetadata})
	if _, over := a.overCollectionBudget(ReportItem{}, collectionCharge{library: 3, bytes: 11}); !over {
		t.Fatal("the restore's total budget is spent")
	}
}

// busyLocker answers ErrBusy until free is closed, and signals tries on
// each answer when set.
type busyLocker struct {
	Locker
	free, tries chan struct{}
}

func (l busyLocker) TryLock(context.Context) (Held, error) {
	select {
	case <-l.free:
		return nil, nil
	default:
	}
	if l.tries != nil {
		select {
		case l.tries <- struct{}{}:
		default:
		}
	}
	return nil, ErrBusy
}

// TestRestoreJobWaitsForTheRequestThatQueuedIt: a restore job claimed while
// the request that queued it still holds the lock takes the lock once that
// request lets go, and gives up only after enqueueHandoff.
func TestRestoreJobWaitsForTheRequestThatQueuedIt(t *testing.T) {
	l := busyLocker{free: make(chan struct{}), tries: make(chan struct{}, 1)}
	// The request lets go once the job has asked once and found it held.
	go func() { <-l.tries; close(l.free) }()
	if _, err := lockWithin(t.Context(), l, enqueueHandoff); err != nil {
		t.Fatalf("lock released after the handoff: %v", err)
	}
	if _, err := lockWithin(t.Context(), busyLocker{free: make(chan struct{})}, 0); !errors.Is(err, ErrBusy) {
		t.Fatalf("lock held past the wait: %v, want ErrBusy", err)
	}
}

// TestUnverifiedTemplateWithholdsSourceAndSchedule: from a backup this
// server cannot verify, a template collection's changed source and sync
// schedule are withheld, not written, since either makes it fetch from
// outside services; the other changes still apply.
func TestUnverifiedTemplateWithholdsSourceAndSchedule(t *testing.T) {
	daily := "0 3 * * *"
	entry := TemplateCollectionEntry{Title: "Mine", SourceURL: "https://lists.example.test/1", SourceConfig: json.RawMessage(`{"list":"1"}`),
		SyncSchedule: &daily}
	def := catalog.CreateLibraryCollectionInput{Title: "Popular"}
	trusted := customizeTemplate(entry, def, nil, nil, true)
	if !slices.Equal(trusted.changes, []string{"title", "source", "sync schedule"}) || len(trusted.withheld) != 0 || trusted.update.SyncSchedule == nil {
		t.Fatalf("verified = %+v", trusted)
	}
	untrusted := customizeTemplate(entry, def, nil, nil, false)
	if !slices.Equal(untrusted.changes, []string{"title"}) || !slices.Equal(untrusted.withheld, []string{"source", "sync schedule"}) ||
		untrusted.update.SourceURL != nil || untrusted.update.SyncSchedule != nil || untrusted.update.SourceConfig != nil {
		t.Fatalf("unverified = %+v, want the source and schedule withheld", untrusted)
	}
}

// TestRestoredLibraryFoldersAreBounded: a library entry whose folders the
// preview would check on disk is refused past maxLibraryFolders folders,
// or with a folder listed twice.
func TestRestoredLibraryFoldersAreBounded(t *testing.T) {
	entry := LibraryEntry{ID: 1, Name: "Movies", Type: "movies", Paths: []string{"/media/a", "/media/b"}}
	if problem := libraryProblem(entry); problem != "" {
		t.Fatalf("two folders: %s", problem)
	}
	many := LibraryEntry{ID: 1, Name: "Movies", Type: "movies"}
	for i := range maxLibraryFolders + 1 {
		many.Paths = append(many.Paths, fmt.Sprintf("/media/%d", i))
	}
	twice := LibraryEntry{ID: 1, Name: "Movies", Type: "movies", Paths: []string{"/", "/"}}
	for name, e := range map[string]LibraryEntry{"too many folders": many, "a folder twice": twice} {
		if libraryProblem(e) == "" {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestUnverifiedWorkIsClassified: every entry of unverifiedWorkFields
// names where the restore leaves it inactive, every boolean option of a
// restored library is either such work or named as none, and each library
// option and the seasonal section rule leave their work off for a backup
// this server cannot verify, and on for one it can.
func TestUnverifiedWorkIsClassified(t *testing.T) {
	kinds := map[string]bool{KindLibrary: true, KindCollection: true, KindTemplateCollection: true, KindHomeSection: true, KindPlugin: true, KindSettingsPage: true}
	for _, w := range unverifiedWorkFields {
		if !kinds[w.kind] || w.field == "" || w.label == "" || w.sets == "" {
			t.Errorf("unverified work %+v is incomplete", w)
		}
		if _, ok := libraryWork[w.field]; ok != (w.kind == KindLibrary) {
			t.Errorf("library work %q has no reader and clearer, or one without a library entry", w.field)
		}
	}
	notWork := map[string]string{"Enabled": "the library is shown", "RealtimeMonitoring": "watches its own folders", "BoxSetsInSearch": whyShown}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[LibraryEntry]()) {
		if f.Type.Kind() != reflect.Bool && f.Type != reflect.TypeFor[*bool]() {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if _, work := libraryWork[tag]; !work && notWork[f.Name] == "" {
			t.Errorf("library option %s is neither unverified work nor named as none", f.Name)
		}
	}
	for field, w := range libraryWork {
		on := LibraryEntry{}
		reflect.ValueOf(&on).Elem().FieldByNameFunc(func(name string) bool {
			f, _ := reflect.TypeFor[LibraryEntry]().FieldByName(name)
			tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			return tag == field
		}).SetBool(true)
		if !w.on(on) || len(costlyLibraryOptions(on)) != 1 {
			t.Errorf("%s on is not named: %v", field, costlyLibraryOptions(on))
		}
		in := withoutCostlyOptions(libraryInput(on))
		if len(costlyLibraryOptions(LibraryEntry{AutoTranslateMetadata: in.AutoTranslateMetadata, TrickplayEnabled: in.TrickplayEnabled,
			IntroDetectionEnabled: in.IntroDetectionEnabled, ChapterThumbnailsEnabled: in.ChapterThumbnailsEnabled, BoxSetsEnabled: in.BoxSetsEnabled})) != 0 {
			t.Errorf("%s is still on for an unverified backup", field)
		}
	}
	seasonal := HomeSectionEntry{SectionType: string(sections.SectionSeasonalThemed), Enabled: true, Config: json.RawMessage(`{}`)}
	if on, note := restoredSectionEnabled(seasonal, true, time.Now()); on || note == "" {
		t.Error("a seasonal section of an unverified backup is restored enabled")
	}
	if on, _ := restoredSectionEnabled(seasonal, false, time.Now()); !on {
		t.Error("a seasonal section of a verified backup is restored disabled")
	}
	// Every enabled row is fetched on each home page request, so every
	// section of an unverified backup comes back off too.
	other := HomeSectionEntry{SectionType: string(sections.SectionRecentlyAdded), Enabled: true}
	if on, note := restoredSectionEnabled(other, true, time.Now()); on || note == "" {
		t.Error("a section of an unverified backup is restored enabled")
	}
	if on, _ := restoredSectionEnabled(other, false, time.Now()); !on {
		t.Error("a section of a verified backup is restored disabled")
	}
}
