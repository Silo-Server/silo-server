package nodesource

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// fakeCore answers open calls from a queue of outcomes; once the queue is
// empty it repeats the approved source.
type fakeCore struct {
	mu       sync.Mutex
	answer   OpenResponse
	outcomes []func() (*OpenResponse, error)
	calls    int
}

func (c *fakeCore) open(context.Context, string) (*OpenResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if len(c.outcomes) > 0 {
		next := c.outcomes[0]
		c.outcomes = c.outcomes[1:]
		return next()
	}
	answer := c.answer
	return &answer, nil
}

func (c *fakeCore) then(outcomes ...func() (*OpenResponse, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outcomes = append(c.outcomes, outcomes...)
}

func (c *fakeCore) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func refuse(status int) func() (*OpenResponse, error) {
	return func() (*OpenResponse, error) { return nil, &StatusError{Op: "open", Status: status} }
}

func unreachable() (*OpenResponse, error) { return nil, errors.New("connection refused") }

// approvedCore is a core that approves the file at path as it is now.
func approvedCore(t *testing.T, path string) *fakeCore {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	revision := Revision{Kind: KindFile, ID: 42, Size: info.Size(), ModTimeUnixNano: info.ModTime().UnixNano()}
	return &fakeCore{answer: OpenResponse{Grant: "renewed", Path: path, Size: revision.Size, ModTimeUnixNano: revision.ModTimeUnixNano, ETag: revision.ETag()}}
}

func quietOpener(open OpenFunc) *Opener {
	return NewOpener(open, slog.New(slog.DiscardHandler))
}

func TestOpenerOpensTheApprovedLocalFile(t *testing.T) {
	path := writeSource(t, "movie.mkv", "movie")
	core := approvedCore(t, path)
	opener := quietOpener(core.open)
	t.Cleanup(func() { _ = opener.Close() })

	source, err := opener.Open(t.Context(), "dispatched")
	if err != nil {
		t.Fatal(err)
	}
	if source.Input() != path || source.Size() != 5 || source.Identity() != core.answer.ETag || source.Err() != nil {
		t.Fatalf("source = %s %d %s %v", source.Input(), source.Size(), source.Identity(), source.Err())
	}
	_ = source.Close()
	if !errors.Is(source.Err(), ErrClosed) || source.Context().Err() == nil {
		t.Fatalf("after close: %v", source.Err())
	}

	if _, err := opener.Open(t.Context(), " "); !errors.Is(err, ErrNoGrant) {
		t.Fatalf("no grant: %v", err)
	}
	core.then(refuse(http.StatusGone))
	if _, err := opener.Open(t.Context(), "dispatched"); err == nil {
		t.Fatal("a refused grant opened")
	} else if refusal, ok := errors.AsType[*StatusError](err); !ok || refusal.Status != http.StatusGone {
		t.Fatalf("refused: %v", err)
	}
}

// A node whose mount shows a file of another size, or none, refuses the job
// with an error naming both sides.
func TestOpenerRefusesAMountMismatch(t *testing.T) {
	path := writeSource(t, "movie.mkv", "movie")
	core := approvedCore(t, path)
	core.answer.Size++
	_, err := quietOpener(core.open).Open(t.Context(), "dispatched")
	mismatch, ok := errors.AsType[*MountMismatchError](err)
	if !ok || !errors.Is(err, ErrMountMismatch) || mismatch.CoreSize != 6 || mismatch.NodeSize != 5 {
		t.Fatalf("open = %v, want a mount mismatch of 6 against 5 bytes", err)
	}

	core = approvedCore(t, path)
	core.answer.Path = path + ".missing"
	if _, err := quietOpener(core.open).Open(t.Context(), "dispatched"); !errors.Is(err, ErrMountMismatch) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing on the node: %v", err)
	}
}

// Mounts of storage that keeps no modification times each report a
// placeholder of their own, so the core and a node can date one file decades
// apart. The node opens it by path and size, and reports the time its own
// mount gives beside the granted one.
func TestOpenerOpensASourceItsMountDatesDifferently(t *testing.T) {
	path := writeSource(t, "movie.mkv", "movie")
	nodeTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, nodeTime, nodeTime); err != nil {
		t.Fatal(err)
	}
	core := approvedCore(t, path)
	core.answer.ModTimeUnixNano = 0
	core.answer.ETag = Revision{Kind: KindFile, ID: 42, Size: core.answer.Size}.ETag()
	opener := quietOpener(core.open)
	t.Cleanup(func() { _ = opener.Close() })

	source, err := opener.Open(t.Context(), "dispatched")
	if err != nil {
		t.Fatalf("open = %v, want the source opened by path and size", err)
	}
	if !source.ModTime().Equal(time.Unix(0, 0)) || !source.InputModTime().Equal(nodeTime) {
		t.Fatalf("granted %s, input %s; want the core's 1970 and the node's 2000", source.ModTime().UTC(), source.InputModTime().UTC())
	}
}

// Renewal survives a core outage shorter than the grant's life and ends the
// input on the core's lasting refusal.
func TestOpenerRenewal(t *testing.T) {
	path := writeSource(t, "movie.mkv", "movie")
	synctest.Test(t, func(t *testing.T) {
		core := approvedCore(t, path)
		opener := quietOpener(core.open)
		defer func() { _ = opener.Close() }()
		source, err := opener.Open(t.Context(), "dispatched")
		if err != nil {
			t.Fatal(err)
		}

		// The core is unreachable for a few retries at the first renewal,
		// then answers.
		core.then(unreachable, unreachable, unreachable)
		time.Sleep(RenewInterval + 10*time.Second) //nolint:forbidigo // synctest: advances the bubble's fake clock
		synctest.Wait()
		if source.Err() != nil || core.callCount() != 5 {
			t.Fatalf("after a short outage: err %v, %d open calls, want 5", source.Err(), core.callCount())
		}

		// At the next renewal the core says the file is gone.
		core.then(refuse(http.StatusGone))
		time.Sleep(RenewInterval) //nolint:forbidigo // synctest: advances the bubble's fake clock
		synctest.Wait()
		if refusal, ok := errors.AsType[*StatusError](source.Err()); !ok || refusal.Status != http.StatusGone || source.Context().Err() == nil {
			t.Fatalf("after a refusal: %v", source.Err())
		}
	})
}

func TestOpenerRenewalEnds(t *testing.T) {
	path := writeSource(t, "movie.mkv", "movie")
	t.Run("an answer about another source", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			core := approvedCore(t, path)
			opener := quietOpener(core.open)
			defer func() { _ = opener.Close() }()
			source, err := opener.Open(t.Context(), "dispatched")
			if err != nil {
				t.Fatal(err)
			}
			other := core.answer
			other.Size++
			core.then(func() (*OpenResponse, error) { return &other, nil })
			time.Sleep(RenewInterval) //nolint:forbidigo // synctest: advances the bubble's fake clock
			synctest.Wait()
			if refusal, ok := errors.AsType[*StatusError](source.Err()); !ok || refusal.Status != http.StatusPreconditionFailed {
				t.Fatalf("err %v, want 412", source.Err())
			}
		})
	})
	t.Run("expiry while the core is unreachable", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			core := approvedCore(t, path)
			opener := quietOpener(core.open)
			defer func() { _ = opener.Close() }()
			source, err := opener.Open(t.Context(), "dispatched")
			if err != nil {
				t.Fatal(err)
			}
			for range 100 {
				core.then(unreachable)
			}
			time.Sleep(GrantLifetime) //nolint:forbidigo // synctest: advances the bubble's fake clock
			synctest.Wait()
			if !errors.Is(source.Err(), ErrGrantExpired) {
				t.Fatalf("err %v, want ErrGrantExpired", source.Err())
			}
		})
	})
	t.Run("a longer grant arrives while a renewal is out", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			core := approvedCore(t, path)
			opener := quietOpener(core.open)
			defer func() { _ = opener.Close() }()
			source, err := opener.Open(t.Context(), "dispatched")
			if err != nil {
				t.Fatal(err)
			}
			// The first renewal hangs until just before the grant expires,
			// and fails only after another job has brought a fresh grant.
			release := make(chan struct{})
			core.then(func() (*OpenResponse, error) {
				<-release
				return unreachable()
			})
			time.Sleep(GrantLifetime - time.Second) //nolint:forbidigo // synctest: advances the bubble's fake clock
			synctest.Wait()
			source.(*handle).offerGrant("fresher")
			close(release)
			time.Sleep(time.Minute) //nolint:forbidigo // synctest: advances the bubble's fake clock
			synctest.Wait()
			if source.Err() != nil {
				t.Fatalf("err %v, want the source kept on the fresher grant", source.Err())
			}
		})
	})
	t.Run("a refusal of a grant another job has replaced", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			core := approvedCore(t, path)
			opener := quietOpener(core.open)
			defer func() { _ = opener.Close() }()
			source, err := opener.Open(t.Context(), "dispatched")
			if err != nil {
				t.Fatal(err)
			}
			// The first renewal is refused for good, but only after
			// another job has brought a newer grant.
			release := make(chan struct{})
			core.then(func() (*OpenResponse, error) {
				<-release
				return refuse(http.StatusForbidden)()
			})
			time.Sleep(RenewInterval + time.Second) //nolint:forbidigo // synctest: advances the bubble's fake clock
			synctest.Wait()
			source.(*handle).offerGrant("fresher")
			close(release)
			time.Sleep(time.Minute) //nolint:forbidigo // synctest: advances the bubble's fake clock
			synctest.Wait()
			if source.Err() != nil {
				t.Fatalf("err %v, want the source kept on the newer grant", source.Err())
			}
		})
	})
	t.Run("the opener closes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			core := approvedCore(t, path)
			opener := quietOpener(core.open)
			source, err := opener.Open(t.Context(), "dispatched")
			if err != nil {
				t.Fatal(err)
			}
			_ = opener.Close()
			if !errors.Is(source.Err(), ErrOpenerClosed) {
				t.Fatalf("err %v", source.Err())
			}
			if _, err := opener.Open(t.Context(), "dispatched"); !errors.Is(err, ErrOpenerClosed) {
				t.Fatalf("open after close: %v", err)
			}
		})
	})
}
