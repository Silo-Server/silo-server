// Package connectionstore keeps device connection observations shared by API replicas.
package connectionstore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
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
type State struct {
	Connection   string `json:"connection"`
	LearnedLimit *Limit `json:"learned_limit"`
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
	_, err := s.Pool.Exec(ctx, `DELETE FROM playback_connections WHERE user_id=$1 AND device_id=$2 AND connection_key=$3`, user, device, key)
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
