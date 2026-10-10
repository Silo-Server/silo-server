package downloadstorage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A read parked on a hung mount times out, and until it returns no second
// read starts.
func TestDirGuardBoundsAndSerializesReads(t *testing.T) {
	release := make(chan struct{})
	in := &DirGuard{read: func(dir, _ string, now time.Time) Listing {
		<-release
		return Listing{Usage: Usage{Dir: dir, MeasuredAt: now}}
	}}
	if _, err := in.Inspect(context.Background(), "/hung", "", 10*time.Millisecond); !errors.Is(err, ErrDirTimeout) {
		t.Fatalf("hung read = %v, want ErrDirTimeout", err)
	}
	if _, err := in.Inspect(context.Background(), "/hung", "", time.Second); !errors.Is(err, ErrDirBusy) {
		t.Fatalf("second read while the first is parked = %v, want ErrDirBusy", err)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		listing, err := in.Inspect(context.Background(), "/back", "", time.Second)
		if err == nil {
			if listing.Usage.Dir != "/back" {
				t.Fatalf("listing = %+v", listing)
			}
			return
		}
		if !errors.Is(err, ErrDirBusy) || time.Now().After(deadline) {
			t.Fatalf("read after the mount answered = %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A completed read frees the slot before the caller sees its answer, so the
// next call starts a new read.
func TestDirGuardFreesTheSlotBeforeAnswering(t *testing.T) {
	in := &DirGuard{read: func(dir, _ string, now time.Time) Listing {
		return Listing{Usage: Usage{Dir: dir, MeasuredAt: now}}
	}}
	for i := range 100 {
		if _, err := in.Inspect(context.Background(), "/dir", "", time.Second); err != nil {
			t.Fatalf("read %d = %v", i, err)
		}
	}
}

// Removals share the guard with listings: while a read is parked on a hung
// mount, a removal fails at once instead of parking too. Once the directory
// answers, removals delete what exists and ignore what is already gone.
func TestDirGuardRemoveSharesTheSlot(t *testing.T) {
	release := make(chan struct{})
	g := &DirGuard{read: func(dir, _ string, now time.Time) Listing {
		<-release
		return Listing{}
	}}
	if _, err := g.Inspect(context.Background(), "/hung", "", 10*time.Millisecond); !errors.Is(err, ErrDirTimeout) {
		t.Fatalf("hung read = %v", err)
	}
	if err := g.Remove(context.Background(), time.Second, "/hung/file"); !errors.Is(err, ErrDirBusy) {
		t.Fatalf("removal during a parked read = %v, want ErrDirBusy", err)
	}
	close(release)

	dir := t.TempDir()
	path := filepath.Join(dir, "prepared.mp4")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := g.Remove(context.Background(), time.Second, path, path+".part")
		if err == nil {
			break
		}
		if !errors.Is(err, ErrDirBusy) || time.Now().After(deadline) {
			t.Fatalf("removal after the mount answered = %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still there: %v", err)
	}
}
