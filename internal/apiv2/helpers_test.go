package apiv2

import (
	"bufio"
	"net"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	chimw "github.com/go-chi/chi/v5/middleware"
)

type huma_Operation = huma.Operation

func chiRequestIDFrom(r *http.Request) string { return chimw.GetReqID(r.Context()) }

func bufioReader(c net.Conn) *bufio.Reader { return bufio.NewReader(c) }
