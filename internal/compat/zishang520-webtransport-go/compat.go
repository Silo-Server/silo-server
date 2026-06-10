// Package webtransport preserves the github.com/zishang520/webtransport-go
// API shape expected by github.com/zishang520/engine.io while delegating to
// the maintained github.com/quic-go/webtransport-go module.
package webtransport

import (
	"net"
	"net/http"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	upstream "github.com/quic-go/webtransport-go"
)

type (
	Session          = upstream.Session
	SessionState     = upstream.SessionState
	SessionError     = upstream.SessionError
	SessionErrorCode = upstream.SessionErrorCode
	Stream           = upstream.Stream
	ReceiveStream    = upstream.ReceiveStream
	SendStream       = upstream.SendStream
	StreamError      = upstream.StreamError
	StreamErrorCode  = upstream.StreamErrorCode
	Dialer           = upstream.Dialer
)

const (
	WTBufferedStreamRejectedErrorCode = upstream.WTBufferedStreamRejectedErrorCode
	WTSessionGoneErrorCode            = upstream.WTSessionGoneErrorCode
)

// Server matches the older zishang520/webtransport-go shape where H3 was a
// value field. The upstream module changed it to *http3.Server in v0.10.0.
type Server struct {
	H3                   http3.Server
	ApplicationProtocols []string
	ReorderingTimeout    time.Duration
	CheckOrigin          func(r *http.Request) bool

	upstream *upstream.Server
}

func (s *Server) delegate() *upstream.Server {
	if s.upstream == nil {
		s.upstream = &upstream.Server{H3: &s.H3}
	}
	s.upstream.ApplicationProtocols = s.ApplicationProtocols
	s.upstream.ReorderingTimeout = s.ReorderingTimeout
	s.upstream.CheckOrigin = s.CheckOrigin
	return s.upstream
}

func (s *Server) Serve(conn net.PacketConn) error {
	return s.delegate().Serve(conn)
}

func (s *Server) ServeQUICConn(conn *quic.Conn) error {
	return s.delegate().ServeQUICConn(conn)
}

func (s *Server) ListenAndServe() error {
	return s.delegate().ListenAndServe()
}

func (s *Server) ListenAndServeTLS(certFile, keyFile string) error {
	return s.delegate().ListenAndServeTLS(certFile, keyFile)
}

func (s *Server) Close() error {
	if s.upstream != nil {
		return s.upstream.Close()
	}
	return s.H3.Close()
}

func (s *Server) Upgrade(w http.ResponseWriter, r *http.Request) (*Session, error) {
	return s.delegate().Upgrade(w, r)
}
