// Package connectionstore keeps device connection observations shared by API replicas.
package connectionstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
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

const TTL = 7 * 24 * time.Hour

type Limit struct {
	Kbps       int       `json:"kbps"`
	ObservedAt time.Time `json:"observed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}
type Probe struct {
	Kbps       int       `json:"kbps"`
	MeasuredAt time.Time `json:"measured_at"`
	Transport  string    `json:"transport"`
}
type ProbeResult struct {
	Kbps       int    `json:"kbps"`
	Bytes      int64  `json:"bytes"`
	DurationMS int    `json:"duration_ms"`
	Transport  string `json:"transport"`
}
type State struct {
	Probe          *Probe `json:"probe,omitempty"`
	AutoBudgetKbps *int   `json:"auto_budget_kbps,omitempty"`
	Connection     string `json:"connection"`
	LearnedLimit   *Limit `json:"learned_limit"`
}
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
func (s *Store) Get(ctx context.Context, user int, device, key string) (*Limit, error) {
	if s == nil || device == "" || key == "" {
		return nil, nil
	}
	var limit Limit
	err := s.Pool.QueryRow(ctx, `SELECT limit_kbps, observed_at, observed_at + interval '7 days' FROM playback_connections WHERE user_id=$1 AND device_id=$2 AND connection_key=$3 AND observed_at > now()-interval '7 days'`, user, device, key).Scan(&limit.Kbps, &limit.ObservedAt, &limit.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &limit, err
}
func (s *Store) Observe(ctx context.Context, user int, device, key string, kbps int) error {
	if s == nil || device == "" || key == "" {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO playback_connections(user_id,device_id,connection_key,limit_kbps,observed_at,updated_at) VALUES($1,$2,$3,$4,now(),now()) ON CONFLICT(user_id,device_id,connection_key) DO UPDATE SET limit_kbps=EXCLUDED.limit_kbps,observed_at=now(),updated_at=now()`, user, device, key, kbps)
	return err
}
func (s *Store) Clear(ctx context.Context, user int, device, key string) error {
	if s == nil || device == "" || key == "" {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `UPDATE playback_connections SET limit_kbps=NULL, observed_at=NULL, updated_at=now() WHERE user_id=$1 AND device_id=$2 AND connection_key=$3`, user, device, key)
	return err
}
func (s *Store) Cleanup(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM playback_connections WHERE updated_at < now()-interval '30 days'`)
	return err
}
func (s *Store) RunCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if err := s.Cleanup(ctx); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "playback connection retention failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Budget uses the same margin as Auto. A missing measurement is not a zero-speed link.
func Budget(probe *Probe, limit *Limit, capKbps int) *int {
	kbps := 0
	if probe != nil {
		kbps = probe.Kbps
	}
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
func (s *Store) ReportProbe(ctx context.Context, user int, device, key string, result ProbeResult) error {
	if s == nil || device == "" || key == "" {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO playback_connections(user_id,device_id,connection_key,probe_kbps,probe_bytes,probe_duration_ms,probe_transport,measured_at,updated_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,now(),now()) ON CONFLICT(user_id,device_id,connection_key) DO UPDATE SET probe_kbps=$4,probe_bytes=$5,probe_duration_ms=$6,probe_transport=$7,measured_at=now(),updated_at=now()`, user, device, key, result.Kbps, result.Bytes, result.DurationMS, result.Transport)
	return err
}
func (s *Store) Probe(ctx context.Context, user int, device, key string) (*Probe, error) {
	if s == nil || device == "" || key == "" {
		return nil, nil
	}
	var p Probe
	err := s.Pool.QueryRow(ctx, `SELECT probe_kbps,measured_at,probe_transport FROM playback_connections WHERE user_id=$1 AND device_id=$2 AND connection_key=$3 AND measured_at IS NOT NULL AND updated_at > now()-interval '30 days'`, user, device, key).Scan(&p.Kbps, &p.MeasuredAt, &p.Transport)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &p, err
}

type AdminConnection struct {
	ID           string       `json:"id"`
	Connection   string       `json:"connection"`
	Probe        *Probe       `json:"probe"`
	LearnedLimit *Limit       `json:"learned_limit"`
	PlanSummary  *PlanSummary `json:"plan_summary"`
}

func (s *Store) List(ctx context.Context, user int, device string) ([]AdminConnection, error) {
	result := []AdminConnection{}
	if s == nil {
		return result, nil
	}
	rows, err := s.Pool.Query(ctx, `SELECT connection_key,probe_kbps,measured_at,probe_transport,limit_kbps,observed_at,plan_summary FROM playback_connections WHERE user_id=$1 AND device_id=$2 AND updated_at > now()-interval '30 days' ORDER BY updated_at DESC`, user, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r AdminConnection
		var pk, lk *int
		var measured, observed *time.Time
		var transport *string
		if err := rows.Scan(&r.ID, &pk, &measured, &transport, &lk, &observed, &r.PlanSummary); err != nil {
			return nil, err
		}
		r.Connection = r.ID
		if strings.HasPrefix(r.ID, "remote:") {
			r.Connection = "remote"
		}
		if pk != nil && measured != nil && transport != nil {
			r.Probe = &Probe{*pk, *measured, *transport}
		}
		if lk != nil && observed != nil && time.Since(*observed) < TTL {
			r.LearnedLimit = &Limit{*lk, *observed, observed.Add(TTL)}
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

type PlanSummary struct {
	PlanID      string            `json:"plan_id"`
	ReportedAt  time.Time         `json:"reported_at"`
	Diagnostics map[string]string `json:"diagnostics"`
}

func (s *Store) ReportSummary(ctx context.Context, user int, device, key string, summary PlanSummary) error {
	if s == nil || device == "" || key == "" {
		return nil
	}
	body, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO playback_connections(user_id,device_id,connection_key,plan_summary,summary_at,updated_at)
 VALUES($1,$2,$3,$4,$5,now()) ON CONFLICT(user_id,device_id,connection_key) DO UPDATE SET plan_summary=$4,summary_at=$5,updated_at=now() WHERE playback_connections.summary_at IS NULL OR playback_connections.summary_at <= $5`, user, device, key, body, summary.ReportedAt)
	return err
}
