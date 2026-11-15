package nodecontrol

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/Silo-Server/silo-server/internal/fspath"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/nodesource"
)

// PathSources is the source bytes route: a core-access node reads the bytes
// of a source it holds a grant for here (nodesource.PathSources).
const PathSources = nodesource.PathSources

// ignoredSourceConditions are the conditional request headers a source read
// drops. If-Match on the grant's entity tag is the only condition a node may
// set, and it can only refuse the request. The others could turn a range into
// the whole body (If-Range) or the body into nothing (If-None-Match,
// If-Modified-Since), and a condition may only narrow the answer to the
// granted revision.
var ignoredSourceConditions = []string{headerIfNoneMatch, "If-Modified-Since", "If-Unmodified-Since", "If-Range"}

// Outcomes of a source read, the outcome label of
// silo_node_source_stream_ends_total. A read that streamed ends as
// httpstream's completed, client_gone or stalled_reap.
const (
	sourceOutcomeForbidden   = "forbidden"
	sourceOutcomeGone        = "gone"
	sourceOutcomeChanged     = "changed"
	sourceOutcomeUnavailable = "unavailable"
	sourceOutcomeRange       = "range_not_satisfiable"
)

// Source read metrics, labeled by node so an operator can see what each
// core-access node costs the API servers before moving more nodes to it.
var (
	nodeSourceActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "silo_node_source_streams_active",
		Help: "Source byte reads this API server is serving to core-access transcode nodes, by node id.",
	}, []string{"node"})
	nodeSourceBytes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "silo_node_source_bytes_sent_total",
		Help: "Source bytes this API server sent to core-access transcode nodes, by node id.",
	}, []string{"node"})
	nodeSourceEnds = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "silo_node_source_stream_ends_total",
		Help: "Source byte reads by terminal outcome: completed, client_gone, stalled_reap, or a refusal (forbidden, gone, changed, range_not_satisfiable, unavailable).",
	}, []string{"outcome"})
)

// HandleSource serves GET and HEAD /api/internal/nodes/sources: the bytes of
// the source a grant approves, to the node it names, with the grant in the
// X-Silo-Source-Grant header (which the node-to-core signature covers). It
// reads no catalog: the catalog is checked when the source opens and at every
// renewal, so it is at most one renewal interval stale here.
//
// The core opens the granted path through fspath, without blocking on a FIFO,
// fstats the descriptor and serves from it, so a file replaced after the
// check is never served. Single and suffix ranges and If-Match on the grant's
// entity tag are honored; other conditions are dropped. The response is
// identity-encoded and rolls its write deadline with progress instead of the
// server's absolute write timeout.
//
// 403: the grant does not verify, has expired or reached its whole life, or
// names another node. 410: the file is gone or not a regular file. 412: its
// size or modification time (at microseconds) differs from the grant, or
// If-Match names another revision. 416: the range lies past the source. 503:
// no grant key.
func (s *Server) HandleSource(w http.ResponseWriter, r *http.Request) {
	id, ok := NodeID(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	claims, status := s.verifyGrant(r.Context(), id, strings.TrimSpace(r.Header.Get(nodesource.HeaderSourceGrant)))
	if status != 0 {
		refuseSource(w, status)
		return
	}
	f, info, err := openGrantedSource(claims)
	if err != nil {
		if errors.Is(err, nodesource.ErrSourceChanged) {
			refuseSource(w, http.StatusPreconditionFailed)
		} else {
			slog.DebugContext(r.Context(), "granted source unavailable", "component", "nodecontrol", "node_id", id, "source", claims.ETag(), "error", err)
			refuseSource(w, http.StatusGone)
		}
		return
	}
	defer func() { _ = f.Close() }()
	if s.afterSourceOpen != nil {
		s.afterSourceOpen()
	}

	for _, name := range ignoredSourceConditions {
		r.Header.Del(name)
	}
	if strings.Contains(r.Header.Get(headerRange), ",") {
		// A node asks for one range at a time; a multipart answer is not
		// something its relay reads.
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", info.Size()))
		refuseSource(w, http.StatusRequestedRangeNotSatisfiable)
		return
	}
	stream := httpstream.NewRollingDeadlineWriter(w)
	header := stream.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Type", "application/octet-stream")
	header.Set("ETag", claims.ETag())
	node := strconv.Itoa(id)
	nodeSourceActive.WithLabelValues(node).Inc()
	// ServeContent answers ranges, suffix ranges, If-Match and 416 from the
	// descriptor; its body goes through the writer's ReadFrom, so sendfile
	// survives.
	http.ServeContent(&sourceResponseWriter{RollingDeadlineWriter: stream, size: info.Size()}, r, "", info.ModTime(), f)
	nodeSourceActive.WithLabelValues(node).Dec()
	nodeSourceBytes.WithLabelValues(node).Add(float64(stream.BytesWritten()))
	outcome := string(stream.Outcome(r.Context()))
	switch stream.StatusCode() {
	case http.StatusPreconditionFailed:
		outcome = sourceOutcomeChanged
	case http.StatusRequestedRangeNotSatisfiable:
		outcome = sourceOutcomeRange
	}
	nodeSourceEnds.WithLabelValues(outcome).Inc()
}

// refuseSource answers a source read the core will not serve, and counts it.
func refuseSource(w http.ResponseWriter, status int) {
	outcome := sourceOutcomeGone
	switch status {
	case http.StatusRequestedRangeNotSatisfiable:
		outcome = sourceOutcomeRange
	case http.StatusForbidden:
		outcome = sourceOutcomeForbidden
	case http.StatusPreconditionFailed:
		outcome = sourceOutcomeChanged
	case http.StatusServiceUnavailable:
		outcome = sourceOutcomeUnavailable
	}
	nodeSourceEnds.WithLabelValues(outcome).Inc()
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, http.StatusText(status), status)
}

// openGrantedSource opens the grant's path read-only through fspath, without
// hanging on a file raced into a FIFO, and checks the descriptor is a regular
// file of the granted revision: ErrSourceGone or ErrSourceChanged otherwise.
// What it returns is what is served, so a file replaced after the check
// cannot be.
func openGrantedSource(claims nodesource.Grant) (*os.File, os.FileInfo, error) {
	f, err := fspath.OpenFile(claims.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", nodesource.ErrSourceGone, err)
	}
	info, err := f.Stat()
	switch {
	case err != nil:
		err = fmt.Errorf("%w: %w", nodesource.ErrSourceGone, err)
	case !info.Mode().IsRegular():
		err = fmt.Errorf("%w: not a regular file", nodesource.ErrSourceGone)
	case !claims.Matches(info):
		err = nodesource.ErrSourceChanged
	}
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	return f, info, nil
}

// sourceResponseWriter gives a 416 the Content-Range of the whole source,
// which ServeContent leaves out when it refuses a range.
type sourceResponseWriter struct {
	*httpstream.RollingDeadlineWriter
	size int64
}

func (w *sourceResponseWriter) WriteHeader(status int) {
	if status == http.StatusRequestedRangeNotSatisfiable {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", w.size))
	}
	if status >= http.StatusBadRequest {
		// An error body is no revision of the source.
		w.Header().Del("ETag")
	}
	w.RollingDeadlineWriter.WriteHeader(status)
}
