package imagecache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/logredact"
)

// Remote fetch bounds. A poster link needs no credentials, so these, not the
// caller, decide how hard the server may lean on a provider.
const (
	// remoteFetchSlots bounds the provider downloads one server runs at once
	// for poster links.
	remoteFetchSlots = 4
	// remoteFetchSlotWait is how long a download waits for a slot before the
	// request is refused as busy.
	remoteFetchSlotWait = 5 * time.Second
	// A finished download is kept this long, so repeating a link does not
	// repeat the provider download; a failed one is remembered for less, so
	// a provider that recovers is tried again soon.
	remoteResultTTL      = 5 * time.Minute
	remoteFailureTTL     = 30 * time.Second
	remoteResultMaxBytes = 4 << 20
	remoteResultsBudget  = 64 << 20
	// remoteResultsMax bounds how many outcomes are kept, so remembered
	// failures, which hold no bytes, cannot grow the map without limit.
	remoteResultsMax   = 4096
	remoteResultsSweep = time.Minute
)

// ErrRemoteBusy reports that every remote fetch slot stayed taken.
var ErrRemoteBusy = errors.New("imagecache: remote image fetches are busy")

// ErrNotImage reports a download whose bytes are not a served image type.
var ErrNotImage = errors.New("imagecache: remote body is not an image")

// ErrRemoteTooLarge reports an image too large to serve from a link. Every
// outcome a link produces must fit the fetcher's memory, or repeating the
// link would repeat the download; the image-cache job, which stores resized
// copies, is what serves a large original.
var ErrRemoteTooLarge = errors.New("imagecache: remote image is too large to serve from a link")

// RemoteImage is one downloaded image.
type RemoteImage struct {
	Data        []byte
	ContentType string
}

// RemoteFetcher downloads artwork that is not cached yet so the server can
// answer with its bytes. It uses the image-cache job's download (guarded
// transport, timeout, size cap), runs at most remoteFetchSlots downloads at
// once, and collapses concurrent requests for the same key into one download.
type RemoteFetcher struct {
	client *http.Client
	slots  chan struct{}
	group  singleflight.Group

	// results holds recent outcomes by key and URL. It bounds what a link
	// holder can make the server download: one provider request per artwork
	// per remoteResultTTL, not one per request.
	mu        sync.Mutex
	results   map[string]remoteResult
	bytes     int
	nextSweep time.Time
}

type remoteResult struct {
	image RemoteImage
	err   error
	until time.Time
}

// recent returns the remembered outcome for id, if it has not expired.
func (f *RemoteFetcher) recent(id string, now time.Time) (remoteResult, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result, ok := f.results[id]
	if !ok || !now.Before(result.until) {
		return remoteResult{}, false
	}
	return result, true
}

// remember keeps an outcome for later requests. Expired entries are dropped
// at most once per remoteResultsSweep.
func (f *RemoteFetcher) remember(id string, image RemoteImage, err error, now time.Time) {
	ttl := remoteResultTTL
	if err != nil && !errors.Is(err, ErrRemoteTooLarge) {
		// An oversized image stays oversized; other failures may clear.
		ttl = remoteFailureTTL
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.results == nil {
		f.results = make(map[string]remoteResult)
	}
	if !now.Before(f.nextSweep) {
		for key, result := range f.results {
			if !now.Before(result.until) {
				f.bytes -= len(result.image.Data)
				delete(f.results, key)
			}
		}
		f.nextSweep = now.Add(remoteResultsSweep)
	}
	if old, ok := f.results[id]; ok {
		f.bytes -= len(old.image.Data)
		delete(f.results, id)
	}
	// Every outcome is kept: when there is no room, the entries nearest
	// their expiry make way. An outcome that was not kept would let its link
	// repeat the download on every request.
	for len(f.results) > 0 && (f.bytes+len(image.Data) > remoteResultsBudget || len(f.results) >= remoteResultsMax) {
		oldest, soonest := "", time.Time{}
		for key, result := range f.results {
			if oldest == "" || result.until.Before(soonest) {
				oldest, soonest = key, result.until
			}
		}
		f.bytes -= len(f.results[oldest].image.Data)
		delete(f.results, oldest)
	}
	f.results[id] = remoteResult{image: image, err: err, until: now.Add(ttl)}
	f.bytes += len(image.Data)
}

// NewRemoteFetcher returns a fetcher on the guarded download client.
func NewRemoteFetcher() *RemoteFetcher {
	return newRemoteFetcher(newGuardedHTTPClient())
}

func newRemoteFetcher(client *http.Client) *RemoteFetcher {
	return &RemoteFetcher{client: client, slots: make(chan struct{}, remoteFetchSlots)}
}

// Fetch downloads rawURL from public addresses only, or the local network
// too when ctx carries netguard.WithPrivateAccess (artworkurl.FetchContext
// decides that per URL). Requests sharing key while a download runs share its
// result; key names the artwork (an item's content ID), so concurrent
// requests for one item make one download. The download
// runs detached from the first caller, so that caller leaving does not fail
// the rest; each caller still stops waiting when its own context ends.
func (f *RemoteFetcher) Fetch(ctx context.Context, key, rawURL string) (RemoteImage, error) {
	id := key + "\x00" + rawURL
	if result, ok := f.recent(id, time.Now()); ok {
		return result.image, result.err
	}
	// Shared by item and source: a request for a poster whose source just
	// changed must not join the download of the old one.
	flight := f.group.DoChan(id, func() (value any, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.ErrorContext(ctx, "remote image fetch panicked", "component", "imagecache",
					"panic", logredact.SanitizeText(fmt.Sprint(rec)), "stack", string(debug.Stack()))
				value, err = RemoteImage{}, errors.New("imagecache: remote image fetch panicked")
			}
		}()
		// A caller that missed the memory just before the previous download
		// finished lands here after it; it takes that outcome.
		if result, ok := f.recent(id, time.Now()); ok {
			return result.image, result.err
		}
		image, err := f.fetch(context.WithoutCancel(ctx), rawURL)
		// A busy server is not an outcome of this artwork; the next request
		// should try again.
		if !errors.Is(err, ErrRemoteBusy) {
			f.remember(id, image, err, time.Now())
		}
		return image, err
	})
	select {
	case <-ctx.Done():
		return RemoteImage{}, ctx.Err()
	case outcome := <-flight:
		if outcome.Err != nil {
			return RemoteImage{}, outcome.Err
		}
		image, _ := outcome.Val.(RemoteImage)
		return image, nil
	}
}

func (f *RemoteFetcher) fetch(ctx context.Context, rawURL string) (RemoteImage, error) {
	wait := time.NewTimer(remoteFetchSlotWait)
	defer wait.Stop()
	select {
	case f.slots <- struct{}{}:
	case <-wait.C:
		return RemoteImage{}, ErrRemoteBusy
	}
	defer func() { <-f.slots }()

	data, err := download(ctx, f.client, rawURL, true)
	if err != nil {
		return RemoteImage{}, err
	}
	if len(data) > remoteResultMaxBytes {
		return RemoteImage{}, ErrRemoteTooLarge
	}
	contentType, ok := ServedImageType(data)
	if !ok {
		return RemoteImage{}, ErrNotImage
	}
	return RemoteImage{Data: data, ContentType: contentType}, nil
}

// ServedImageType returns the media type data is served as when its bytes are
// a raster format every client renders: JPEG, PNG, WebP or GIF. The type comes
// from the bytes, never from the provider's Content-Type header, so an SVG or
// an HTML page labeled image/jpeg is refused.
func ServedImageType(data []byte) (string, bool) {
	switch sniffed := http.DetectContentType(data); sniffed {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return sniffed, true
	default:
		return "", false
	}
}
