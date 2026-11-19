package imagecache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
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
)

// ErrRemoteBusy reports that every remote fetch slot stayed taken.
var ErrRemoteBusy = errors.New("imagecache: remote image fetches are busy")

// ErrNotImage reports a download whose bytes are not a served image type.
var ErrNotImage = errors.New("imagecache: remote body is not an image")

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
	flight := f.group.DoChan(key, func() (value any, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.ErrorContext(ctx, "remote image fetch panicked", "component", "imagecache",
					"panic", logredact.SanitizeText(fmt.Sprint(rec)), "stack", string(debug.Stack()))
				value, err = RemoteImage{}, errors.New("imagecache: remote image fetch panicked")
			}
		}()
		return f.fetch(context.WithoutCancel(ctx), rawURL)
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
	contentType, ok := ServedImageType(data)
	if !ok {
		return RemoteImage{}, ErrNotImage
	}
	return RemoteImage{Data: data, ContentType: contentType}, nil
}

// ServedImageType returns the media type data is served as when its bytes are
// a raster format every client renders: JPEG, PNG, WebP or GIF. The type comes
// from the bytes, never from the provider's Content-Type header, so an SVG or
// an HTML page labelled image/jpeg is refused.
func ServedImageType(data []byte) (string, bool) {
	switch sniffed := http.DetectContentType(data); sniffed {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		return sniffed, true
	default:
		return "", false
	}
}
