// Package connectionstore keeps device connection observations shared by API replicas.
package connectionstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/streamlocation"
)

const (
	labelRemote = "remote"
	labelLocal  = "local"
)

const (
	// TTL is how long a learned limit applies after its latest observation:
	// one bad evening, not a week.
	TTL = 24 * time.Hour
	// Retention is how long a row survives without an update.
	Retention = 30 * 24 * time.Hour
)

// Limit is a learned limit: the step-down estimate a buffering client last
// reported for one device on one connection.
type Limit struct {
	Kbps       int
	ObservedAt time.Time
	ExpiresAt  time.Time
}

// Probe is the latest connection speed test a device reported. LowerBound
// marks a test that proved only "at least Kbps".
type Probe struct {
	Kbps       int
	LowerBound bool
	MeasuredAt time.Time
	Transport  string
}

// Sample is the best throughput playback reached on a connection, kept as a
// lower bound on its speed.
type Sample struct {
	Kbps       int
	MeasuredAt time.Time
}

// ProbeResult is one reported speed test, bounded by the API before it
// reaches the store. Manual marks a test the viewer started.
type ProbeResult struct {
	Kbps       int
	Bytes      int64
	DurationMS int
	Transport  string
	LowerBound bool
	Manual     bool
}

// State is what the calling device sees for the connection it is using now.
type State struct {
	Probe          *Probe
	PlaybackSample *Sample
	AutoBudgetKbps *int
	Connection     string
	LearnedLimit   *Limit
}

// Store reads and writes playback_connections. Every method on a nil Store,
// and every per-device method without a device or connection key, does
// nothing: a request with no device identity neither learns nor applies.
type Store struct{ Pool *pgxpool.Pool }

// Key never retains a public address. IPv6 rotations within a /64 share a key.
// An unresolved address has no identity and must not share another viewer's limit.
func Key(ctx context.Context, secret string) (key, label string) {
	if provider := netaccess.PathFromContext(ctx).Provider; provider != "" {
		return "provider:" + provider, "provider:" + provider
	}
	ip := net.ParseIP(clientip.FromContext(ctx))
	if ip == nil {
		return "", labelRemote
	}
	if streamlocation.FromContext(ctx) == streamlocation.Local {
		return labelLocal, labelLocal
	}
	if secret == "" {
		return "", labelRemote
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	} else {
		ip = ip.Mask(net.CIDRMask(64, 128))
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("playback-connection-v1\x00"))
	mac.Write(ip)
	return "remote:" + hex.EncodeToString(mac.Sum(nil)), labelRemote
}

// Get returns the unexpired learned limit for a device on a connection.
func (s *Store) Get(ctx context.Context, user int, device, key string) (*Limit, error) {
	if s == nil || device == "" || key == "" {
		return nil, nil
	}
	var limit Limit
	err := s.Pool.QueryRow(ctx, `
		SELECT limit_kbps, observed_at, observed_at + $4::interval
		FROM playback_connections
		WHERE user_id = $1 AND device_id = $2 AND connection_key = $3
		  AND observed_at > now() - $4::interval`,
		user, device, key, TTL).Scan(&limit.Kbps, &limit.ObservedAt, &limit.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &limit, err
}

// Observe records a learned limit, replacing any earlier one and restarting
// its TTL.
func (s *Store) Observe(ctx context.Context, user int, device, key string, kbps int) error {
	if s == nil || device == "" || key == "" {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO playback_connections (user_id, device_id, connection_key, limit_kbps, observed_at, updated_at)
		VALUES ($1, $2, $3, $4, now(), now())
		ON CONFLICT (user_id, device_id, connection_key) DO UPDATE
		SET limit_kbps = EXCLUDED.limit_kbps, observed_at = now(), updated_at = now()`,
		user, device, key, kbps)
	return err
}

// Clear removes a learned limit and keeps the probe and plan summary.
func (s *Store) Clear(ctx context.Context, user int, device, key string) error {
	if s == nil || device == "" || key == "" {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `
		UPDATE playback_connections
		SET limit_kbps = NULL, observed_at = NULL, updated_at = now()
		WHERE user_id = $1 AND device_id = $2 AND connection_key = $3`,
		user, device, key)
	return err
}

// DeleteDevices deletes every row of the named devices on the account.
func (s *Store) DeleteDevices(ctx context.Context, user int, devices []string) error {
	if s == nil || len(devices) == 0 {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM playback_connections WHERE user_id = $1 AND device_id = ANY($2)`, user, devices)
	return err
}

// Cleanup deletes rows that have not been updated within Retention.
func (s *Store) Cleanup(ctx context.Context) error {
	if s == nil {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM playback_connections WHERE updated_at < now() - $1::interval`, Retention)
	return err
}

// Reading is the connection's measured speed: the higher of the probe and the
// playback sample, or 0 without either. Callers pass only those within their
// lifetime.
func Reading(probe *Probe, sample *Sample) int {
	kbps := 0
	if probe != nil {
		kbps = probe.Kbps
	}
	if sample != nil && sample.Kbps > kbps {
		kbps = sample.Kbps
	}
	return kbps
}

// Budget uses the same margin as Auto (playback.StreamBudgetKbpsV3): 80% of
// the lower of the Reading and the learned limit, capped by capKbps. A missing
// measurement is not a zero-speed link.
func Budget(probe *Probe, sample *Sample, limit *Limit, capKbps int) *int {
	kbps := Reading(probe, sample)
	if limit != nil && (kbps == 0 || limit.Kbps < kbps) {
		kbps = limit.Kbps
	}
	if kbps == 0 {
		return nil
	}
	kbps = kbps * 8 / 10
	if capKbps > 0 {
		kbps = min(kbps, capKbps)
	}
	return &kbps
}

// ReportProbe stores a device's latest speed test on a connection. A manual
// test also clears the playback sample, so an older, higher sample cannot
// outvote it, and when faster than the learned limit clears the limit, as a
// reset does.
func (s *Store) ReportProbe(ctx context.Context, user int, device, key string, result ProbeResult) error {
	if s == nil || device == "" || key == "" {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO playback_connections (user_id, device_id, connection_key, probe_kbps, probe_bytes, probe_duration_ms, probe_transport, probe_lower_bound, measured_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), now())
		ON CONFLICT (user_id, device_id, connection_key) DO UPDATE
		SET probe_kbps = $4, probe_bytes = $5, probe_duration_ms = $6, probe_transport = $7, probe_lower_bound = $8, measured_at = now(), updated_at = now(),
		    limit_kbps = CASE WHEN $9 AND playback_connections.limit_kbps < $4 THEN NULL ELSE playback_connections.limit_kbps END,
		    observed_at = CASE WHEN $9 AND playback_connections.limit_kbps < $4 THEN NULL ELSE playback_connections.observed_at END,
		    sample_kbps = CASE WHEN $9 THEN NULL ELSE playback_connections.sample_kbps END,
		    sample_at = CASE WHEN $9 THEN NULL ELSE playback_connections.sample_at END`,
		user, device, key, result.Kbps, result.Bytes, result.DurationMS, result.Transport, result.LowerBound, result.Manual)
	return err
}

// ReportSample keeps the best playback throughput on a connection: kbps
// replaces the kept sample when it is at least as high, or when the kept one
// is older than lifetime, the reading lifetime.
func (s *Store) ReportSample(ctx context.Context, user int, device, key string, kbps int, lifetime time.Duration) error {
	if s == nil || device == "" || key == "" {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO playback_connections (user_id, device_id, connection_key, sample_kbps, sample_at, updated_at)
		VALUES ($1, $2, $3, $4, now(), now())
		ON CONFLICT (user_id, device_id, connection_key) DO UPDATE
		SET sample_kbps = EXCLUDED.sample_kbps, sample_at = now(), updated_at = now()
		WHERE playback_connections.sample_kbps IS NULL
		   OR playback_connections.sample_kbps <= EXCLUDED.sample_kbps
		   OR playback_connections.sample_at <= now() - $5::interval`,
		user, device, key, kbps, lifetime)
	return err
}

// AdminConnection is one connection a device used, as the admin device page
// shows it. ID is the opaque connection key the admin reset addresses.
type AdminConnection struct {
	ID             string
	Connection     string
	Probe          *Probe
	PlaybackSample *Sample
	LearnedLimit   *Limit
	PlanSummary    *PlanSummary
}

// List returns a device's retained connections, most recently updated first.
func (s *Store) List(ctx context.Context, user int, device string) ([]AdminConnection, error) {
	return s.list(ctx, user, device, "")
}

// Connection returns a device's retained row for one connection, or nil: its
// readings whatever their age, and its learned limit while unexpired.
func (s *Store) Connection(ctx context.Context, user int, device, key string) (*AdminConnection, error) {
	if s == nil || device == "" || key == "" {
		return nil, nil
	}
	rows, err := s.list(ctx, user, device, key)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// list reads a device's retained rows, only key's row when key is set.
func (s *Store) list(ctx context.Context, user int, device, key string) ([]AdminConnection, error) {
	result := []AdminConnection{}
	if s == nil {
		return result, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT connection_key, probe_kbps, probe_lower_bound, measured_at, probe_transport,
		       sample_kbps, sample_at,
		       CASE WHEN observed_at > now() - $3::interval THEN limit_kbps END,
		       CASE WHEN observed_at > now() - $3::interval THEN observed_at END,
		       plan_summary
		FROM playback_connections
		WHERE user_id = $1 AND device_id = $2 AND updated_at > now() - $4::interval
		  AND ($5::text = '' OR connection_key = $5)
		ORDER BY updated_at DESC`,
		user, device, TTL, Retention, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r AdminConnection
		var probeKbps, sampleKbps, limitKbps *int
		var lowerBound bool
		var measured, sampled, observed *time.Time
		var transport *string
		if err := rows.Scan(&r.ID, &probeKbps, &lowerBound, &measured, &transport, &sampleKbps, &sampled, &limitKbps, &observed, &r.PlanSummary); err != nil {
			return nil, err
		}
		r.Connection = r.ID
		if strings.HasPrefix(r.ID, labelRemote+":") {
			r.Connection = labelRemote
		}
		if probeKbps != nil && measured != nil && transport != nil {
			r.Probe = &Probe{Kbps: *probeKbps, LowerBound: lowerBound, MeasuredAt: *measured, Transport: *transport}
		}
		if sampleKbps != nil && sampled != nil {
			r.PlaybackSample = &Sample{Kbps: *sampleKbps, MeasuredAt: *sampled}
		}
		if limitKbps != nil && observed != nil {
			r.LearnedLimit = &Limit{Kbps: *limitKbps, ObservedAt: *observed, ExpiresAt: observed.Add(TTL)}
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// PlanSummary is the latest plan_summary route event a device reported on a
// connection, with the server's receipt time.
type PlanSummary struct {
	PlanID      string            `json:"plan_id"`
	ReportedAt  time.Time         `json:"reported_at"`
	Diagnostics map[string]string `json:"diagnostics"`
}

// ReportSummary keeps the newest plan summary; an older one that arrives late
// does not replace it.
func (s *Store) ReportSummary(ctx context.Context, user int, device, key string, summary PlanSummary) error {
	if s == nil || device == "" || key == "" {
		return nil
	}
	body, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO playback_connections (user_id, device_id, connection_key, plan_summary, summary_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (user_id, device_id, connection_key) DO UPDATE
		SET plan_summary = $4, summary_at = $5, updated_at = now()
		WHERE playback_connections.summary_at IS NULL OR playback_connections.summary_at <= $5`,
		user, device, key, body, summary.ReportedAt)
	return err
}
