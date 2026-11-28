package handlers

import (
	"context"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// connectionReceiptLifetimeV3 is how long a connection receipt stays valid.
const connectionReceiptLifetimeV3 = 2 * time.Minute

// The two refusals of a probe result's connection receipt. An enrolled device
// must send the receipt of the host that served its probe, and a receipt that
// does not belong to the account and device sending it is not one.
var (
	ErrProbeReceiptMissing = errors.New("connection receipt missing")
	ErrProbeReceiptInvalid = errors.New("connection receipt invalid")
)

// ProbeConnectionReceipt returns the Silo-Connection-Receipt value for probe
// bytes the API host serves itself, or "" when it sends none.
//
// A request over TCP gets the regular class, even when the device also holds a
// live direct path. A request over the direct path gets the direct class only
// when the peer's node key is enrolled to this account and device, and the API
// host's path to the device is live and confirmed, and it is still the path the
// request arrived on; otherwise no receipt is sent.
func (h *PlaybackHandler) ProbeConnectionReceipt(ctx context.Context, user int, device string) string {
	device = connectionDeviceV3(device)
	if user <= 0 || device == "" || h.JWTSecret == "" {
		return ""
	}
	claims := streamtoken.Claims{
		PlayMethod: streamtoken.PlayMethodConnectionReceipt, UserID: user, DeviceID: device,
		Host: streamtoken.ConnectionReceiptHostAPI, ConnectionClass: streamtoken.ConnectionClassRegular,
	}
	if peer := netaccess.PathFromContext(ctx).Direct; peer != nil {
		if h.DirectState == nil || peer.UserID != user || peer.DeviceID != device {
			return ""
		}
		state, enrolled, err := h.DirectState.Device(ctx, user, device)
		if err != nil || !enrolled || !state.APIReady || state.ConnectionKey == "" || state.PathGeneration == 0 {
			return ""
		}
		// The receipt names the path the request arrived on. A path replaced
		// since then is not the one the probe bytes measured. An arrival the host
		// gave no generation has nothing to compare.
		if peer.PathGeneration != 0 && peer.PathGeneration != state.PathGeneration {
			return ""
		}
		claims.ConnectionClass, claims.ConnectionKey, claims.PathGeneration = streamtoken.ConnectionClassDirect, state.ConnectionKey, state.PathGeneration
	}
	receipt, err := streamtoken.Sign(claims, h.JWTSecret, connectionReceiptLifetimeV3)
	if err != nil {
		return ""
	}
	return receipt
}

// probeResultKeyV3 is the connection key a reported probe result belongs to,
// or "" when it is recorded under none. ErrProbeReceiptMissing and
// ErrProbeReceiptInvalid refuse the result; any other error is a failed read.
//
// A device that holds no enrollment and sends no receipt keeps today's regular
// key. Every other result is placed by its receipt: a regular receipt under the
// regular key of the result request, a direct receipt under the receipt's key,
// and, on the replica that runs the engine, only while the API host's path
// generation is still the receipt's. A replica without the engine records a
// valid direct receipt without that check. An expired receipt, and a direct
// receipt of an earlier path on the replica that runs the engine, record
// nothing.
func (h *PlaybackHandler) probeResultKeyV3(ctx context.Context, user int, device, receipt string) (string, error) {
	regular := func() string {
		key, _ := connectionstore.Key(ctx, h.JWTSecret, connectionstore.ClassRegular, "")
		return key
	}
	if receipt == "" {
		if h.DirectState == nil || device == "" {
			return regular(), nil
		}
		enrolled, err := h.DirectState.Enrolled(ctx, user, device)
		if err != nil {
			return "", err //nolint:wrapcheck // The caller answers a failed read as unavailable.
		}
		if enrolled {
			return "", ErrProbeReceiptMissing
		}
		return regular(), nil
	}
	claims, err := streamtoken.Verify(receipt, h.JWTSecret)
	if errors.Is(err, jwt.ErrTokenExpired) {
		return "", nil
	}
	if err != nil || !validConnectionReceiptV3(claims, user, device) {
		return "", ErrProbeReceiptInvalid
	}
	if claims.ConnectionClass == streamtoken.ConnectionClassRegular {
		return regular(), nil
	}
	if h.DirectState == nil || claims.Host != streamtoken.ConnectionReceiptHostAPI {
		return "", nil
	}
	// The engine runs on one replica, and any replica can get the result. Only
	// the replica that runs the engine knows the path's generation, so another
	// replica records the result under the receipt's key: the key is a keyed
	// hash of the address the probe measured, and the sample stays filed under
	// that path.
	engineRunning, generation := h.DirectState.PathObservation(user, device)
	if engineRunning && generation != claims.PathGeneration {
		return "", nil
	}
	return claims.ConnectionKey, nil
}

// validConnectionReceiptV3 reports whether claims are a well-formed receipt for
// this account and device: a regular one carries no key or generation, and a
// direct one carries both.
func validConnectionReceiptV3(claims *streamtoken.Claims, user int, device string) bool {
	if claims.PlayMethod != streamtoken.PlayMethodConnectionReceipt || claims.UserID != user || device == "" || claims.DeviceID != device || claims.Host == "" {
		return false
	}
	switch claims.ConnectionClass {
	case streamtoken.ConnectionClassRegular:
		return claims.ConnectionKey == "" && claims.PathGeneration == 0
	case streamtoken.ConnectionClassDirect:
		return claims.ConnectionKey != "" && claims.PathGeneration != 0
	}
	return false
}
