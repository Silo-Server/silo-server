package apiv2

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/bandwidthprobe"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

const (
	opGetPlaybackBandwidthProbe = "getPlaybackBandwidthProbe"
	bandwidthProbeSizeParam     = "size"
	bandwidthProbeDefaultSize   = 1_000_000
)

// countingWriter counts the bytes written through it.
type countingWriter struct {
	w io.Writer
	n int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}

// registerPlaybackBandwidthProbe serves random bytes a client times to
// estimate its throughput to this server for bandwidth_estimate_kbps.
// Browsers cap their own downlink hint (Chromium reports at most 10 Mbps), so
// a client that wants Auto quality to pick 4K has to measure.
func registerPlaybackBandwidthProbe(reg *Registry) {
	const locationHeader = "Location"
	params := []*huma.Param{{
		Name: bandwidthProbeSizeParam, In: playbackParamQuery,
		Description: "Number of bytes to send. A client may close the connection early once it has timed enough of the transfer.",
		Schema:      &huma.Schema{Type: huma.TypeInteger, Format: playbackIntegerFormat, Minimum: new(float64(1)), Maximum: new(float64(bandwidthprobe.MaxSize)), Default: bandwidthProbeDefaultSize},
	}}
	headers := map[string]*huma.Param{playbackContentLength: {Schema: &huma.Schema{Type: huma.TypeInteger, Format: playbackIntegerFormat}}, playbackCacheControlHeader: {Schema: &huma.Schema{Type: huma.TypeString}}}
	responses := map[string]*huma.Response{
		"307": {Description: "Selected media proxy; follow the short-lived probe URL", Headers: map[string]*huma.Param{locationHeader: {Schema: &huma.Schema{Type: huma.TypeString}}}},
		"200": {Description: "Random bytes", Content: map[string]*huma.MediaType{playbackMediaBinary: {Schema: &huma.Schema{Type: huma.TypeString, Format: playbackBinaryFormat}}}, Headers: headers},
		"422": {Description: http.StatusText(http.StatusUnprocessableEntity), Content: map[string]*huma.MediaType{problemContentType: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[Problem](), true, "")}}},
	}
	op := humaOp(http.MethodGet, Prefix+"/playback/bandwidth-probe", opGetPlaybackBandwidthProbe, playbackTag,
		"Download random bytes to measure throughput to this server. Clients time the transfer and send the result as bandwidth_estimate_kbps; the bytes count as transfer egress, not playback. A server runs at most 4 probes at once; an account may run two of them and draw up to 2,000,000,000 bytes per 10 minutes. Beyond any limit the request gets 429 with Retry-After.")
	op.Parameters = append(params, &huma.Param{Name: "media_path", In: playbackParamQuery, Description: "Use the client media egress route.", Schema: &huma.Schema{Type: huma.TypeBoolean}})
	op.Responses = responses
	raw := RawOperation{Operation: Operation{Operation: op, Class: ClassAuthenticated}, Protocol: "bandwidth-probe",
		Reason: "A throughput measurement needs an unbuffered, uncompressible byte stream the client can time and abandon mid-transfer."}
	limiter := bandwidthprobe.NewLimiter()
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
		mediaPath := false
		if value := r.URL.Query().Get("media_path"); value != "" {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				writeProblem(w, r, validationProblem("query.media_path", codeInvalid, "Expected a boolean."))
				return
			}
			mediaPath = parsed
		}
		if mediaPath {
			service, ok := reg.deps.Playback.(interface {
				PlaybackProbeRedirect(context.Context, int, int) (string, error)
			})
			if !ok {
				writeProblem(w, r, unavailable("media path probe"))
				return
			}
			target, err := service.PlaybackProbeRedirect(r.Context(), claims.UserID, size)
			if err != nil {
				writeProblem(w, r, playbackProblem(err))
				return
			}
			if target != "" {
				w.Header().Set("Cache-Control", "no-store")
				http.Redirect(w, r, target, http.StatusTemporaryRedirect)
				return
			}
		}
		admitted, window, wait := limiter.Acquire(claims.UserID, size, time.Now())
		if !admitted {
			writeProblem(w, r, NewProblem(TypeRateLimited, "This account has reached its bandwidth probe limit on this server.").WithRetryAfter(max(1, int(wait.Round(time.Second)/time.Second))))
			return
		}
		sent := &countingWriter{w: w}
		defer func() { limiter.Release(claims.UserID, size-sent.n, window, time.Now()) }()
		streamtelemetry.Attach(r.Context(), streamtelemetry.Attachment{Subject: streamtelemetry.UserSubject(claims.UserID),
			StartedAtSource: streamtelemetry.StartedAtSourceFirstSeen, TokenIssuedAtSource: streamtelemetry.TokenIssuedAtSourceNone})
		w.Header().Set("Content-Type", playbackMediaBinary)
		w.Header().Set(playbackContentLength, strconv.Itoa(size))
		w.Header().Set(playbackCacheControlHeader, "no-store")
		w.WriteHeader(http.StatusOK)
		_ = bandwidthprobe.Write(sent, size)
	})
	if reg.deps.ObserveBandwidthProbe != nil {
		handler = reg.deps.ObserveBandwidthProbe(handler)
	}
	RegisterRaw(reg, raw, handler)
}
