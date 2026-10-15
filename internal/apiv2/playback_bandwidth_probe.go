package apiv2

import (
	"net/http"
	"reflect"
	"strconv"
	"sync"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/bandwidthprobe"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

const (
	opGetPlaybackBandwidthProbe = "getPlaybackBandwidthProbe"
	bandwidthProbeSizeParam     = "size"
	bandwidthProbeDefaultSize   = 1_000_000
	// bandwidthProbeMaxInFlight bounds the probes one account can run at once
	// on this server. A client needs one at a time; the request rate limiter
	// counts requests, not the bytes each one streams.
	bandwidthProbeMaxInFlight = 2
)

// bandwidthProbeLimiter counts the probes each account has in flight.
type bandwidthProbeLimiter struct {
	mu       sync.Mutex
	inFlight map[int]int
}

func (l *bandwidthProbeLimiter) acquire(userID int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight[userID] >= bandwidthProbeMaxInFlight {
		return false
	}
	l.inFlight[userID]++
	return true
}

func (l *bandwidthProbeLimiter) release(userID int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight[userID] <= 1 {
		delete(l.inFlight, userID)
		return
	}
	l.inFlight[userID]--
}

// registerPlaybackBandwidthProbe serves random bytes a client times to
// estimate its throughput to this server for bandwidth_estimate_kbps.
// Browsers cap their own downlink hint (Chromium reports at most 10 Mbps), so
// a client that wants Auto quality to pick 4K has to measure.
func registerPlaybackBandwidthProbe(reg *Registry) {
	params := []*huma.Param{{
		Name: bandwidthProbeSizeParam, In: playbackParamQuery,
		Description: "Number of bytes to send. A client may close the connection early once it has timed enough of the transfer.",
		Schema:      &huma.Schema{Type: huma.TypeInteger, Format: playbackIntegerFormat, Minimum: new(float64(1)), Maximum: new(float64(bandwidthprobe.MaxSize)), Default: bandwidthProbeDefaultSize},
	}}
	headers := map[string]*huma.Param{playbackContentLength: {Schema: &huma.Schema{Type: huma.TypeInteger, Format: playbackIntegerFormat}}, playbackCacheControlHeader: {Schema: &huma.Schema{Type: huma.TypeString}}}
	responses := map[string]*huma.Response{
		"200": {Description: "Random bytes", Content: map[string]*huma.MediaType{playbackMediaBinary: {Schema: &huma.Schema{Type: huma.TypeString, Format: playbackBinaryFormat}}}, Headers: headers},
		"422": {Description: http.StatusText(http.StatusUnprocessableEntity), Content: map[string]*huma.MediaType{problemContentType: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[Problem](), true, "")}}},
	}
	op := humaOp(http.MethodGet, Prefix+"/playback/bandwidth-probe", opGetPlaybackBandwidthProbe, playbackTag,
		"Download random bytes to measure throughput to this server. Clients time the transfer and send the result as bandwidth_estimate_kbps; the bytes count as transfer egress, not playback. An account may run two probes at once on a server; a third is refused with 429.")
	op.Parameters = params
	op.Responses = responses
	raw := RawOperation{Operation: Operation{Operation: op, Class: ClassAuthenticated}, Protocol: "bandwidth-probe",
		Reason: "A throughput measurement needs an unbuffered, uncompressible byte stream the client can time and abandon mid-transfer."}
	limiter := &bandwidthProbeLimiter{inFlight: map[int]int{}}
	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size := bandwidthProbeDefaultSize
		if value := r.URL.Query().Get(bandwidthProbeSizeParam); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > bandwidthprobe.MaxSize {
				writeProblem(w, r, validationProblem("query.size", codeInvalid, "Expected an integer from 1 to "+strconv.Itoa(bandwidthprobe.MaxSize)+"."))
				return
			}
			size = parsed
		}
		claims := claimsFrom(r.Context())
		if claims == nil {
			writeProblem(w, r, NewProblem(TypeAuthenticationRequired, "Authentication is required."))
			return
		}
		if !limiter.acquire(claims.UserID) {
			writeProblem(w, r, NewProblem(TypeRateLimited, "This account already has bandwidth probes running.").WithRetryAfter(5))
			return
		}
		defer limiter.release(claims.UserID)
		streamtelemetry.Attach(r.Context(), streamtelemetry.Attachment{Subject: streamtelemetry.UserSubject(claims.UserID),
			StartedAtSource: streamtelemetry.StartedAtSourceFirstSeen, TokenIssuedAtSource: streamtelemetry.TokenIssuedAtSourceNone})
		w.Header().Set("Content-Type", playbackMediaBinary)
		w.Header().Set(playbackContentLength, strconv.Itoa(size))
		w.Header().Set(playbackCacheControlHeader, "no-store")
		w.WriteHeader(http.StatusOK)
		_ = bandwidthprobe.Write(w, size)
	})
	if reg.deps.ObserveBandwidthProbe != nil {
		handler = reg.deps.ObserveBandwidthProbe(handler)
	}
	RegisterRaw(reg, raw, handler)
}
