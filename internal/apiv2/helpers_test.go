package apiv2

import (
	"bufio"
	"net"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

type huma_Operation = huma.Operation

func chiRequestIDFrom(r *http.Request) string { return chimw.GetReqID(r.Context()) }

func bufioReader(c net.Conn) *bufio.Reader { return bufio.NewReader(c) }

// Declaration tests need the real registration adapter, but no application routes.
func registerTestOperations(register func(*Registry)) {
	register(&Registry{api: humachi.New(chi.NewRouter(), humaConfig())})
}
