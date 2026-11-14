package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/secret"
)

const (
	swapStamp    = "20261031t040000z"
	liveDB       = "silo"
	restoredDB   = "silo_restore_" + swapStamp
	beforeDB     = "silo_before_restore_" + swapStamp
	replacedKey  = "library-posters/1.jpg"
	addedKey     = "library-posters/2.jpg"
	liveBytes    = "live poster"
	restoredByte = "restored poster"
)

var swapNow = time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)

// fakeDB is one database: its oid stays with it across renames.
type fakeDB struct {
	oid   uint32
	allow bool
	// sessions counts clients other than the command.
	sessions int
}

// fakeControl models pg_database closely enough to catch impossible swaps:
// OIDs that survive renames, name collisions, renames refused while other
// sessions are connected, and connection admission. crashAt ends the
// calling goroutine right after the named statement takes effect, before
// anything can record it; lostReply applies a statement and then reports an
// error, as a dropped connection would.
type fakeControl struct {
	t         *testing.T
	dbs       map[string]*fakeDB
	nextOID   uint32
	calls     []string
	fail      map[string]error
	lostReply map[string]bool
	crashAt   string
	// hang makes the named statement wait until its context ends, as a
	// statement blocked on a lock would.
	hang string
	// openAfter records, after each statement, which databases admitted
	// connections.
	openAfter [][]string
	// joinOnLoad and joinOnCarry connect a client to the live database
	// while the restore loads the dump, or right after it carries the
	// credentials, as a server started meanwhile would.
	joinOnLoad, joinOnCarry bool
	// liveServerID is the live server's identity; "" is the manifest's.
	// carriedFrom records the live URL each credential carry read from.
	liveServerID string
	// superuser is what Superuser reports for every database.
	superuser   bool
	carriedFrom []string
	// checkedExtensions are what CheckExtensions was asked about, and
	// extensionsErr its answer.
	checkedExtensions []string
	extensionsErr     error
	// restoreLocks is the restore lock table, shared by every control of
	// one server; held names the databases locked.
	restoreLocks *fakeRestoreLocks
}

type fakeRestoreLocks struct {
	mu   sync.Mutex
	held map[string]bool
}

func (f *fakeControl) LockRestore(_ context.Context, live string) (func(context.Context), error) {
	locks := f.restoreLocks
	locks.mu.Lock()
	defer locks.mu.Unlock()
	if locks.held[live] {
		return nil, ErrRestoreRunning
	}
	locks.held[live] = true
	return func(context.Context) {
		locks.mu.Lock()
		defer locks.mu.Unlock()
		delete(locks.held, live)
	}, nil
}

func newFakeControl(t *testing.T) *fakeControl {
	return &fakeControl{t: t, dbs: map[string]*fakeDB{liveDB: {oid: 100, allow: true}}, nextOID: 200, fail: map[string]error{}, lostReply: map[string]bool{},
		restoreLocks: &fakeRestoreLocks{held: map[string]bool{}}}
}

func (f *fakeControl) do(call string, apply func() error) error {
	f.calls = append(f.calls, call)
	err := f.fail[call]
	if err == nil {
		err = apply()
	}
	var open []string
	for name, db := range f.dbs {
		if db.allow {
			open = append(open, name)
		}
	}
	slices.Sort(open)
	f.openAfter = append(f.openAfter, open)
	if err != nil {
		return err
	}
	if call == f.crashAt {
		f.crashAt = ""
		runtime.Goexit()
	}
	if f.lostReply[call] {
		return errors.New("connection lost before the reply")
	}
	return nil
}

func (f *fakeControl) OID(_ context.Context, name string) (uint32, bool, error) {
	if db := f.dbs[name]; db != nil {
		return db.oid, true, nil
	}
	return 0, false, nil
}

func (f *fakeControl) NameOf(_ context.Context, oid uint32) (string, bool, error) {
	for name, db := range f.dbs {
		if db.oid == oid {
			return name, true, nil
		}
	}
	return "", false, nil
}

func (f *fakeControl) OtherConnections(_ context.Context, name string) (int, error) {
	if db := f.dbs[name]; db != nil {
		return db.sessions, nil
	}
	return 0, nil
}
func (f *fakeControl) CheckPrivileges(context.Context, string) error { return nil }
func (f *fakeControl) CheckExtensions(_ context.Context, names []string) error {
	f.checkedExtensions = names
	return f.extensionsErr
}

func (f *fakeControl) Create(_ context.Context, name string) error {
	return f.do("create "+name, func() error {
		if f.dbs[name] != nil {
			return fmt.Errorf("database %q already exists", name)
		}
		f.nextOID++
		f.dbs[name] = &fakeDB{oid: f.nextOID, allow: true}
		return nil
	})
}

func (f *fakeControl) Drop(ctx context.Context, name string) error {
	if f.hang == "drop "+name {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.do("drop "+name, func() error {
		delete(f.dbs, name) // WITH (FORCE) ends its sessions
		return nil
	})
}

func (f *fakeControl) Rename(_ context.Context, from, to string) error {
	return f.do("rename "+from+" "+to, func() error {
		db := f.dbs[from]
		switch {
		case db == nil:
			return fmt.Errorf("database %q does not exist", from)
		case f.dbs[to] != nil:
			return fmt.Errorf("database %q already exists", to)
		case db.sessions > 0:
			return fmt.Errorf("database %q is being accessed by other users", from)
		}
		delete(f.dbs, from)
		f.dbs[to] = db
		return nil
	})
}

func (f *fakeControl) AllowConnections(_ context.Context, name string, allow bool) error {
	return f.do(fmt.Sprintf("allow %s %t", name, allow), func() error {
		db := f.dbs[name]
		if db == nil {
			return fmt.Errorf("database %q does not exist", name)
		}
		db.allow = allow
		return nil
	})
}

func (f *fakeControl) Terminate(_ context.Context, name string) error {
	return f.do("terminate "+name, func() error {
		if db := f.dbs[name]; db != nil {
			db.sessions = 0
		}
		return nil
	})
}

// connect is a client connecting to name; it fails while name is closed.
func (f *fakeControl) connect(name string) error {
	db := f.dbs[name]
	if db == nil || !db.allow {
		return fmt.Errorf("database %q does not accept connections", name)
	}
	return nil
}

// fakeLoader restores nothing; it connects the way pg_restore and the
// verification do and reports the manifest's inventory.
type fakeLoader struct {
	ctl *fakeControl
	m   *Manifest
	// applied, when set, is what the restored database reports instead of
	// the manifest's applied migrations.
	applied []int64
}

func (l fakeLoader) Empty(context.Context, string) (bool, error) { return true, nil }
func (l fakeLoader) UnbindStorage(context.Context, string) error { return nil }
func (l fakeLoader) ClearUnreadableSettings(context.Context, string, *secret.Cipher) ([]string, error) {
	return nil, nil
}
func (l fakeLoader) DropSubtitles(context.Context, string, func(context.Context, string) (bool, error)) (int, error) {
	return 0, nil
}
func (l fakeLoader) RotateSigningSecrets(context.Context, string, *secret.Cipher) (bool, error) {
	return true, nil
}
func (l fakeLoader) KeepStricterSettings(context.Context, string, string, *secret.Cipher) ([]string, error) {
	return nil, nil
}
func (l fakeLoader) Extensions(context.Context, string) ([]string, error) {
	return []string{"vector"}, nil
}
func (l fakeLoader) CarryPolicies(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (l fakeLoader) CarryPluginReleases(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (l fakeLoader) DisablePlugins(context.Context, string) ([]string, error) {
	return nil, nil
}
func (l fakeLoader) CarryServerSpecificSettings(context.Context, string, string) (int, error) {
	return 0, nil
}
func (l fakeLoader) CarryOutboundSettings(context.Context, string, string) ([]string, []string, error) {
	return nil, nil, nil
}
func (l fakeLoader) InterruptWork(context.Context, string, string) ([]string, error) { return nil, nil }
func (l fakeLoader) Superuser(context.Context, string) (bool, error)                 { return l.ctl.superuser, nil }
func (l fakeLoader) AccountRollback(context.Context, string, string) (AccountRollback, error) {
	return AccountRollback{}, nil
}
func (l fakeLoader) ServerID(context.Context, string) (string, error) {
	if l.ctl.liveServerID != "" {
		return l.ctl.liveServerID, nil
	}
	return l.m.ServerID, nil
}
func (l fakeLoader) CarryCredentials(_ context.Context, _, liveURL string) (CarriedCredentials, error) {
	l.ctl.carriedFrom = append(l.ctl.carriedFrom, liveURL)
	if l.ctl.joinOnCarry {
		l.ctl.dbs[liveDB].sessions++
	}
	return CarriedCredentials{}, nil
}
func (l fakeLoader) Load(_ context.Context, dbURL, _ string) error {
	name, err := databaseName(dbURL)
	if err != nil {
		return err
	}
	if l.ctl.joinOnLoad {
		l.ctl.dbs[liveDB].sessions++
	}
	return l.ctl.connect(name)
}
func (l fakeLoader) Inspect(_ context.Context, dbURL string) (Inspection, error) {
	name, err := databaseName(dbURL)
	if err != nil {
		return Inspection{}, err
	}
	if err := l.ctl.connect(name); err != nil {
		return Inspection{}, err
	}
	applied := l.m.AppliedMigrations
	if l.applied != nil {
		applied = l.applied
	}
	return Inspection{GooseVersion: l.m.GooseVersion, Applied: applied, Tables: l.m.Tables}, nil
}

// crashingStore ends the calling goroutine right after writing crashKey.
type crashingStore struct {
	blobstore.Store
	crashKey string
	failKey  string
}

func (s *crashingStore) PutStream(ctx context.Context, key string, r io.Reader, contentType string) error {
	if key == s.failKey {
		return errors.New("injected write failure")
	}
	if err := s.Store.PutStream(ctx, key, r, contentType); err != nil {
		return err
	}
	if key == s.crashKey {
		s.crashKey = ""
		runtime.Goexit()
	}
	return nil
}

type tarEntry struct{ name, data string }

// writeTestBackup writes a backup folder whose manifest matches its objects.
func writeTestBackup(t *testing.T, entries []tarEntry) *Source {
	t.Helper()
	dir := t.TempDir()
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	dump := []byte("PGDMP fake dump")
	entry := func(name string, data []byte) ObjectEntry {
		sum := sha256.Sum256(data)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return ObjectEntry{Name: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	}
	m := Manifest{Format: manifestFormat, Version: manifestVersion, ID: "20261030T040000Z-nightly", Kind: KindNightly, ServerID: "server",
		GooseVersion: 1, Objects: []ObjectEntry{entry(DumpName, dump), entry(BlobsName, archive.Bytes())},
		Blobs: BlobSummary{Count: len(entries)}}
	for _, name := range inventoryTables {
		m.Tables = append(m.Tables, TableCount{Name: name})
	}
	m.KeyCheck = testKeyCheck
	if err := m.sign(testManifestKey); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, ManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := OpenSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src
}

// TestValidateReadsOnlyCheckedCopies: what a restore consumes is the bytes
// Validate checked. Validation copies each object into a private directory
// as it hashes it, and every later read goes there, so a backup folder
// changed after the check changes nothing; one changed before fails it.
func TestValidateReadsOnlyCheckedCopies(t *testing.T) {
	src := writeTestBackup(t, []tarEntry{{name: "assets/" + addedKey, data: "added poster"}})
	origin := src.Dir
	opts := ValidateOptions{KeyCheck: testKeyCheck, ManifestKey: testManifestKey, CopyDir: t.TempDir()}
	if err := src.Validate(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if src.Dir == origin {
		t.Fatal("validation left the restore reading the backup folder")
	}
	info, err := os.Stat(src.Dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("private copy directory %v (%v), want 0700", info, err)
	}
	if err := os.WriteFile(filepath.Join(origin, DumpName), []byte("swapped after the check"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(src.path(DumpName)); err != nil || string(got) != "PGDMP fake dump" {
		t.Fatalf("the restore reads %q (%v), want the checked dump", got, err)
	}
	private := src.Dir
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(private); !os.IsNotExist(err) {
		t.Fatalf("private copies left behind: %v", err)
	}
	// Changed before the check: refused, and no copy is left.
	if err := src.Validate(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "does not match its checksum") {
		t.Fatalf("validating a changed dump = %v", err)
	}
	if entries, _ := os.ReadDir(opts.CopyDir); len(entries) != 0 {
		t.Fatalf("a failed check left %d copies", len(entries))
	}
}

type swapFixture struct {
	t       *testing.T
	ctl     *fakeControl
	root    string
	live    blobstore.Store
	src     *Source
	workDir string
	cipher  *secret.Cipher
	wrap    func(blobstore.Store) blobstore.Store
	output  strings.Builder
	// validate is the restore's step 1 options; restoredApplied, when set,
	// is what the restored database reports as its applied migrations.
	validate        ValidateOptions
	restoredApplied []int64
}

func newSwapFixture(t *testing.T) *swapFixture {
	return newSwapFixtureWith(t, []tarEntry{{"assets/" + replacedKey, restoredByte}, {"assets/" + addedKey, "added poster"}})
}

func newSwapFixtureWith(t *testing.T, entries []tarEntry) *swapFixture {
	t.Helper()
	root := t.TempDir()
	live, err := blobstore.NewFilesystem(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := live.Put(t.Context(), replacedKey, []byte(liveBytes)); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("swap-test-key-0123456789abcdef0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	return &swapFixture{t: t, ctl: newFakeControl(t), root: root, live: live, src: writeTestBackup(t, entries), workDir: t.TempDir(), cipher: cipher,
		validate: ValidateOptions{KeyCheck: testKeyCheck, ManifestKey: testManifestKey}}
}

// resign signs the fixture's manifest again after a test changed it, as
// the backup that wrote it would have.
func (f *swapFixture) resign() {
	f.t.Helper()
	if err := f.src.Manifest.sign(testManifestKey); err != nil {
		f.t.Fatal(err)
	}
}

// testManifestKey and testKeyCheck are the SECRET_KEY test backups are
// written under and test restores check them with.
var testManifestKey = []byte("0123456789abcdef0123456789abcdef")

const testKeyCheck = "0000-1111-2222-3333"

func (f *swapFixture) openStores(ctx context.Context, sc StoreConfig) (blobstore.Stores, error) {
	if sc.Backend != blobstore.BackendLocal || sc.LocalPath != f.root {
		f.t.Fatalf("stores opened from %+v, not the recorded configuration", sc)
	}
	stores, _, err := blobstore.Open(ctx, blobstore.Options{Backend: sc.Backend, LocalPath: sc.LocalPath})
	if err != nil || f.wrap == nil {
		return stores, err
	}
	wrapped := f.wrap(stores.Assets)
	return blobstore.Stores{Assets: wrapped, Operational: wrapped}, nil
}

// restore runs RestoreSwap in its own goroutine, so a simulated crash ends
// it the way a dead process would. crashed reports that it did not return.
func (f *swapFixture) restore() (err error, crashed bool) {
	f.t.Helper()
	done := make(chan error, 1)
	returned := false
	go func() {
		defer close(done)
		e := RestoreSwap(f.t.Context(), f.src, fakeLoader{ctl: f.ctl, m: f.src.Manifest, applied: f.restoredApplied}, f.ctl, SwapOptions{
			Validate:    f.validate,
			LiveURL:     "postgres://silo@db.invalid/" + liveDB,
			StoreConfig: StoreConfig{Backend: blobstore.BackendLocal, LocalPath: f.root},
			OpenStores:  f.openStores, Cipher: f.cipher, JournalKey: testJournalKey,
			RestoredSettings: func(context.Context, string) (map[string]string, error) { return map[string]string{}, nil },
			WorkDir:          f.workDir, Now: func() time.Time { return swapNow }, Out: &f.output,
		})
		returned = true
		done <- e
	}()
	err = await(f.t, done, "the restore to return or stop")
	return err, !returned
}

func (f *swapFixture) recover() (err error, crashed bool) {
	f.t.Helper()
	done := make(chan error, 1)
	returned := false
	go func() {
		defer close(done)
		e := RecoverSwap(f.t.Context(), JournalPath(f.workDir, swapStamp), f.ctl, f.cipher, testJournalKey, "postgres://silo@db.invalid/"+liveDB, f.openStores, &f.output)
		returned = true
		done <- e
	}()
	err = await(f.t, done, "the recovery to return or stop")
	return err, !returned
}

func (f *swapFixture) blob(key string) string {
	f.t.Helper()
	body, _, err := f.live.Get(f.t.Context(), key)
	if errors.Is(err, blobstore.ErrNotFound) {
		return "<absent>"
	}
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	data, _ := io.ReadAll(body)
	return string(data)
}

// wantRolledBack: the original database has the live name and is open, the
// restored one is gone, and the live files are as they were.
func (f *swapFixture) wantRolledBack() {
	f.t.Helper()
	db := f.ctl.dbs[liveDB]
	if db == nil || db.oid != 100 || !db.allow || f.ctl.dbs[restoredDB] != nil || f.ctl.dbs[beforeDB] != nil {
		f.t.Fatalf("databases after rollback: %+v\n%s", f.ctl.dbs, f.output.String())
	}
	if got := f.blob(replacedKey); got != liveBytes {
		f.t.Fatalf("replaced key after rollback = %q, want the live bytes back", got)
	}
	if got := f.blob(addedKey); got != "<absent>" {
		f.t.Fatalf("added key after rollback = %q, want it deleted", got)
	}
}

// wantSwapped: the restored database has the live name and is open, the
// original is kept closed, and the files are the backup's.
func (f *swapFixture) wantSwapped() {
	f.t.Helper()
	live, before := f.ctl.dbs[liveDB], f.ctl.dbs[beforeDB]
	if live == nil || live.oid == 100 || !live.allow || before == nil || before.oid != 100 || before.allow {
		f.t.Fatalf("databases after the swap: %+v\n%s", f.ctl.dbs, f.output.String())
	}
	if f.blob(replacedKey) != restoredByte || f.blob(addedKey) != "added poster" {
		f.t.Fatal("the restored files are not in place")
	}
	if f.blob("backups/server/"+swapStamp+"-overwritten-blobs/"+replacedKey) != liveBytes {
		f.t.Fatal("the replaced bytes were not preserved")
	}
}

// wantClosedThroughout: once the live database closes, no database admits a
// connection until the final reopen.
func (f *swapFixture) wantClosedThroughout() {
	f.t.Helper()
	closed := slices.Index(f.ctl.calls, "allow "+liveDB+" false")
	if closed < 0 {
		return
	}
	reopened := closed + slices.Index(f.ctl.calls[closed:], "allow "+liveDB+" true")
	if reopened < closed {
		reopened = len(f.ctl.calls)
	}
	for i := closed; i < reopened; i++ {
		if len(f.ctl.openAfter[i]) != 0 {
			f.t.Fatalf("after %q these databases admitted connections: %v", f.ctl.calls[i], f.ctl.openAfter[i])
		}
	}
}

func TestSwapCompletes(t *testing.T) {
	f := newSwapFixture(t)
	if err, _ := f.restore(); err != nil {
		t.Fatalf("%v\n%s", err, f.output.String())
	}
	want := []string{"create " + restoredDB, "allow " + restoredDB + " false", "terminate " + restoredDB,
		"allow silo false", "terminate silo", "rename silo " + beforeDB, "rename " + restoredDB + " silo", "allow silo true"}
	if !slices.Equal(f.ctl.calls, want) {
		t.Fatalf("calls = %v\nwant    %v", f.ctl.calls, want)
	}
	f.wantSwapped()
	f.wantClosedThroughout()
}

// TestSwapRollsBackInOrder: a failure at each point after the live database
// closes is rolled back — the rename undone, files put back, the restored
// database dropped — with the original reopened last.
func TestSwapRollsBackInOrder(t *testing.T) {
	tail := []string{"drop " + restoredDB, "allow silo true"}
	quiesce := []string{"create " + restoredDB, "allow " + restoredDB + " false", "terminate " + restoredDB, "allow silo false", "terminate silo"}
	tests := []struct {
		name, fail, failBlob string
		want                 []string
	}{
		{name: "blob write fails", failBlob: addedKey, want: slices.Concat(quiesce, tail)},
		{name: "first rename fails", fail: "rename silo " + beforeDB,
			want: slices.Concat(quiesce, []string{"rename silo " + beforeDB}, tail)},
		{name: "second rename fails", fail: "rename " + restoredDB + " silo",
			want: slices.Concat(quiesce, []string{"rename silo " + beforeDB, "rename " + restoredDB + " silo", "rename " + beforeDB + " silo"}, tail)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSwapFixture(t)
			if tt.fail != "" {
				f.ctl.fail[tt.fail] = errors.New("injected")
			}
			if tt.failBlob != "" {
				f.wrap = func(s blobstore.Store) blobstore.Store { return &crashingStore{Store: s, failKey: tt.failBlob} }
			}
			err, _ := f.restore()
			if err == nil || errors.Is(err, ErrRollbackIncomplete) {
				t.Fatalf("restore = %v, want the original failure after a complete rollback\n%s", err, f.output.String())
			}
			if !slices.Equal(f.ctl.calls, tt.want) {
				t.Fatalf("calls = %v\nwant    %v", f.ctl.calls, tt.want)
			}
			f.wantRolledBack()
			f.wantClosedThroughout()
		})
	}
}

// TestSwapTrustsTheDatabasesOverStatementReplies: a rename that took effect
// but lost its reply is read back by OID and the swap carries on.
func TestSwapTrustsTheDatabasesOverStatementReplies(t *testing.T) {
	for _, call := range []string{"rename silo " + beforeDB, "rename " + restoredDB + " silo"} {
		t.Run(call, func(t *testing.T) {
			f := newSwapFixture(t)
			f.ctl.lostReply[call] = true
			if err, _ := f.restore(); err != nil {
				t.Fatalf("%v\n%s", err, f.output.String())
			}
			f.wantSwapped()
		})
	}
}

// TestSwapRecoversFromACrashAtEveryStep: the process dies right after each
// statement or file write takes effect, before the journal records it.
// --recover reads the databases by OID and ends in one consistent state:
// fully swapped once the second rename landed, fully rolled back before.
func TestSwapRecoversFromACrashAtEveryStep(t *testing.T) {
	tests := []struct {
		crashAt, crashBlob string
		swapped            bool
	}{
		{crashAt: "allow silo false"},
		{crashAt: "terminate silo"},
		{crashBlob: replacedKey},
		{crashBlob: addedKey},
		{crashAt: "rename silo " + beforeDB},
		{crashAt: "rename " + restoredDB + " silo", swapped: true},
		{crashAt: "allow silo true", swapped: true},
	}
	for _, tt := range tests {
		t.Run(tt.crashAt+tt.crashBlob, func(t *testing.T) {
			f := newSwapFixture(t)
			f.ctl.crashAt = tt.crashAt
			if tt.crashBlob != "" {
				f.wrap = func(s blobstore.Store) blobstore.Store { return &crashingStore{Store: s, crashKey: tt.crashBlob} }
			}
			if _, crashed := f.restore(); !crashed {
				t.Fatalf("the restore did not reach the crash point\n%s", f.output.String())
			}
			f.wrap = nil
			if err, crashed := f.recover(); err != nil || crashed {
				t.Fatalf("recover = %v (crashed %v)\n%s", err, crashed, f.output.String())
			}
			if tt.swapped {
				f.wantSwapped()
			} else {
				f.wantRolledBack()
			}
			f.wantClosedThroughout()
		})
	}
}

// TestSwapRecoveryCanBeInterruptedAgain: a recovery that dies partway, and
// then one that dies at its next step, still end rolled back.
func TestSwapRecoveryCanBeInterruptedAgain(t *testing.T) {
	f := newSwapFixture(t)
	f.ctl.crashAt = "rename silo " + beforeDB
	if _, crashed := f.restore(); !crashed {
		t.Fatal("the restore did not crash")
	}
	for _, crashAt := range []string{"rename " + beforeDB + " silo", "drop " + restoredDB} {
		f.ctl.crashAt = crashAt
		if _, crashed := f.recover(); !crashed {
			t.Fatalf("recovery did not reach %q\n%s", crashAt, f.output.String())
		}
	}
	if err, crashed := f.recover(); err != nil || crashed {
		t.Fatalf("final recover = %v (crashed %v)\n%s", err, crashed, f.output.String())
	}
	f.wantRolledBack()
	f.wantClosedThroughout()
}

// TestSwapRollbackStopsClosed: a rollback step that fails leaves every
// database closed and the journal for --recover.
func TestSwapRollbackStopsClosed(t *testing.T) {
	f := newSwapFixture(t)
	f.ctl.fail["rename "+restoredDB+" silo"] = errors.New("injected")
	f.ctl.fail["drop "+restoredDB] = errors.New("drop refused")
	err, _ := f.restore()
	if !errors.Is(err, ErrRollbackIncomplete) {
		t.Fatalf("restore = %v, want ErrRollbackIncomplete", err)
	}
	for name, db := range f.ctl.dbs {
		if db.allow {
			t.Fatalf("%s admits connections after a stopped rollback", name)
		}
	}
	delete(f.ctl.fail, "drop "+restoredDB)
	if err, _ := f.recover(); err != nil {
		t.Fatalf("recover: %v\n%s", err, f.output.String())
	}
	f.wantRolledBack()
}

// TestSwapCleanupHasADeadline: a rollback statement that never returns
// stops the cleanup at its deadline, with the databases closed and the
// journal and --recover command printed, instead of hanging; once the
// statement works again, --recover finishes the rollback.
func TestSwapCleanupHasADeadline(t *testing.T) {
	previous := swapCleanupTimeout
	swapCleanupTimeout = 200 * time.Millisecond
	t.Cleanup(func() { swapCleanupTimeout = previous })
	f := newSwapFixture(t)
	f.ctl.fail["rename "+restoredDB+" silo"] = errors.New("injected")
	f.ctl.hang = "drop " + restoredDB
	err, _ := f.restore()
	if !errors.Is(err, ErrRollbackIncomplete) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("restore = %v, want ErrRollbackIncomplete at the cleanup deadline", err)
	}
	out := f.output.String()
	for _, want := range []string{JournalPath(f.workDir, swapStamp), "silo backup restore --recover " + swapStamp} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	for name, db := range f.ctl.dbs {
		if db.allow {
			t.Fatalf("%s admits connections after a timed-out rollback", name)
		}
	}
	f.ctl.hang = ""
	if err, _ := f.recover(); err != nil {
		t.Fatalf("recover: %v\n%s", err, f.output.String())
	}
	f.wantRolledBack()
}

// TestSwapPreflightChangesNothing: refusals before the restore starts leave
// every database and file untouched.
func TestSwapPreflightChangesNothing(t *testing.T) {
	t.Run("other clients connected", func(t *testing.T) {
		f := newSwapFixture(t)
		f.ctl.dbs[liveDB].sessions = 2
		if err, _ := f.restore(); err == nil || len(f.ctl.calls) != 0 {
			t.Fatalf("restore = %v, calls %v", err, f.ctl.calls)
		}
	})
	t.Run("a database already has the name the swap needs", func(t *testing.T) {
		f := newSwapFixture(t)
		f.ctl.dbs[beforeDB] = &fakeDB{oid: 7}
		if err, _ := f.restore(); err == nil || len(f.ctl.calls) != 0 {
			t.Fatalf("restore = %v, calls %v", err, f.ctl.calls)
		}
	})
}

// TestSwapWaitsForNoOtherRestore: a full restore or a recovery of a
// database another one holds is refused before it changes anything, and
// the lock is free again once a restore returns.
func TestSwapWaitsForNoOtherRestore(t *testing.T) {
	f := newSwapFixture(t)
	unlock, err := f.ctl.LockRestore(t.Context(), liveDB)
	if err != nil {
		t.Fatal(err)
	}
	if err, _ := f.restore(); !errors.Is(err, ErrRestoreRunning) || len(f.ctl.calls) != 0 {
		t.Fatalf("restore beside another = %v, calls %v", err, f.ctl.calls)
	}
	unlock(t.Context())
	if err, _ := f.restore(); err != nil {
		t.Fatalf("%v\n%s", err, f.output.String())
	}
	if again, err := f.ctl.LockRestore(t.Context(), liveDB); err != nil {
		t.Fatalf("the restore kept its lock: %v", err)
	} else {
		again(t.Context())
	}

	// A recovery takes the same lock: one beside another changes nothing.
	f = newSwapFixture(t)
	f.ctl.crashAt = "rename silo " + beforeDB
	if _, crashed := f.restore(); !crashed {
		t.Fatal("the restore did not crash")
	}
	f.ctl.crashAt = ""
	unlock, err = f.ctl.LockRestore(t.Context(), liveDB)
	if err != nil {
		t.Fatal(err)
	}
	calls := len(f.ctl.calls)
	if err, _ := f.recover(); !errors.Is(err, ErrRestoreRunning) || len(f.ctl.calls) != calls {
		t.Fatalf("recovery beside another = %v, calls %v", err, f.ctl.calls[calls:])
	}
	unlock(t.Context())
	if err, crashed := f.recover(); err != nil || crashed {
		t.Fatalf("recover = %v (crashed %v)\n%s", err, crashed, f.output.String())
	}
	f.wantRolledBack()
}

// TestSwapCarriesOnlyForTheSameDeployment: the live server's credentials,
// switches, and destinations are tied to its ids, so a restore carries them
// only from the deployment the backup's manifest names. A backup from
// another needs --accept-lost-credentials, refused before anything changes
// without it, and then carries none.
func TestSwapCarriesOnlyForTheSameDeployment(t *testing.T) {
	f := newSwapFixture(t)
	if err, _ := f.restore(); err != nil {
		t.Fatalf("%v\n%s", err, f.output.String())
	}
	if !slices.Equal(f.ctl.carriedFrom, []string{"postgres://silo@db.invalid/" + liveDB}) {
		t.Fatalf("the same deployment carried from %q, want the live database", f.ctl.carriedFrom)
	}

	f = newSwapFixture(t)
	f.ctl.liveServerID = "another-deployment"
	if err, _ := f.restore(); !errors.Is(err, ErrOtherDeployment) || len(f.ctl.calls) != 0 {
		t.Fatalf("another deployment's backup = %v, calls %v; want ErrOtherDeployment before any change", err, f.ctl.calls)
	}
	f.validate.AcceptLostCredentials = true
	if err, _ := f.restore(); err != nil {
		t.Fatalf("%v\n%s", err, f.output.String())
	}
	if !slices.Equal(f.ctl.carriedFrom, []string{""}) || !strings.Contains(f.output.String(), "is not this deployment's") {
		t.Fatalf("another deployment carried from %q, want nothing; output:\n%s", f.ctl.carriedFrom, f.output.String())
	}
}

// TestSwapRefusesExtensionsTheRoleCannotCreate: the restored database is
// new, so a role that cannot create an extension the backup needs is
// refused in preflight, before anything changes.
func TestSwapRefusesExtensionsTheRoleCannotCreate(t *testing.T) {
	f := newSwapFixture(t)
	f.ctl.extensionsErr = ErrExtensionNeedsSuperuser
	if err, _ := f.restore(); !errors.Is(err, ErrExtensionNeedsSuperuser) || slices.ContainsFunc(f.ctl.calls, func(c string) bool { return strings.HasPrefix(c, "create ") }) {
		t.Fatalf("restore = %v, calls %v; want ErrExtensionNeedsSuperuser before any database is created", err, f.ctl.calls)
	}
	if !slices.Equal(f.ctl.checkedExtensions, []string{"vector"}) {
		t.Fatalf("checked extensions %v, want the dump's", f.ctl.checkedExtensions)
	}
}

// TestUnverifiedDumpsAreNotLoadedAsASuperuser: a backup this key cannot
// authenticate could have been changed, and pg_restore runs what its dump
// says with the connecting role's rights. Restored or verified, it needs
// --accept-lost-credentials, is loaded with a warning, and is refused for a
// superuser role before anything changes unless the operator passes
// --accept-superuser. This deployment's own backup loads as any role.
func TestUnverifiedDumpsAreNotLoadedAsASuperuser(t *testing.T) {
	unverified := func(f *swapFixture) {
		f.validate = ValidateOptions{KeyCheck: testKeyCheck}
		f.ctl.superuser = true
	}
	f := newSwapFixture(t)
	f.ctl.superuser = true
	if err, _ := f.restore(); err != nil {
		t.Fatalf("this deployment's backup as a superuser: %v\n%s", err, f.output.String())
	}

	f = newSwapFixture(t)
	unverified(f)
	if err, _ := f.restore(); !errors.Is(err, ErrUnauthenticatedBackup) || len(f.ctl.calls) != 0 {
		t.Fatalf("unverified backup = %v, calls %v; want ErrUnauthenticatedBackup before any change", err, f.ctl.calls)
	}
	f.validate.AcceptLostCredentials = true
	if err, _ := f.restore(); !errors.Is(err, ErrSuperuserLoad) || len(f.ctl.calls) != 0 || !strings.Contains(f.output.String(), msgUnverifiedLoad) {
		t.Fatalf("accepted unverified backup as a superuser = %v, calls %v; want ErrSuperuserLoad before any change\n%s", err, f.ctl.calls, f.output.String())
	}
	f.validate.AcceptSuperuser = true
	if err, _ := f.restore(); err != nil {
		t.Fatalf("with --accept-superuser: %v\n%s", err, f.output.String())
	}

	f = newSwapFixture(t)
	unverified(f)
	loader := fakeLoader{ctl: f.ctl, m: f.src.Manifest}
	f.ctl.dbs["scratch"] = &fakeDB{oid: 300, allow: true}
	scratch := "postgres://silo@db.invalid/scratch"
	if err := VerifyBackup(t.Context(), f.src, loader, scratch, f.validate, io.Discard); !errors.Is(err, ErrUnauthenticatedBackup) {
		t.Fatalf("verify of an unverified backup = %v, want ErrUnauthenticatedBackup", err)
	}
	f.validate.AcceptLostCredentials = true
	if err := VerifyBackup(t.Context(), f.src, loader, scratch, f.validate, io.Discard); !errors.Is(err, ErrSuperuserLoad) || len(f.ctl.calls) != 0 {
		t.Fatalf("verify as a superuser = %v, calls %v; want ErrSuperuserLoad before loading", err, f.ctl.calls)
	}
	f.ctl.superuser = false
	if err := VerifyBackup(t.Context(), f.src, loader, scratch, f.validate, io.Discard); err != nil {
		t.Fatalf("verify as an ordinary role: %v", err)
	}
}

// TestSwapStopsWhenAClientJoinsTheLiveDatabase: the credentials a restore
// carries are the live server's only while nothing else is on the live
// database. A client that connects after preflight, before or after the
// carry, stops the restore with the live database open and unchanged,
// rather than having its session ended and its changes lost.
func TestSwapStopsWhenAClientJoinsTheLiveDatabase(t *testing.T) {
	for name, join := range map[string]func(*fakeControl){
		"while the dump loads":  func(c *fakeControl) { c.joinOnLoad = true },
		"after the credentials": func(c *fakeControl) { c.joinOnCarry = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSwapFixture(t)
			join(f.ctl)
			err, _ := f.restore()
			if !errors.Is(err, ErrLiveInUse) {
				t.Fatalf("restore = %v, want ErrLiveInUse\n%s", err, f.output.String())
			}
			if slices.Contains(f.ctl.calls, "terminate "+liveDB) {
				t.Fatalf("the swap ended the client's session: %v", f.ctl.calls)
			}
			f.wantRolledBack()
		})
	}
}

// TestSwapRefusesUnsafeArchives: blobs.tar is checked whole before anything
// changes. Two entries for one object would preserve the first restored
// bytes over the original, so the rollback could not put it back; keys
// outside the backup namespaces could overwrite backups/ or the journal.
func TestSwapRefusesUnsafeArchives(t *testing.T) {
	tests := map[string][]tarEntry{
		"duplicate key":  {{"assets/" + replacedKey, "one"}, {"assets/" + replacedKey, "two"}},
		"backups prefix": {{"assets/backups/server/" + swapStamp + "-overwritten-blobs/journal.json", "{}"}},
		"unknown store":  {{"elsewhere/" + replacedKey, "x"}},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			f := newSwapFixtureWith(t, entries)
			if err, _ := f.restore(); err == nil {
				t.Fatal("the restore accepted the archive")
			}
			if len(f.ctl.calls) != 0 || f.blob(replacedKey) != liveBytes {
				t.Fatalf("a refused archive changed something: calls %v, %s = %q", f.ctl.calls, replacedKey, f.blob(replacedKey))
			}
		})
	}
}

// TestSwapStopsWhenTheJournalCannotBeMadeDurable: if the journal's directory
// cannot be synced, a host crash could lose the journal's name after
// databases were renamed or files overwritten. The swap stops before the live
// database closes, drops its restored copy, and touches no file.
func TestSwapStopsWhenTheJournalCannotBeMadeDurable(t *testing.T) {
	f := newSwapFixture(t)
	original := syncDir
	syncDir = func(string) error { return errors.New("injected directory sync failure") }
	t.Cleanup(func() { syncDir = original })
	err, _ := f.restore()
	if err == nil || !strings.Contains(err.Error(), "durable") {
		t.Fatalf("restore = %v, want the durability failure", err)
	}
	if slices.Contains(f.ctl.calls, "allow silo false") {
		t.Fatalf("the live database was closed: %v", f.ctl.calls)
	}
	if f.ctl.dbs[restoredDB] != nil || !f.ctl.dbs[liveDB].allow || f.blob(replacedKey) != liveBytes || f.blob(addedKey) != "<absent>" {
		t.Fatalf("a refused swap changed something: %v", f.ctl.calls)
	}
	if _, err := os.Stat(JournalPath(f.workDir, swapStamp)); !os.IsNotExist(err) {
		t.Fatalf("an undurable journal was left behind: %v", err)
	}
}

// TestSwapRefusesMigrationsThisBinaryDoesNotCarry: migrations run out of
// order, so a backup can hold a version older than this binary's newest that
// the binary does not carry. A manifest that records its applied versions is
// refused before anything changes; one from before they were recorded falls
// back to the newest-version check, and the restored database is checked
// before the swap goes ahead.
func TestSwapRefusesMigrationsThisBinaryDoesNotCarry(t *testing.T) {
	binary := ValidateOptions{BinaryGooseVersion: 9, BinaryMigrations: []int64{1, 9}, KeyCheck: testKeyCheck, ManifestKey: testManifestKey}
	tests := []struct {
		name     string
		goose    int64
		recorded []int64 // the manifest's applied migrations; nil is a legacy manifest
		restored []int64 // what the restored database reports
		want     error
		wantText string
		touched  bool // the restore created its database before refusing
	}{
		{name: "recorded and carried", goose: 9, recorded: []int64{1, 9}, restored: []int64{1, 9}},
		{name: "recorded version this binary lacks", goose: 9, recorded: []int64{1, 5, 9}, restored: []int64{1, 5, 9},
			want: ErrUnknownMigrations, wantText: "records 5;"},
		{name: "legacy manifest, newer schema", goose: 10, restored: []int64{1, 10}, want: ErrNewerSchema},
		{name: "legacy manifest, carried", goose: 9, restored: []int64{1, 9}},
		{name: "legacy manifest, restored version this binary lacks", goose: 9, restored: []int64{1, 5, 9},
			want: ErrUnknownMigrations, wantText: "has 5;", touched: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSwapFixture(t)
			f.validate = binary
			f.src.Manifest.GooseVersion, f.src.Manifest.AppliedMigrations = tt.goose, tt.recorded
			f.resign()
			f.restoredApplied = tt.restored
			err, _ := f.restore()
			if tt.want == nil {
				if err != nil {
					t.Fatalf("restore = %v", err)
				}
				f.wantSwapped()
				return
			}
			if !errors.Is(err, tt.want) || !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("restore = %v, want %v naming %q", err, tt.want, tt.wantText)
			}
			if touched := len(f.ctl.calls) > 0; touched != tt.touched {
				t.Fatalf("restore made calls %v before refusing", f.ctl.calls)
			}
			if f.ctl.dbs[restoredDB] != nil || !f.ctl.dbs[liveDB].allow || f.blob(replacedKey) != liveBytes {
				t.Fatalf("a refused restore changed something: %v", f.ctl.calls)
			}
		})
	}
}

// TestAuthorityWiderThan: a live credential is carried only when its
// account has no more authority in the restored data than live, by role,
// sign-in, account policy, or a restriction of a profile both sides have.
func TestAuthorityWiderThan(t *testing.T) {
	age := func(n int) *int { return &n }
	live := accountAuthority{rank: 0, enabled: true,
		policy: access.EffectiveUserPolicy{LibraryIDs: []int{1}},
		profiles: map[string]profileLimits{"kid": {child: true, ceiling: "PG", advisoryAge: age(10), requireAdvisoryAge: true,
			restricted: true, libraries: []int{1}}}}
	cases := []struct {
		name   string
		change func(*accountAuthority)
		wider  bool
	}{
		{"same", func(*accountAuthority) {}, false},
		{"admin", func(a *accountAuthority) { a.rank = 1 }, true},
		{"break glass", func(a *accountAuthority) { a.breakGlass = true }, true},
		{"every library", func(a *accountAuthority) { a.policy.LibraryIDs = nil }, true},
		{"a profile only the backup has", func(a *accountAuthority) { a.profiles["old"] = profileLimits{} }, true},
		{"not a child", func(a *accountAuthority) {
			a.profiles["kid"] = with(a.profiles["kid"], func(p *profileLimits) { p.child = false })
		}, true},
		{"higher rating ceiling", func(a *accountAuthority) {
			a.profiles["kid"] = with(a.profiles["kid"], func(p *profileLimits) { p.ceiling = "R" })
		}, true},
		{"lower rating ceiling", func(a *accountAuthority) {
			a.profiles["kid"] = with(a.profiles["kid"], func(p *profileLimits) { p.ceiling = "G" })
		}, false},
		{"no advisory limit", func(a *accountAuthority) {
			a.profiles["kid"] = with(a.profiles["kid"], func(p *profileLimits) { p.advisoryAge = nil })
		}, true},
		{"another library", func(a *accountAuthority) {
			a.profiles["kid"] = with(a.profiles["kid"], func(p *profileLimits) { p.libraries = []int{1, 2} })
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restored := live
			restored.profiles = maps.Clone(live.profiles)
			tc.change(&restored)
			if got := restored.widerThan(live); got != tc.wider {
				t.Fatalf("widerThan = %v, want %v", got, tc.wider)
			}
		})
	}
}

func with(p profileLimits, change func(*profileLimits)) profileLimits {
	change(&p)
	return p
}

// TestSensitiveSettingsAreClassified: every sensitive setting is either a
// signing key a restore replaces or a credential it restores, so a new
// signing key cannot come back from a backup without someone deciding so.
func TestSensitiveSettingsAreClassified(t *testing.T) {
	restored := map[string]bool{}
	for _, key := range restoredSecretSettings {
		restored[key] = true
		if _, rotated := rotatedSettings[key]; rotated {
			t.Errorf("%s is both rotated and restored", key)
		}
	}
	for key := range catalog.SensitiveSettingKeys {
		if _, rotated := rotatedSettings[key]; !rotated && !restored[key] {
			t.Errorf("sensitive setting %s is neither in rotatedSettings nor in restoredSecretSettings", key)
		}
	}
	for _, key := range append(rotatedSettingKeys(), restoredSecretSettings...) {
		if !catalog.SensitiveSettingKeys[key] {
			t.Errorf("%s is classified but is not a sensitive setting", key)
		}
	}
}

// TestValidateAuthenticatesTheManifest: a full restore trusts a manifest
// only when it verifies under this server's SECRET_KEY. One naming this
// key that does not verify is refused even when the operator accepts lost
// credentials; one that cannot be checked (no key check value, another
// key, or no SECRET_KEY here) is refused unless the operator passes
// --accept-lost-credentials.
func TestValidateAuthenticatesTheManifest(t *testing.T) {
	tests := []struct {
		name      string
		change    func(*Manifest)
		keyCheck  string
		key       []byte
		want      error // without --accept-lost-credentials
		wantAfter error // with it
	}{
		{name: "matching", change: func(*Manifest) {}},
		{name: "wrong MAC", change: func(m *Manifest) { m.MAC = strings.Repeat("ab", 32) }, want: ErrManifestTampered, wantAfter: ErrManifestTampered},
		{name: "missing MAC", change: func(m *Manifest) { m.MAC = "" }, want: ErrManifestTampered, wantAfter: ErrManifestTampered},
		{name: "changed checksum", change: func(m *Manifest) { m.Objects[0].SHA256 = strings.Repeat("0", 64) }, want: ErrManifestTampered, wantAfter: ErrManifestTampered},
		{name: "missing key check", change: func(m *Manifest) { m.KeyCheck = "" }, want: ErrUnauthenticatedBackup},
		{name: "other key", change: func(m *Manifest) { m.KeyCheck = "9999-8888-7777-6666" }, want: ErrKeyMismatch},
		{name: "no SECRET_KEY here", change: func(*Manifest) {}, keyCheck: "-", want: ErrUnauthenticatedBackup},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := writeTestBackup(t, nil)
			tt.change(src.Manifest)
			opts := ValidateOptions{KeyCheck: testKeyCheck, ManifestKey: testManifestKey}
			if tt.keyCheck == "-" {
				opts = ValidateOptions{}
			}
			if err := src.Validate(t.Context(), opts); !errors.Is(err, tt.want) || (tt.want == nil) != (err == nil) {
				t.Fatalf("Validate = %v, want %v", err, tt.want)
			}
			opts.AcceptLostCredentials = true
			if err := src.Validate(t.Context(), opts); !errors.Is(err, tt.wantAfter) || (tt.wantAfter == nil) != (err == nil) {
				t.Fatalf("Validate with --accept-lost-credentials = %v, want %v", err, tt.wantAfter)
			}
		})
	}
}

// TestTarGuardRefusesWhatTheWriterNeverMakes: an archive's headers are
// checked before tar.Reader expands them. Regular files, with PAX records
// for long names and large sizes, read as before; an old-GNU sparse
// header with 50,000 extension blocks, a directory, a link, an oversized
// PAX record, a PAX key given twice, and a PAX key the writer never emits
// are refused at their header, without expanding them.
func TestTarGuardRefusesWhatTheWriterNeverMakes(t *testing.T) {
	var ok bytes.Buffer
	tw := tar.NewWriter(&ok)
	for _, h := range []*tar.Header{
		{Name: "assets/" + strings.Repeat("long/", 40) + "a.jpg", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg, Format: tar.FormatPAX, ModTime: time.Unix(1, 5)},
		{Name: "assets/b.jpg", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg},
	} {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(bytes.Repeat([]byte("x"), int(h.Size)))
	}
	_ = tw.Close()
	guard := newTarGuard(bytes.NewReader(ok.Bytes()))
	tr := tar.NewReader(guard)
	for range 2 {
		h, err := tr.Next()
		if err != nil {
			t.Fatalf("a regular archive: %v", err)
		}
		if err := guard.checkEntry(h.Size); err != nil {
			t.Fatalf("a regular archive: %v", err)
		}
	}
	if _, err := tr.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last entry: %v", err)
	}

	header := func(typeflag byte, size int64) []byte {
		var b bytes.Buffer
		tw := tar.NewWriter(&b)
		_ = tw.WriteHeader(&tar.Header{Name: "x", Mode: 0o644, Size: size, Typeflag: tar.TypeReg})
		raw := b.Bytes()[:512]
		raw[156] = typeflag
		// Recompute the checksum as tar.Reader would expect it.
		copy(raw[148:156], "        ")
		sum := 0
		for _, c := range raw {
			sum += int(c)
		}
		copy(raw[148:156], fmt.Sprintf("%06o\x00 ", sum))
		return raw
	}
	sparse := append(header('S', 0), bytes.Repeat(append(make([]byte, 511), 1), 50_000)...)
	// pax is a PAX record of records, then a regular header of size 0.
	pax := func(records ...string) []byte {
		var body []byte
		for _, r := range records {
			n := len(r) + 3
			for len(fmt.Sprintf("%d %s\n", n, r)) != n {
				n++
			}
			body = append(body, fmt.Sprintf("%d %s\n", n, r)...)
		}
		out := append(header('x', int64(len(body))), body...)
		out = append(out, make([]byte, (512-len(body)%512)%512)...)
		return append(out, header('0', 0)...)
	}
	for name, archive := range map[string][]byte{
		"old GNU sparse":   sparse,
		"directory":        header('5', 0),
		"symlink":          header('2', 0),
		"oversized record": append(header('x', maxPAXRecord+1), make([]byte, maxPAXRecord+512)...),
		// tar.Reader keeps the last of a repeated key; the guard would
		// have read the first, and taken what follows for data.
		"size given twice":    pax("size=100000", "size=0"),
		"path given twice":    pax("path=a", "path=b"),
		"a GNU sparse record": pax("GNU.sparse.size=1000"),
	} {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		_, err := tar.NewReader(newTarGuard(bytes.NewReader(archive))).Next()
		runtime.ReadMemStats(&after)
		if !errors.Is(err, errTarShape) {
			t.Fatalf("%s: %v, want errTarShape", name, err)
		}
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
			t.Fatalf("%s: refusing allocated %d bytes", name, allocated)
		}
	}
}

// TestNightlySlots: each night backs up once, on its server's wall clock
// across daylight saving changes, and a server in another zone that fires
// later the same night skips.
func TestNightlySlots(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip(err)
	}
	slot := func(at string, loc *time.Location) time.Time {
		t.Helper()
		now, err := time.ParseInLocation("2006-01-02 15:04", at, loc)
		if err != nil {
			t.Fatal(err)
		}
		return nightlySlot(now, "03:00", loc)
	}
	for _, tc := range []struct {
		name       string
		last, slot time.Time
		done       bool
	}{
		{"the night after spring forward", slot("2026-03-28 03:10", berlin), slot("2026-03-29 03:10", berlin), false},
		{"the night after falling back", slot("2026-10-24 03:10", berlin), slot("2026-10-25 03:10", berlin), false},
		{"a second run the same night", slot("2026-03-29 03:10", berlin), slot("2026-03-29 04:30", berlin), true},
		{"a UTC+1 server after a UTC server", slot("2026-01-10 03:05", time.UTC), slot("2026-01-10 03:20", time.FixedZone("UTC+1", 3600)), true},
		{"a UTC server after a UTC+1 server", slot("2026-01-10 03:05", time.FixedZone("UTC+1", 3600)), slot("2026-01-10 03:20", time.UTC), true},
	} {
		if got := slotDone(&tc.last, tc.slot); got != tc.done {
			t.Errorf("%s: last %v, slot %v: done %v, want %v", tc.name, tc.last, tc.slot, got, tc.done)
		}
	}
	if slotDone(nil, time.Now()) {
		t.Error("no recorded slot counts as done")
	}
}
