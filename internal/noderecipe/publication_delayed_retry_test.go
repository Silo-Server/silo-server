package noderecipe

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func publicationReadCommand(r *bufio.Reader) ([]byte, []string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, nil, err
	}
	if len(line) == 0 || line[0] != '*' {
		return nil, nil, fmt.Errorf("expected array: %q", line)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return nil, nil, err
	}
	raw := []byte(line)
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		head, e := r.ReadString('\n')
		if e != nil {
			return nil, nil, e
		}
		if len(head) == 0 || head[0] != '$' {
			return nil, nil, fmt.Errorf("expected bulk: %q", head)
		}
		size, e := strconv.Atoi(strings.TrimSpace(head[1:]))
		if e != nil {
			return nil, nil, e
		}
		body := make([]byte, size+2)
		if _, e = io.ReadFull(r, body); e != nil {
			return nil, nil, e
		}
		raw = append(raw, head...)
		raw = append(raw, body...)
		args = append(args, string(body[:size]))
	}
	return raw, args, nil
}

//nolint:funlen,gocyclo // Keep this ordered failure-injection scenario and its exact authority assertions in one scope.
func TestRedisPublicationDelayedRetriesCannotRepublishRejectedCandidate(t *testing.T) {
	_, direct, work := publicationTestStore(t)
	for _, script := range []*redis.Script{prepareRecipePublicationScript, publishRecipeScript, retireRecipePublicationScript} {
		if err := script.Load(t.Context(), direct).Err(); err != nil {
			t.Fatal(err)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var seenPrepare, seenPublish atomic.Int32
	releasePrepare, releasePublish := make(chan struct{}), make(chan struct{})
	donePrepare, donePublish := make(chan struct{}), make(chan struct{})
	releaseP := sync.OnceFunc(func() { close(releasePrepare) })
	releaseW := sync.OnceFunc(func() { close(releasePublish) })
	var wg sync.WaitGroup
	t.Cleanup(func() { releaseP(); releaseW(); _ = ln.Close(); wg.Wait() })
	go func() {
		for {
			down, e := ln.Accept()
			if e != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = down.Close() }()
				up, e := net.Dial("tcp", direct.Options().Addr)
				if e != nil {
					t.Error(e)
					return
				}
				defer func() { _ = up.Close() }()
				var responseGate atomic.Pointer[chan struct{}]
				go func() {
					buffer := make([]byte, 32768)
					for {
						n, e := up.Read(buffer)
						if n > 0 {
							if gate := responseGate.Swap(nil); gate != nil {
								close(*gate)
							}
							if _, werr := down.Write(buffer[:n]); werr != nil {
								return
							}
						}
						if e != nil {
							return
						}
					}
				}()
				r := bufio.NewReader(down)
				for {
					raw, args, e := publicationReadCommand(r)
					if e != nil {
						return
					}
					var heldDone chan struct{}
					if len(args) > 1 && strings.EqualFold(args[0], "evalsha") {
						switch args[1] {
						case prepareRecipePublicationScript.Hash():
							if seenPrepare.Add(1) == 1 {
								<-releasePrepare
								heldDone = donePrepare
							}
						case publishRecipeScript.Hash():
							if seenPublish.Add(1) == 1 {
								<-releasePublish
								heldDone = donePublish
							}
						}
					}
					if heldDone != nil {
						responseGate.Store(&heldDone)
					}
					if _, e = up.Write(raw); e != nil {
						t.Error(e)
						return
					}
					if heldDone != nil { // A real Redis reply confirms execution after the original client timed out.
						select {
						case <-heldDone:
						case <-t.Context().Done():
						}
						return
					}
				}
			}()
		}
	}()
	opts := *direct.Options()
	opts.Addr = ln.Addr().String()
	opts.ReadTimeout = 25 * time.Millisecond
	opts.WriteTimeout = time.Second
	opts.MaxRetries = 2
	opts.MinRetryBackoff = time.Millisecond
	opts.MaxRetryBackoff = time.Millisecond
	client := redis.NewClient(&opts)
	t.Cleanup(func() { _ = client.Close() })
	store := NewStore(client, time.Minute)
	card := playback.RecipeCard{SessionID: work, InputPath: "/approved/rejected-retry"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	receipt, err := store.PublishWithUndo(ctx, work, card)
	if err != nil {
		t.Fatal("initial publication failed", err)
	}
	if seenPrepare.Load() < 2 || seenPublish.Load() < 2 {
		t.Fatalf("real Redis automatic retries did not fire: prepare=%d publish=%d", seenPrepare.Load(), seenPublish.Load())
	}
	if err = receipt.Retire(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found := store.Get(ctx, work); found {
		t.Fatal("candidate not retired")
	}
	releaseP()
	select {
	case <-donePrepare:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	releaseW()
	select {
	case <-donePublish:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if got, found := store.Get(ctx, work); found {
		t.Fatalf("timed-out original prepare and publish arrived after acknowledged retry+Retire and resurrected rejected candidate %q (prepare=%d publish=%d)", got.InputPath, seenPrepare.Load(), seenPublish.Load())
	}
}
