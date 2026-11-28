package handlers

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/directconn"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

const receiptSecretV3 = "receipt-test-secret"

// signReceiptV3 signs a receipt with the handler's secret unless claims name a
// different one through secret.
func signReceiptV3(t *testing.T, secret string, claims streamtoken.Claims, ttl time.Duration) string {
	t.Helper()
	receipt, err := streamtoken.Sign(claims, secret, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

// receiptUserV3 inserts an account and returns its ID.
func receiptUserV3(t *testing.T, pool *pgxpool.Pool, name string) int {
	t.Helper()
	var user int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username, role) VALUES ($1, 'user') RETURNING id`, fmt.Sprintf("%s-%d", name, time.Now().UnixNano())).Scan(&user); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.WithoutCancel(t.Context()), `DELETE FROM users WHERE id = $1`, user) })
	return user
}

// receiptRequestV3 is a probe result request from a remote address.
func receiptRequestV3(user int) context.Context {
	return clientip.SetContext(apimw.SetClaims(context.Background(), &auth.Claims{UserID: user, Role: "user", TokenType: auth.TokenTypeAccess}), "203.0.113.7")
}

// recordedKeysV3 lists the connection keys that hold a probe reading for the
// device.
func recordedKeysV3(t *testing.T, pool *pgxpool.Pool, user int, device string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT connection_key FROM playback_connections WHERE user_id = $1 AND device_id = $2 AND probe_kbps IS NOT NULL ORDER BY connection_key`, user, device)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return keys
}

// TestProbeReceiptRecordsUnderReceiptKeyDB: a probe result is recorded under
// the key its receipt names, and under nothing when the receipt is refused,
// expired, or of a path that has been replaced.
func TestProbeReceiptRecordsUnderReceiptKeyDB(t *testing.T) {
	pool := connectionTestPool(t)
	user := receiptUserV3(t, pool, "probe-receipt")
	state := &directStateV3{}
	h := &PlaybackHandler{Connections: &connectionstore.Store{Pool: pool}, JWTSecret: receiptSecretV3, DirectState: state}
	request := receiptRequestV3(user)
	regularKey, _ := connectionstore.Key(request, receiptSecretV3, connectionstore.ClassRegular, "")
	if regularKey == "" {
		t.Fatal("the test request has no regular key")
	}
	const directKey = "direct:receipt-path-1"
	regular := streamtoken.Claims{PlayMethod: streamtoken.PlayMethodConnectionReceipt, UserID: user, Host: "node:7", ConnectionClass: streamtoken.ConnectionClassRegular}
	direct := streamtoken.Claims{PlayMethod: streamtoken.PlayMethodConnectionReceipt, UserID: user, Host: streamtoken.ConnectionReceiptHostAPI,
		ConnectionClass: streamtoken.ConnectionClassDirect, ConnectionKey: directKey, PathGeneration: 7}
	with := func(c streamtoken.Claims, device string, edit func(*streamtoken.Claims)) streamtoken.Claims {
		c.DeviceID = device
		if edit != nil {
			edit(&c)
		}
		return c
	}
	result := connectionstore.ProbeResult{Kbps: 48000, Bytes: 12_000_000, DurationMS: 2000, Transport: "wifi"}

	for _, tc := range []struct {
		name          string
		enrolled      bool
		engineRunning bool
		generation    uint64
		receipt       func(device string) string
		wantErr       error
		wantKeys      []string
	}{
		{name: "never enrolled, no receipt", engineRunning: true, receipt: func(string) string { return "" }, wantKeys: []string{regularKey}},
		{name: "enrolled, no receipt", enrolled: true, engineRunning: true, receipt: func(string) string { return "" }, wantErr: ErrProbeReceiptMissing},
		{name: "bad signature", enrolled: true, engineRunning: true, receipt: func(d string) string {
			return signReceiptV3(t, "another-secret", with(regular, d, nil), time.Minute)
		}, wantErr: ErrProbeReceiptInvalid},
		{name: "not a token", enrolled: true, engineRunning: true, receipt: func(string) string { return "not-a-receipt" }, wantErr: ErrProbeReceiptInvalid},
		{name: "another account", enrolled: true, engineRunning: true, receipt: func(d string) string {
			return signReceiptV3(t, receiptSecretV3, with(regular, d, func(c *streamtoken.Claims) { c.UserID = user + 1 }), time.Minute)
		}, wantErr: ErrProbeReceiptInvalid},
		{name: "another device", enrolled: true, engineRunning: true, receipt: func(string) string {
			return signReceiptV3(t, receiptSecretV3, with(regular, "another-device", nil), time.Minute)
		}, wantErr: ErrProbeReceiptInvalid},
		{name: "another token purpose", enrolled: true, engineRunning: true, receipt: func(d string) string {
			return signReceiptV3(t, receiptSecretV3, with(regular, d, func(c *streamtoken.Claims) { c.PlayMethod = streamtoken.PlayMethodBandwidthProbe }), time.Minute)
		}, wantErr: ErrProbeReceiptInvalid},
		{name: "direct receipt without a key", enrolled: true, engineRunning: true, generation: 7, receipt: func(d string) string {
			return signReceiptV3(t, receiptSecretV3, with(direct, d, func(c *streamtoken.Claims) { c.ConnectionKey = "" }), time.Minute)
		}, wantErr: ErrProbeReceiptInvalid},
		{name: "regular receipt with a key", enrolled: true, engineRunning: true, receipt: func(d string) string {
			return signReceiptV3(t, receiptSecretV3, with(regular, d, func(c *streamtoken.Claims) { c.ConnectionKey = directKey }), time.Minute)
		}, wantErr: ErrProbeReceiptInvalid},
		{name: "expired receipt", enrolled: true, engineRunning: true, generation: 7, receipt: func(d string) string {
			return signReceiptV3(t, receiptSecretV3, with(direct, d, nil), -time.Minute)
		}},
		{name: "direct receipt of an earlier path", enrolled: true, engineRunning: true, generation: 8, receipt: func(d string) string {
			return signReceiptV3(t, receiptSecretV3, with(direct, d, nil), time.Minute)
		}},
		{name: "direct receipt, no live path", enrolled: true, engineRunning: true, generation: 0, receipt: func(d string) string {
			return signReceiptV3(t, receiptSecretV3, with(direct, d, nil), time.Minute)
		}},
		{name: "valid direct receipt", enrolled: true, engineRunning: true, generation: 7, receipt: func(d string) string {
			return signReceiptV3(t, receiptSecretV3, with(direct, d, nil), time.Minute)
		}, wantKeys: []string{directKey}},
		{name: "valid regular receipt", enrolled: true, engineRunning: true, receipt: func(d string) string {
			return signReceiptV3(t, receiptSecretV3, with(regular, d, nil), time.Minute)
		}, wantKeys: []string{regularKey}},
		{name: "valid direct receipt, no engine on this replica", enrolled: true, engineRunning: false, generation: 0, receipt: func(d string) string {
			return signReceiptV3(t, receiptSecretV3, with(direct, d, nil), time.Minute)
		}, wantKeys: []string{directKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			device := "receipt-" + strings.ReplaceAll(tc.name, " ", "-")
			state.enrolled, state.engineRunning, state.generation = tc.enrolled, tc.engineRunning, tc.generation
			err := h.ReportPlaybackProbe(request, device, result, tc.receipt(device))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("report = %v, want %v", err, tc.wantErr)
			}
			if got := recordedKeysV3(t, pool, user, device); !equalStringsV3(got, tc.wantKeys) {
				t.Fatalf("recorded under %q, want %q", got, tc.wantKeys)
			}
		})
	}

	// A valid regular receipt keys by the result request's address, whatever
	// direct path the request itself arrived on.
	t.Run("regular receipt on a direct-path request", func(t *testing.T) {
		const device = "receipt-regular-over-direct"
		state.enrolled, state.generation = true, 7
		onDirect := netaccess.WithPath(request, netaccess.Path{Direct: &netaccess.DirectPath{HostID: "api", PeerKey: "nodekey:peer", UserID: user, DeviceID: device, Class: "direct"}})
		if err := h.ReportPlaybackProbe(onDirect, device, result, signReceiptV3(t, receiptSecretV3, with(regular, device, nil), time.Minute)); err != nil {
			t.Fatal(err)
		}
		if got := recordedKeysV3(t, pool, user, device); !equalStringsV3(got, []string{regularKey}) {
			t.Fatalf("recorded under %q, want the regular key only", got)
		}
	})
}

// directReceiptV3 is a direct receipt of the API host for one device, the
// shape probeResultKeyV3 accepts.
func directReceiptV3(t *testing.T, user int, device string, edit func(*streamtoken.Claims), ttl time.Duration) string {
	t.Helper()
	claims := streamtoken.Claims{PlayMethod: streamtoken.PlayMethodConnectionReceipt, UserID: user, DeviceID: device, Host: streamtoken.ConnectionReceiptHostAPI,
		ConnectionClass: streamtoken.ConnectionClassDirect, ConnectionKey: "direct:receipt-path-1", PathGeneration: 7}
	if edit != nil {
		edit(&claims)
	}
	return signReceiptV3(t, receiptSecretV3, claims, ttl)
}

// TestProbeResultKeyV3EngineAbsent: a replica that does not run the engine
// records a valid direct receipt of the API host under the receipt's key. It
// asks the direct state once, through PathObservation, and does not compare the
// generation, which only the engine's replica can know. It still refuses what a
// valid API-host receipt does not cover.
func TestProbeResultKeyV3EngineAbsent(t *testing.T) {
	const user, device = 7, "tv-1"
	for _, tc := range []struct {
		name             string
		generation       uint64
		receipt          func(t *testing.T) string
		wantKey          string
		wantErr          error
		wantObservations int
	}{
		{name: "valid direct receipt, no live path here", generation: 0, wantKey: "direct:receipt-path-1", wantObservations: 1,
			receipt: func(t *testing.T) string { return directReceiptV3(t, user, device, nil, time.Minute) }},
		{name: "valid direct receipt, another generation here", generation: 9, wantKey: "direct:receipt-path-1", wantObservations: 1,
			receipt: func(t *testing.T) string { return directReceiptV3(t, user, device, nil, time.Minute) }},
		{name: "expired direct receipt", wantObservations: 0,
			receipt: func(t *testing.T) string { return directReceiptV3(t, user, device, nil, -time.Minute) }},
		{name: "direct receipt of another host", wantObservations: 0,
			receipt: func(t *testing.T) string {
				return directReceiptV3(t, user, device, func(c *streamtoken.Claims) { c.Host = "node:7" }, time.Minute)
			}},
		{name: "invalid direct receipt", wantErr: ErrProbeReceiptInvalid, wantObservations: 0,
			receipt: func(t *testing.T) string {
				return directReceiptV3(t, user, device, func(c *streamtoken.Claims) { c.ConnectionKey = "" }, time.Minute)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &directStateV3{generation: tc.generation}
			h := &PlaybackHandler{JWTSecret: receiptSecretV3, DirectState: state}
			key, err := h.probeResultKeyV3(t.Context(), user, device, tc.receipt(t))
			if !errors.Is(err, tc.wantErr) || key != tc.wantKey {
				t.Fatalf("probeResultKeyV3 = %q, %v; want %q, %v", key, err, tc.wantKey, tc.wantErr)
			}
			if observations, others := state.directCalls(); observations != tc.wantObservations || others != 0 {
				t.Fatalf("direct-state calls = %d PathObservation and %d others, want %d and 0", observations, others, tc.wantObservations)
			}
		})
	}
}

// TestProbeResultKeyV3EngineRunning: on the replica that runs the engine, a
// direct receipt is recorded only while its generation is the engine's.
func TestProbeResultKeyV3EngineRunning(t *testing.T) {
	const user, device = 7, "tv-1"
	for _, tc := range []struct {
		name       string
		generation uint64
		wantKey    string
	}{
		{"same generation", 7, "direct:receipt-path-1"},
		{"earlier path", 8, ""},
		{"no live path", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &directStateV3{engineRunning: true, generation: tc.generation}
			h := &PlaybackHandler{JWTSecret: receiptSecretV3, DirectState: state}
			key, err := h.probeResultKeyV3(t.Context(), user, device, directReceiptV3(t, user, device, nil, time.Minute))
			if err != nil || key != tc.wantKey {
				t.Fatalf("probeResultKeyV3 = %q, %v; want %q, nil", key, err, tc.wantKey)
			}
		})
	}
}

func equalStringsV3(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestPairedProbeOnOneAccount: one account, two enrolled devices, and a proxy
// on another origin. The redirected probe requests carry no device header, so
// each device's identity travels in its probe token and receipt. Each device's
// result reaches only its own entries, and one device's receipt does not place
// another device's result.
func TestPairedProbeOnOneAccount(t *testing.T) {
	pool := connectionTestPool(t)
	user := receiptUserV3(t, pool, "paired-probe")
	proxy := probeProxyStub(t, playback.TransportFeatureBandwidthProbeV1, playback.TransportFeatureConnectionReceiptV1)
	h := probeHandler(proxy)
	h.JWTSecret = receiptSecretV3
	h.Connections = &connectionstore.Store{Pool: pool}
	h.DirectState = &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic"}}
	request := receiptRequestV3(user)
	regularKey, _ := connectionstore.Key(request, receiptSecretV3, connectionstore.ClassRegular, "")

	// proxyReceipt is the receipt the proxy sends for a probe URL: it knows the
	// device and the account only from the probe token.
	proxyReceipt := func(target string) string {
		t.Helper()
		token := path.Base(strings.SplitN(target, "?", 2)[0])
		claims, err := streamtoken.Verify(token, receiptSecretV3)
		if err != nil {
			t.Fatal(err)
		}
		return signReceiptV3(t, receiptSecretV3, streamtoken.Claims{PlayMethod: streamtoken.PlayMethodConnectionReceipt, UserID: claims.UserID, DeviceID: claims.ProbeDevice,
			Host: "node:1", ConnectionClass: streamtoken.ConnectionClassRegular}, time.Minute)
	}
	devices := []struct {
		id   string
		kbps int
	}{{"paired-a", 11000}, {"paired-b", 22000}}
	receipts := map[string]string{}
	for _, device := range devices {
		target, err := h.PlaybackProbeRedirect(probePolicyContext(config.PlaybackEgressPreferProxy), user, 1024, device.id, playback.MediaTransportRegularV3)
		if err != nil || !strings.HasPrefix(target, proxy.URL+"/stream/bandwidth-probe/") {
			t.Fatalf("%s: probe redirect %q, %v; want the proxy", device.id, urlOriginV3(target), err)
		}
		receipts[device.id] = proxyReceipt(target)
	}
	for _, device := range devices {
		result := connectionstore.ProbeResult{Kbps: device.kbps, Bytes: 3_000_000, DurationMS: 500, Transport: "ethernet"}
		if err := h.ReportPlaybackProbe(request, device.id, result, receipts[device.id]); err != nil {
			t.Fatalf("%s: report: %v", device.id, err)
		}
	}
	// A's receipt does not place B's result.
	crossed := connectionstore.ProbeResult{Kbps: 99000, Bytes: 3_000_000, DurationMS: 500, Transport: "ethernet"}
	if err := h.ReportPlaybackProbe(request, "paired-b", crossed, receipts["paired-a"]); !errors.Is(err, ErrProbeReceiptInvalid) {
		t.Fatalf("crossed receipt: %v, want ErrProbeReceiptInvalid", err)
	}
	for _, device := range devices {
		c, err := h.Connections.Connection(request, user, device.id, regularKey)
		if err != nil || c == nil || c.Probe == nil || c.Probe.Kbps != device.kbps {
			t.Fatalf("%s: entry = %+v, %v; want a probe of %d kbps", device.id, c, err, device.kbps)
		}
		if keys := recordedKeysV3(t, pool, user, device.id); !equalStringsV3(keys, []string{regularKey}) {
			t.Fatalf("%s: recorded under %q, want the regular key only", device.id, keys)
		}
	}
}

// TestProbeConnectionReceipt: the API host's receipt is regular over TCP even
// when a direct path is live, and direct only over a direct path whose peer is
// the enrolled device and whose API-host confirmation is live.
func TestProbeConnectionReceipt(t *testing.T) {
	const user, device, directKey, generation = 7, "tv-1", "direct:api-path-1", 5
	live := directconn.DeviceDirect{Policy: "automatic", APIReady: true, ConnectionKey: directKey, PathGeneration: generation}
	peer := func(userID int, deviceID string) *netaccess.DirectPath {
		return &netaccess.DirectPath{HostID: "api", PeerKey: "nodekey:peer", UserID: userID, DeviceID: deviceID, Class: "direct"}
	}
	for _, tc := range []struct {
		name      string
		direct    *netaccess.DirectPath // nil is a request over TCP
		device    string
		state     *directStateV3
		wantClass string // empty means no receipt
	}{
		{"TCP with a live direct path", nil, device, &directStateV3{enrolled: true, device: live}, streamtoken.ConnectionClassRegular},
		{"TCP with no direct state", nil, device, nil, streamtoken.ConnectionClassRegular},
		{"direct path, the enrolled device", peer(user, device), device, &directStateV3{enrolled: true, device: live}, streamtoken.ConnectionClassDirect},
		{"direct path, peer enrolled to another device", peer(user, "other-device"), device, &directStateV3{enrolled: true, device: live}, ""},
		{"direct path, peer enrolled to another account", peer(user+1, device), device, &directStateV3{enrolled: true, device: live}, ""},
		{"direct path, API path not ready", peer(user, device), device, &directStateV3{enrolled: true, device: directconn.DeviceDirect{Policy: "automatic", ConnectionKey: directKey}}, ""},
		{"direct path, not enrolled", peer(user, device), device, &directStateV3{device: live}, ""},
		{"direct path, state unreadable", peer(user, device), device, &directStateV3{err: errors.New("store unavailable")}, ""},
		{"direct path, no direct state", peer(user, device), device, nil, ""},
		{"no device header", nil, "", &directStateV3{enrolled: true, device: live}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &PlaybackHandler{JWTSecret: receiptSecretV3}
			if tc.state != nil {
				h.DirectState = tc.state
			}
			ctx := context.Background()
			if tc.direct != nil {
				ctx = netaccess.WithPath(ctx, netaccess.Path{Direct: tc.direct})
			}
			receipt := h.ProbeConnectionReceipt(ctx, user, tc.device)
			if tc.wantClass == "" {
				if receipt != "" {
					t.Fatal("a receipt was sent")
				}
				return
			}
			claims, err := streamtoken.Verify(receipt, receiptSecretV3)
			if err != nil {
				t.Fatalf("receipt does not verify: %v", err)
			}
			wantKey, wantGen := "", uint64(0)
			if tc.wantClass == streamtoken.ConnectionClassDirect {
				wantKey, wantGen = directKey, generation
			}
			if claims.PlayMethod != streamtoken.PlayMethodConnectionReceipt || claims.UserID != user || claims.DeviceID != device || claims.Host != "api" ||
				claims.ConnectionClass != tc.wantClass || claims.ConnectionKey != wantKey || claims.PathGeneration != wantGen {
				t.Fatalf("receipt = method %q uid %d did %q host %q class %q key %q gen %d", claims.PlayMethod, claims.UserID, claims.DeviceID, claims.Host, claims.ConnectionClass, claims.ConnectionKey, claims.PathGeneration)
			}
			if lifetime := claims.ExpiresAt.Sub(claims.IssuedAt.Time); lifetime != 2*time.Minute {
				t.Fatalf("receipt lifetime = %v, want 2m", lifetime)
			}
		})
	}
}
