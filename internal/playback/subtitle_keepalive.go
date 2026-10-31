package playback

import (
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// A whole-track sidecar read through the container's index can take tens of
// seconds on network storage before its first byte, and players give up on
// a silent response long before the server's stall window: Android's player
// client reads with a 30 s timeout, Apple's after about a minute. A WebVTT
// response can show progress by sending its constant header ahead a byte at
// a time, then blank lines until output arrives. WebVTT ignores those empty
// blocks. The header is dropped from the output when it comes.
//
// SubRip, ASS and SUP outputs start with no constant prefix (ASS's script
// header is the track's own, SUP's first bytes carry a timestamp), so their
// first byte still waits on the read, which a request waits on for at most
// the source's whole-track wait. A client that gives up leaves the read
// running, and its retry finds the track being read or kept.

// webVTTPrefix starts every WebVTT output of FFmpeg's muxer.
const webVTTPrefix = "WEBVTT\n"

var (
	// keepaliveDelay is how long output may be silent before the first
	// prefix byte goes ahead; a kept track converts well within it.
	keepaliveDelay = 5 * time.Second
	// keepaliveInterval spaces prefix bytes and subsequent blank lines so
	// no gap nears a 30 s read timeout.
	keepaliveInterval = 12 * time.Second
)

// errKeepalivePrefix means the output didn't start with the prefix sent
// ahead of it: the response can't be completed correctly.
var errKeepalivePrefix = errors.New("subtitle output doesn't start with the prefix sent ahead of it")

// prefixKeepalive writes to w, sending prefix ahead a byte at a time while
// nothing has been written, and dropping the bytes it sent from the start
// of the output, which must match them.
type prefixKeepalive struct {
	w      io.Writer
	prefix string
	done   chan struct{}
	once   sync.Once

	mu sync.Mutex
	// sent counts the prefix bytes sent ahead; matched those the output has
	// repeated since. started stops the keepalive once output arrives.
	sent, matched int
	started       bool
	err           error
}

func newPrefixKeepalive(w io.Writer, prefix string, delay, interval time.Duration) *prefixKeepalive {
	k := &prefixKeepalive{w: w, prefix: prefix, done: make(chan struct{})}
	go k.run(delay, interval)
	return k
}

func (k *prefixKeepalive) run(delay, interval time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-k.done:
			return
		case <-timer.C:
		}
		if !k.tick() {
			return
		}
		timer.Reset(interval)
	}
}

// tick sends the next prefix byte. It reports whether more may follow.
func (k *prefixKeepalive) tick() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.started || k.err != nil {
		return false
	}
	next := byte('\n')
	if k.sent < len(k.prefix) {
		next = k.prefix[k.sent]
	}
	if _, err := k.w.Write([]byte{next}); err != nil {
		k.err = err
		return false
	}
	if k.sent < len(k.prefix) {
		k.sent++
	}
	if f, ok := k.w.(http.Flusher); ok {
		f.Flush()
	}
	return true
}

func (k *prefixKeepalive) Write(p []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		return 0, k.err
	}
	k.started = true
	n := len(p)
	for k.matched < k.sent && len(p) > 0 {
		if p[0] != k.prefix[k.matched] {
			k.err = errKeepalivePrefix
			return 0, k.err
		}
		p = p[1:]
		k.matched++
	}
	if len(p) == 0 {
		return n, nil
	}
	if _, err := k.w.Write(p); err != nil {
		k.err = err
		return 0, err
	}
	return n, nil
}

func (k *prefixKeepalive) Flush() {
	if f, ok := k.w.(http.Flusher); ok {
		f.Flush()
	}
}

// stop ends the keepalive; it sends nothing after.
func (k *prefixKeepalive) stop() {
	k.once.Do(func() { close(k.done) })
	k.mu.Lock()
	k.started = true
	k.mu.Unlock()
}
