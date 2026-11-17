package proxy

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/bandwidthprobe"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// probeWriteTimeout bounds one probe response. The standalone proxy runs
// without a server write timeout, so a client that stopped reading would
// otherwise hold its account's probe slot for good. A probe is short, so it
// gets the API listener's absolute two minutes, not a streaming stall window.
var probeWriteTimeout = 2 * time.Minute

// probeEndHook, set only by tests, runs as a probe ends and before it gives
// its slot back, so a test can observe a probe that is over but still counted.
var probeEndHook func()

func (s *Server) handleBandwidthProbe(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyToken(w, r)
	if claims == nil {
		return
	}
	if claims.PlayMethod != streamtoken.PlayMethodBandwidthProbe || claims.UserID <= 0 {
		http.Error(w, "invalid probe token", http.StatusForbidden)
		return
	}
	size, err := strconv.Atoi(r.URL.Query().Get("size"))
	if err != nil || size < 1 || size > bandwidthprobe.MaxSize {
		http.Error(w, "invalid size", http.StatusBadRequest)
		return
	}
	admitted, window, wait := s.probeLimiter.Acquire(claims.UserID, size, time.Now())
	if !admitted {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(wait.Round(time.Second)/time.Second))))
		http.Error(w, "probe rate limited", http.StatusTooManyRequests)
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(probeWriteTimeout))
	writer := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
	defer func() {
		if probeEndHook != nil {
			probeEndHook()
		}
		s.probeLimiter.Release(claims.UserID, size-writer.BytesWritten(), window, time.Now())
	}()
	streamtelemetry.Attach(r.Context(), streamtelemetry.Attachment{Subject: streamtelemetry.UserSubject(claims.UserID), StartedAtSource: streamtelemetry.StartedAtSourceFirstSeen, TokenIssuedAtSource: streamtelemetry.TokenIssuedAtSourceNone})
	if receipt := s.probeReceipt(r, claims); receipt != "" {
		w.Header().Set(streamtoken.ConnectionReceiptHeader, receipt)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(size))
	w.Header().Set("Cache-Control", "no-store")
	_ = bandwidthprobe.Write(writer, size)
}

// connectionReceiptLifetime is how long a receipt stays valid.
const connectionReceiptLifetime = 2 * time.Minute

// probeReceipt returns the connection receipt for a probe whose token names a
// device: always the regular class, because a proxy serves no direct path. It
// returns "" for a token without a device, and when this proxy cannot name
// itself or sign, so no receipt is sent and the client's result is refused.
func (s *Server) probeReceipt(r *http.Request, claims *streamtoken.Claims) string {
	if claims.ProbeDevice == "" {
		return ""
	}
	nodeID, known := s.currentNodeRowID()
	if !known || nodeID <= 0 {
		return ""
	}
	receipt, err := streamtoken.Sign(streamtoken.Claims{
		PlayMethod: streamtoken.PlayMethodConnectionReceipt, UserID: claims.UserID, DeviceID: claims.ProbeDevice,
		Host: fmt.Sprintf("node:%d", nodeID), ConnectionClass: streamtoken.ConnectionClassRegular,
	}, s.watcher.Config().Auth.JWTSecret, connectionReceiptLifetime)
	if err != nil {
		slog.WarnContext(r.Context(), "bandwidth probe receipt not signed", "component", "proxy", "error", err)
		return ""
	}
	return receipt
}
