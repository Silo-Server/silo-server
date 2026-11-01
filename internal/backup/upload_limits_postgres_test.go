package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
)

// probeDestination wraps a destination to report a chosen free space, make
// part reads fail or block until closed, refuse folder deletes, and count
// the part bodies left open.
type probeDestination struct {
	Destination
	mu   sync.Mutex
	free *uint64
	// opened receives each part body as it is opened.
	opened    chan struct{}
	failReads bool
	block     bool
	// blockPut makes part writes wait for their context to end; writing
	// counts them, peak is the most at once, and putStarted receives each.
	blockPut   bool
	writing    int
	peak       int
	putStarted chan struct{}
	refuse     int
	open       int
}

func (d *probeDestination) set(fn func(*probeDestination)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(d)
}

func (d *probeDestination) openBodies() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.open
}

func (d *probeDestination) FreeBytes(ctx context.Context) (uint64, bool, error) {
	d.mu.Lock()
	free := d.free
	d.mu.Unlock()
	if free != nil {
		return *free, true, nil
	}
	return d.Destination.FreeBytes(ctx)
}

func (d *probeDestination) Put(ctx context.Context, key string, r io.Reader) error {
	d.mu.Lock()
	block := d.blockPut
	d.writing++
	d.peak = max(d.peak, d.writing)
	if d.putStarted != nil {
		select {
		case d.putStarted <- struct{}{}:
		default:
		}
	}
	d.mu.Unlock()
	defer d.set(func(d *probeDestination) { d.writing-- })
	if block {
		<-ctx.Done()
		return context.Cause(ctx)
	}
	return d.Destination.Put(ctx, key, r)
}

func (d *probeDestination) DeletePrefix(ctx context.Context, prefix string) error {
	d.mu.Lock()
	refuse := d.refuse > 0
	if refuse {
		d.refuse--
	}
	d.mu.Unlock()
	if refuse {
		return errors.New("the store refused the delete")
	}
	return d.Destination.DeletePrefix(ctx, prefix)
}

func (d *probeDestination) Open(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	body, size, err := d.Destination.Open(ctx, key)
	if err != nil || !strings.HasPrefix(key, uploadsPrefix) {
		return body, size, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.open++
	if d.opened != nil {
		select {
		case d.opened <- struct{}{}:
		default:
		}
	}
	return &probeBody{d: d, body: body, fail: d.failReads, block: d.block, closed: make(chan struct{})}, size, nil
}

type probeBody struct {
	d           *probeDestination
	body        io.ReadCloser
	fail, block bool
	read        int
	closed      chan struct{}
	once        sync.Once
}

func (b *probeBody) Read(p []byte) (int, error) {
	if b.block {
		<-b.closed
		return 0, errors.New("read on a closed body")
	}
	if b.fail && b.read > 0 {
		return 0, errors.New("the store stopped answering")
	}
	n, err := b.body.Read(p[:min(len(p), 512)])
	b.read += n
	return n, err
}

func (b *probeBody) Close() error {
	b.once.Do(func() {
		close(b.closed)
		b.d.mu.Lock()
		b.d.open--
		b.d.mu.Unlock()
	})
	return b.body.Close()
}

// probeNodes are two API servers writing through one probe destination.
func probeNodes(t *testing.T, limits Options) (*pgxpool.Pool, blobstore.Store, *probeDestination, *Service, *Service) {
	t.Helper()
	pool, _ := testDB(t)
	resetUploads(t, pool)
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dest := &probeDestination{Destination: NewStoreDestination(store)}
	return pool, store, dest, uploadNodeOn(pool, store, dest, limits), uploadNodeOn(pool, store, dest, limits)
}

// statusGone is uploadStatus for an upload with no row.
const statusGone = "gone"

func uploadStatus(t *testing.T, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM backup_uploads WHERE id = $1`, id).Scan(&status); err != nil {
		return statusGone
	}
	return status
}

// age sets an object's modification time back.
func age(t *testing.T, store blobstore.Store, key string, by time.Duration) {
	t.Helper()
	at := time.Now().Add(-by)
	if err := os.Chtimes(filepath.Join(blobstoreRoot(t, store), filepath.FromSlash(key)), at, at); err != nil {
		t.Fatal(err)
	}
}

// TestUploadReservesSpaceDB: open uploads reserve what they have yet to
// write across API servers, so simultaneous creates cannot together exceed
// the free space; and every part checks the margin again before it is
// written.
func TestUploadReservesSpaceDB(t *testing.T) {
	pool, store, dest, a, b := probeNodes(t, Options{})
	ctx := t.Context()
	each := uint64(10 + MaxPartAttempts*MinUploadPartSize)
	free := freeSpaceMargin + 2*each + 5
	dest.set(func(d *probeDestination) { d.free = &free })
	errs := make(chan error, 3)
	var wg sync.WaitGroup
	for _, node := range []*Service{a, b, a} {
		wg.Go(func() {
			_, err := node.CreateUpload(ctx, 0, 10, "", MinUploadPartSize)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	created, refused := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrNotEnoughSpace):
			refused++
		default:
			t.Fatal(err)
		}
	}
	if created != 2 || refused != 1 {
		t.Fatalf("simultaneous creates near capacity: %d created, %d refused", created, refused)
	}

	// The disk filled after the upload opened: the part is refused before
	// anything is written.
	low := uint64(freeSpaceMargin + 5)
	dest.set(func(d *probeDestination) { d.free = &low })
	list, err := a.Uploads(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("uploads = %v, %v", list, err)
	}
	if _, err := b.PutUploadPart(ctx, list[0].ID, 0, strings.NewReader("0123456789"), 10); !errors.Is(err, ErrNotEnoughSpace) {
		t.Fatalf("a part on a full disk = %v, want ErrNotEnoughSpace", err)
	}
	if keys := uploadObjects(t, store, list[0].ID); len(keys) != 0 || countRows(t, pool, `SELECT count(*) FROM backup_upload_attempts`) != 0 {
		t.Fatalf("a refused part left %v and its attempt", keys)
	}
}

// TestUploadPartAttemptsDB: objects no part row names (a crash between
// replacing a part and deleting the old copy, or a folder whose session's
// delete failed) are reclaimed once older than the grace period, even with
// no backup running.
func TestUploadPartAttemptsDB(t *testing.T) {
	_, store, dest, a, b := probeNodes(t, Options{})
	ctx := t.Context()
	u, err := a.CreateUpload(ctx, 0, 10, "", MinUploadPartSize)
	if err != nil {
		t.Fatal(err)
	}
	putPart(t, a, u.ID, []byte("0123456789"), u.PartSize, 0)

	// A crash kept a replaced copy; another object is still being written.
	winning := uploadObjects(t, store, u.ID)[0]
	for _, name := range []string{"00000.replaced", "00000.inflight"} {
		if err := store.Put(ctx, uploadPrefix(u.ID)+name, []byte("0123456789")); err != nil {
			t.Fatal(err)
		}
	}
	age(t, store, uploadPrefix(u.ID)+"00000.replaced", 2*uploadObjectGrace)
	age(t, store, winning, 2*uploadObjectGrace)
	// A delete whose folder the store refused leaves a folder no session
	// names.
	gone, err := a.CreateUpload(ctx, 0, 10, "", MinUploadPartSize)
	if err != nil {
		t.Fatal(err)
	}
	putPart(t, a, gone.ID, []byte("0123456789"), gone.PartSize, 0)
	dest.set(func(d *probeDestination) { d.refuse = 1 })
	if err := a.DeleteUpload(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	left := uploadObjects(t, store, gone.ID)
	if len(left) != 1 {
		t.Fatalf("a refused folder delete left %v", left)
	}
	age(t, store, left[0], 2*uploadObjectGrace)

	// Listing runs the full sweep once the interval has passed.
	b.lastUploadSweep = time.Time{}
	if _, err := b.Uploads(ctx); err != nil {
		t.Fatal(err)
	}
	keys := uploadObjects(t, store, u.ID)
	if len(keys) != 2 || !strings.Contains(strings.Join(keys, " "), winning) || !strings.Contains(strings.Join(keys, " "), "inflight") {
		t.Fatalf("after the sweep the upload holds %v, want its part and the recent object", keys)
	}
	if left := uploadObjects(t, store, gone.ID); len(left) != 0 {
		t.Fatalf("a deleted upload's folder remains: %v", left)
	}
}

// TestUploadCompletionClosesPartsDB: completion closes the part it was
// reading however the read ends: an encrypted file rejected from its first
// bytes, a file the reader refuses, and a destination that stops answering.
func TestUploadCompletionClosesPartsDB(t *testing.T) {
	pool, _, dest, a, _ := probeNodes(t, Options{})
	ctx := t.Context()
	twoParts := func(head string) []byte {
		return append([]byte(head), bytes.Repeat([]byte("x"), int(MinUploadPartSize))...)
	}
	for _, c := range []struct {
		name      string
		data      []byte
		failReads bool
		want      string
	}{
		{"encrypted", twoParts("age-encryption.org/v1\n"), false, UploadRejected},
		{"not a restore set", twoParts("PGDMP"), false, UploadRejected},
		{"a read failure", paddedRestoreSet(t), true, UploadUploading},
	} {
		u, err := a.CreateUpload(ctx, 0, int64(len(c.data)), "", MinUploadPartSize)
		if err != nil {
			t.Fatal(err)
		}
		for n := range u.PartCount {
			putPart(t, a, u.ID, c.data, u.PartSize, n)
		}
		dest.set(func(d *probeDestination) { d.failReads = c.failReads })
		_, err = a.CompleteUpload(ctx, u.ID)
		dest.set(func(d *probeDestination) { d.failReads = false })
		if (err != nil) != c.failReads || uploadStatus(t, pool, u.ID) != c.want {
			t.Fatalf("%s: completion = %v, status %s", c.name, err, uploadStatus(t, pool, u.ID))
		}
		if open := dest.openBodies(); open != 0 {
			t.Fatalf("%s: %d part bodies left open", c.name, open)
		}
		if err := a.DeleteUpload(ctx, u.ID); err != nil {
			t.Fatal(err)
		}
	}
}

// TestUploadCompletionDeadlineDB: a completion whose destination read
// stalls is canceled at its deadline: the read is closed, the lock is
// released, and the upload can be completed again. While a completion
// holds the lock, the sweep leaves its upload alone however old it looks;
// a checking upload whose server stopped is swept once the lock is free.
func TestUploadCompletionDeadlineDB(t *testing.T) {
	pool, _, dest, a, b := probeNodes(t, Options{UploadCheckTimeout: 2 * time.Second})
	ctx := t.Context()
	data := paddedRestoreSet(t)
	u, err := a.CreateUpload(ctx, 0, int64(len(data)), "", MinUploadPartSize)
	if err != nil {
		t.Fatal(err)
	}
	for n := range u.PartCount {
		putPart(t, a, u.ID, data, u.PartSize, n)
	}
	opened := make(chan struct{}, 1)
	dest.set(func(d *probeDestination) { d.block, d.opened = true, opened })
	started := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := a.CompleteUpload(ctx, u.ID)
		done <- err
	}()
	<-opened
	dbExec(t, pool, `UPDATE backup_uploads SET updated_at = now() - interval '25 hours' WHERE id = $1`, u.ID)
	b.sweepUploads(ctx, true, false)
	if status := uploadStatus(t, pool, u.ID); status != UploadChecking {
		t.Fatalf("an upload being checked after the sweep = %s", status)
	}
	if err := <-done; err == nil || time.Since(started) > 10*time.Second {
		t.Fatalf("a stalled completion = %v after %v", err, time.Since(started))
	}
	if open := dest.openBodies(); open != 0 {
		t.Fatalf("%d part bodies left open after the deadline", open)
	}
	held, err := b.opts.Locker.TryLock(ctx)
	if err != nil {
		t.Fatalf("the lock after the deadline: %v", err)
	}
	held.Release(ctx)
	if status := uploadStatus(t, pool, u.ID); status != UploadUploading {
		t.Fatalf("after the deadline the upload is %s, want uploading", status)
	}
	dest.set(func(d *probeDestination) { d.block = false })
	if again, err := b.CompleteUpload(ctx, u.ID); err != nil || again.Status != UploadComplete {
		t.Fatalf("completing again = %+v, %v", again, err)
	}

	// A checking upload left by a server that stopped, with the lock free.
	dbExec(t, pool, `UPDATE backup_uploads SET status = 'checking', updated_at = now() - interval '25 hours' WHERE id = $1`, u.ID)
	b.sweepUploads(ctx, false, false)
	if status := uploadStatus(t, pool, u.ID); status != statusGone {
		t.Fatalf("an abandoned check after the sweep = %s", status)
	}
}

// TestUploadPartWriteDeadlineDB: a part's storage write has a lifetime
// shorter than its attempt. A write that stalls is canceled at its
// deadline, and its attempt and object are removed, so a retry then
// succeeds; while the first write is within its lifetime no second writer
// of the part starts.
func TestUploadPartWriteDeadlineDB(t *testing.T) {
	if partAttemptLimit < partWriteTimeout+time.Minute {
		t.Fatalf("an attempt (%v) must outlast its write (%v) by a margin", partAttemptLimit, partWriteTimeout)
	}
	pool, store, dest, a, b := probeNodes(t, Options{UploadPartWriteTimeout: time.Second})
	ctx := t.Context()
	u, err := a.CreateUpload(ctx, 0, 10, "", MinUploadPartSize)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	dest.set(func(d *probeDestination) { d.blockPut, d.putStarted = true, started })
	first := make(chan error, 1)
	begun := time.Now()
	go func() {
		_, err := a.PutUploadPart(ctx, u.ID, 0, strings.NewReader("0123456789"), 10)
		first <- err
	}()
	<-started
	if _, err := b.PutUploadPart(ctx, u.ID, 0, strings.NewReader("0123456789"), 10); !errors.Is(err, ErrPartBusy) {
		t.Fatalf("a retry while the first write runs = %v, want ErrPartBusy", err)
	}
	if err := <-first; !errors.Is(err, context.DeadlineExceeded) || time.Since(begun) > 10*time.Second {
		t.Fatalf("a stalled write = %v after %v, want its deadline", err, time.Since(begun))
	}
	if n := countRows(t, pool, `SELECT count(*) FROM backup_upload_attempts`); n != 0 || len(uploadObjects(t, store, u.ID)) != 0 {
		t.Fatalf("a canceled write left %d attempts and objects %v", n, uploadObjects(t, store, u.ID))
	}
	dest.set(func(d *probeDestination) { d.blockPut = false })
	if got, err := b.PutUploadPart(ctx, u.ID, 0, strings.NewReader("0123456789"), 10); err != nil || got.ReceivedParts != 1 {
		t.Fatalf("the retry = %+v, %v", got, err)
	}
	dest.set(func(d *probeDestination) {
		if d.peak != 1 {
			t.Errorf("%d writers of the part ran at once", d.peak)
		}
	})
}
