package handlers

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

// closedLoopbackURL returns a loopback URL with nothing listening on it.
func closedLoopbackURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return "http://" + addr + "/poster.jpg"
}

// redirectFirstHop answers requests to one public-looking host with a
// redirect, without dialing, and sends every other request through next.
type redirectFirstHop struct {
	host, location string
	next           http.RoundTripper
}

func (rt redirectFirstHop) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != rt.host {
		return rt.next.RoundTrip(req)
	}
	return &http.Response{
		StatusCode: http.StatusFound,
		Header:     http.Header{"Location": []string{rt.location}},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}
