package handlers

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/imageutil"
)

// uploadedImageDecodes bounds how many images uploaded by signed-in profiles
// are decoded at once in this process: personal collection posters and
// profile avatars. Decoded size, not file size, decides what a decode costs:
// one can hold several hundred megabytes of memory and temporary disk, and
// the rate limiter bounds a profile's requests per second, not its requests
// in flight. Two decodes
// match the smallest image cache worker pool. Each upload that waits for a
// turn holds its bytes, up to 10 MiB, so only a few may wait.
//
// Artwork the server fetches or composes itself, and artwork an administrator
// uploads, does not pass through here, so a burst of uploads cannot stall it.
var uploadedImageDecodes = newDecodeSlots(2, 8)

// uploadedImageDecodeRetryAfter is the Retry-After a busy answer carries: a
// decode of the largest image accepted takes a few seconds.
const uploadedImageDecodeRetryAfter = 5

// decodeSlots admits a fixed number of image decodes at once and lets a
// fixed number more wait for a turn.
type decodeSlots struct {
	running chan struct{}
	waiting chan struct{}
}

func newDecodeSlots(running, waiting int) *decodeSlots {
	return &decodeSlots{running: make(chan struct{}, running), waiting: make(chan struct{}, waiting)}
}

// decode runs fn when a slot is free. With every slot taken it waits for one
// until ctx ends; with the wait full as well it runs nothing and returns a
// retryable 503 *APIError.
func (s *decodeSlots) decode(ctx context.Context, fn func() (*imageutil.VariantResult, error)) (*imageutil.VariantResult, error) {
	select {
	case s.running <- struct{}{}:
	default:
		select {
		case s.waiting <- struct{}{}:
		default:
			busy := apiError(http.StatusServiceUnavailable, "service_unavailable", "The server is busy with other uploads; try again in a moment")
			busy.RetryAfter = uploadedImageDecodeRetryAfter
			return nil, busy
		}
		select {
		case s.running <- struct{}{}:
			<-s.waiting
		case <-ctx.Done():
			<-s.waiting
			return nil, ctx.Err()
		}
	}
	defer func() { <-s.running }()
	return fn()
}
