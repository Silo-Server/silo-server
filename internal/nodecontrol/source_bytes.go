package nodecontrol

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
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
// httpstream's completed, client_gone or stalled_reap, or as shutdown when the
// API server cut it as it shut down.
const (
	sourceOutcomeForbidden   = "forbidden"
	sourceOutcomeGone        = "gone"
	sourceOutcomeChanged     = "changed"
	sourceOutcomeUnavailable = "unavailable"
	sourceOutcomeRange       = "range_not_satisfiable"
	sourceOutcomeThrottled   = "throttled"
	sourceOutcomeShutdown    = "shutdown"
)

// maxSourceStreamsPerNode caps the source byte responses one API replica
// serves one node at once. Each holds a file descriptor and a goroutine, and
// the route sits outside the per-node request budgets, so without a cap a
// node key and one grant could hold enough slow reads open to run the
// replica out of descriptors for every route. Legitimate use is far below
// it: a node's Go readers keep at most 128 fetches in flight per process
// (nodesource's readerFetchSlots), and each FFmpeg process holds one relayed
// read, two while it seeks, so 32 concurrent jobs and 16 trickplay, chapter
// and sampling runs hold about 96 more. 128 + 96 = 224, and the cap leaves
// more than twice that. A node over it gets 429, which its fetch loop backs
// off from and retries (250 ms doubling to 4 s, for up to 30 seconds without
// progress), so a burst that briefly crosses it only delays reads.
const maxSourceStreamsPerNode = 512

// sourceRetryAfter is the Retry-After of a 429, in seconds.
const sourceRetryAfter = "1"

// Source read metrics, labeled by node so an operator can see what each
// core-access node costs the API servers before moving more nodes to it.
var (
	nodeSourceActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "silo_node_source_streams_active",
		Help: "Source byte reads this API server is serving to core-access transcode nodes, by node id.",
	}, []string{"node"})
	nodeSourceBytes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "silo_node_source_bytes_sent_total",
		Help: "Source bytes this API server sent to core-access transcode nodes, by node id, counted as they are sent.",
	}, []string{"node"})
	nodeSourceEnds = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "silo_node_source_stream_ends_total",
		Help: "Source byte reads by terminal outcome: completed, client_gone, stalled_reap, shutdown, or a refusal (forbidden, gone, changed, range_not_satisfiable, throttled, unavailable).",
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
// server's absolute write timeout. It is cut when the API server's Lifetime
// ends; the node resumes the read from its offset.
//
// 403: the grant does not verify, has expired or reached its whole life, or
// names another node. 410: the file is gone or not a regular file. 412: its
// size or modification time (at microseconds) differs from the grant, or
// If-Match names another revision. 416: the range lies past the source. 429:
// the node has maxSourceStreamsPerNode responses in flight here. 503: no
// grant key, or the API server is shutting down. Every answer is no-store.
func (s *Server) HandleSource(w http.ResponseWriter, r *http.Request) {
	id, ok := NodeID(r.Context())
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.opts.Lifetime != nil && s.opts.Lifetime.Err() != nil {
		refuseSource(w, http.StatusServiceUnavailable)
		return
	}
	if !s.sourceStreams.acquire(id, s.sourceStreamLimit) {
		w.Header().Set("Retry-After", sourceRetryAfter)
		refuseSource(w, http.StatusTooManyRequests)
		return
	}
	defer s.sourceStreams.release(id)
	claims, status := s.verifyGrant(r.Context(), id, strings.TrimSpace(r.Header.Get(nodesource.HeaderSourceGrant)))
	if status != 0 {
		refuseSource(w, status)
		return
	}
	f, info, err := openGrantedSource(claims)
	if err != nil {
		status := sourceStatus(err)
		switch status {
		case http.StatusPreconditionFailed:
		case http.StatusGone:
			slog.DebugContext(r.Context(), "granted source unavailable", "component", "nodecontrol", "node_id", id, "source", claims.ETag(), "error", err)
		default:
			slog.WarnContext(r.Context(), "granted source could not be opened", "component", "nodecontrol", "node_id", id, "source", claims.ETag(), "error", err)
		}
		refuseSource(w, status)
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
		w.Header().Set("ETag", claims.ETag())
		refuseSource(w, http.StatusRequestedRangeNotSatisfiable)
		return
	}
	stream := httpstream.NewRollingDeadlineWriter(w)
	header := stream.Header()
	header.Set("Content-Type", "application/octet-stream")
	// ServeContent checks If-Match against this before it answers.
	header.Set("ETag", claims.ETag())
	node := strconv.Itoa(id)
	out := &sourceResponseWriter{RollingDeadlineWriter: stream, size: info.Size(), etag: claims.ETag(), sent: nodeSourceBytes.WithLabelValues(node)}
	if s.opts.Lifetime != nil {
		// A node's relay resumes a cut read against any replica, so ending
		// the response costs nothing, while waiting for it would hold the
		// shutdown until its timeout.
		stop := context.AfterFunc(s.opts.Lifetime, func() { _ = stream.Abort() })
		defer stop()
	}
	nodeSourceActive.WithLabelValues(node).Inc()
	// ServeContent answers ranges, suffix ranges, If-Match and 416 from the
	// descriptor; its body goes through the writer's ReadFrom, so sendfile
	// survives.
	http.ServeContent(out, r, "", info.ModTime(), f)
	nodeSourceActive.WithLabelValues(node).Dec()
	outcome := string(stream.Outcome(r.Context()))
	switch {
	case stream.StatusCode() == http.StatusPreconditionFailed:
		outcome = sourceOutcomeChanged
	case stream.StatusCode() == http.StatusRequestedRangeNotSatisfiable:
		outcome = sourceOutcomeRange
	case outcome != string(httpstream.OutcomeCompleted) && s.opts.Lifetime != nil && s.opts.Lifetime.Err() != nil:
		outcome = sourceOutcomeShutdown
	}
	nodeSourceEnds.WithLabelValues(outcome).Inc()
}

// refuseSource answers a source read the core will not serve, and counts it.
func refuseSource(w http.ResponseWriter, status int) {
	outcome := sourceOutcomeGone
	switch status {
	case http.StatusTooManyRequests:
		outcome = sourceOutcomeThrottled
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
// file of the granted revision: ErrSourceGone or ErrSourceChanged otherwise,
// or the open's own error when the file could not be examined. What it
// returns is what is served, so a file replaced after the check cannot be.
func openGrantedSource(claims nodesource.Grant) (*os.File, os.FileInfo, error) {
	f, err := fspath.OpenFile(claims.Path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, nodesource.SourceError(err)
	}
	info, err := f.Stat()
	switch {
	case err != nil:
		err = nodesource.SourceError(err)
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

// sourceResponseWriter keeps the route's headers on every answer and counts
// the body into silo_node_source_bytes_sent_total as it is sent. ServeContent
// drops Cache-Control and ETag when it answers an error, and leaves out the
// whole size on a 416: every answer is no-store, a 416 carries the granted
// revision's ETag and size, and another error carries no ETag, since its body
// is no revision of the source.
type sourceResponseWriter struct {
	*httpstream.RollingDeadlineWriter
	size int64
	etag string
	sent prometheus.Counter
}

func (w *sourceResponseWriter) WriteHeader(status int) {
	header := w.Header()
	header.Set("Cache-Control", "no-store")
	switch {
	case status == http.StatusRequestedRangeNotSatisfiable:
		header.Set("Content-Range", fmt.Sprintf("bytes */%d", w.size))
		header.Set("ETag", w.etag)
	case status >= http.StatusBadRequest:
		header.Del("ETag")
	default:
		header.Set("ETag", w.etag)
	}
	w.RollingDeadlineWriter.WriteHeader(status)
}

func (w *sourceResponseWriter) Write(p []byte) (int, error) {
	n, err := w.RollingDeadlineWriter.Write(p)
	w.sent.Add(float64(n))
	return n, err
}

// ReadFrom keeps the sendfile path of the writer below and counts each slice
// of at most httpstream.ReadFromChunkDefault bytes as it lands, so the
// counter moves while a long read runs.
func (w *sourceResponseWriter) ReadFrom(r io.Reader) (int64, error) {
	return httpstream.ForwardReadFrom(w.RollingDeadlineWriter, w, r, httpstream.ReadFromChunkDefault, func(n int64, _ error) {
		w.sent.Add(float64(n))
	})
}

// nodeStreams counts responses in flight per node.
type nodeStreams struct {
	mu     sync.Mutex
	active map[int]int
}

// acquire counts one more response for node unless it has limit already.
func (c *nodeStreams) acquire(node, limit int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active[node] >= limit {
		return false
	}
	if c.active == nil {
		c.active = make(map[int]int)
	}
	c.active[node]++
	return true
}

// release ends one of node's responses.
func (c *nodeStreams) release(node int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active[node]--; c.active[node] <= 0 {
		delete(c.active, node)
	}
}
