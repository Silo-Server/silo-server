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
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
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
}

func newFakeControl(t *testing.T) *fakeControl {
	return &fakeControl{t: t, dbs: map[string]*fakeDB{liveDB: {oid: 100, allow: true}}, nextOID: 200, fail: map[string]error{}, lostReply: map[string]bool{}}
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
func (l fakeLoader) Load(_ context.Context, dbURL, _ string) error {
	name, err := databaseName(dbURL)
	if err != nil {
		return err
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
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, ManifestName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := OpenSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	return src
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
	return &swapFixture{t: t, ctl: newFakeControl(t), root: root, live: live, src: writeTestBackup(t, entries), workDir: t.TempDir(), cipher: cipher}
}

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
			OpenStores:  f.openStores, Cipher: f.cipher,
			RestoredSettings: func(context.Context, string) (map[string]string, error) { return map[string]string{}, nil },
			WorkDir:          f.workDir, Now: func() time.Time { return swapNow }, Out: &f.output,
		})
		returned = true
		done <- e
	}()
	err = <-done
	return err, !returned
}

func (f *swapFixture) recover() (err error, crashed bool) {
	f.t.Helper()
	done := make(chan error, 1)
	returned := false
	go func() {
		defer close(done)
		e := RecoverSwap(f.t.Context(), JournalPath(f.workDir, swapStamp), f.ctl, f.cipher, f.openStores, &f.output)
		returned = true
		done <- e
	}()
	err = <-done
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
	binary := ValidateOptions{BinaryGooseVersion: 9, BinaryMigrations: []int64{1, 9}}
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
