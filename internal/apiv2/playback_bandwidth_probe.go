package apiv2

import (
	"io"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"time"

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
	// bandwidthProbeWindowBytes bounds the probe bytes one account may draw
	// from this server per bandwidthProbeWindow. The web client sends at most
	// 40 MB about every 30 minutes, so the budget only stops an account that
	// probes back to back.
	bandwidthProbeWindowBytes = 400_000_000
	bandwidthProbeWindow      = 10 * time.Minute
	// bandwidthProbeNodeMaxInFlight bounds probes on this server across all
	// accounts, so many viewers signing in together cannot tie up its CPU and
	// egress with probes. A refused client tries again a few minutes later.
	bandwidthProbeNodeMaxInFlight = 32
	bandwidthProbeNodeRetryAfter  = 30 * time.Second
)

// bandwidthProbeLimiter bounds probes on this server: how many run at once
// overall and per account, and how many bytes each account's probes send per
// window. It is per node on purpose; what it protects (sockets, goroutines,
// the random-byte work) is per node too.
type bandwidthProbeLimiter struct {
	mu       sync.Mutex
	accounts map[int]*bandwidthProbeAccount
	// inFlight counts probes running on this server across all accounts.
	inFlight int
}

type bandwidthProbeAccount struct {
	inFlight    int
	windowStart time.Time
	// windowBytes is what the window has granted: sizes reserved by running
	// probes plus bytes finished probes actually sent.
	windowBytes int
}

// acquire reserves a probe slot and size bytes of the account's window. It
// returns the window the reservation belongs to, for release, or how long to
// wait before retrying when the account is over either limit.
func (l *bandwidthProbeLimiter) acquire(userID, size int, now time.Time) (bool, time.Time, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Forget accounts with nothing running whose window has passed. Probes are
	// rare (one per client about every 30 minutes), so a sweep per admission
	// is cheap and keeps the map to the accounts probing now.
	for id, idle := range l.accounts {
		if idle.inFlight <= 0 && now.Sub(idle.windowStart) >= bandwidthProbeWindow {
			delete(l.accounts, id)
		}
	}
	account := l.accounts[userID]
	if account == nil {
		account = &bandwidthProbeAccount{windowStart: now}
		l.accounts[userID] = account
	}
	if now.Sub(account.windowStart) >= bandwidthProbeWindow {
		account.windowStart, account.windowBytes = now, 0
	}
	if l.inFlight >= bandwidthProbeNodeMaxInFlight {
		return false, time.Time{}, bandwidthProbeNodeRetryAfter
	}
	if account.inFlight >= bandwidthProbeMaxInFlight {
		return false, time.Time{}, 5 * time.Second
	}
	if account.windowBytes+size > bandwidthProbeWindowBytes {
		return false, time.Time{}, account.windowStart.Add(bandwidthProbeWindow).Sub(now)
	}
	l.inFlight++
	account.inFlight++
	account.windowBytes += size
	return true, account.windowStart, 0
}

// release frees the slot and refunds the bytes a probe reserved but did not
// send, as when the client closed the connection once it had timed enough.
// The refund only goes back to the window that granted it; a later window
// never held the reservation.
func (l *bandwidthProbeLimiter) release(userID, unsent int, granted, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inFlight--
	account := l.accounts[userID]
	if account == nil {
		return
	}
	account.inFlight--
	if account.windowStart.Equal(granted) {
		account.windowBytes = max(0, account.windowBytes-unsent)
	}
	if account.inFlight <= 0 && now.Sub(account.windowStart) >= bandwidthProbeWindow {
		delete(l.accounts, userID)
	}
}

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
		"Download random bytes to measure throughput to this server. Clients time the transfer and send the result as bandwidth_estimate_kbps; the bytes count as transfer egress, not playback. A server runs at most 32 probes at once; an account may run two of them and draw up to 400 MB per 10 minutes. Beyond any limit the request gets 429 with Retry-After.")
	op.Parameters = params
	op.Responses = responses
	raw := RawOperation{Operation: Operation{Operation: op, Class: ClassAuthenticated}, Protocol: "bandwidth-probe",
		Reason: "A throughput measurement needs an unbuffered, uncompressible byte stream the client can time and abandon mid-transfer."}
	limiter := &bandwidthProbeLimiter{accounts: map[int]*bandwidthProbeAccount{}}
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
		admitted, window, wait := limiter.acquire(claims.UserID, size, time.Now())
		if !admitted {
			writeProblem(w, r, NewProblem(TypeRateLimited, "This account has reached its bandwidth probe limit on this server.").WithRetryAfter(max(1, int(wait.Round(time.Second)/time.Second))))
			return
		}
		sent := &countingWriter{w: w}
		defer func() { limiter.release(claims.UserID, size-sent.n, window, time.Now()) }()
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
