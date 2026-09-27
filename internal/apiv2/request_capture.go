package apiv2

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
)

// requestCapture retains the authorized context for streamed bodies and byte
// responses. With no Body or RawBody field Huma leaves the body unread.
type requestCapture struct {
	request *http.Request
	writer  http.ResponseWriter
}

func (in *requestCapture) Resolve(ctx huma.Context) []error {
	r, w := humachi.Unwrap(ctx)
	in.request, in.writer = r.WithContext(ctx.Context()), w
	return nil
}
