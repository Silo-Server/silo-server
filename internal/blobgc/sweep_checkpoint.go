package blobgc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
)

// SweepStore identifies the storage location to which continuation tokens belong.
type SweepStore interface {
	Store
	Identity() string
}

type namespaceCheckpoint struct {
	Cursor string    `json:"cursor"`
	Prefix string    `json:"prefix"`
	Newest time.Time `json:"newest,omitzero"`
}

type sweepCheckpoint struct {
	Identity   string                         `json:"identity"`
	Namespaces map[string]namespaceCheckpoint `json:"namespaces"`
}

func (s *Sweeper) loadCheckpoint(ctx context.Context, conn *pgxpool.Conn) (sweepCheckpoint, error) {
	start := sweepCheckpoint{Identity: s.store.Identity(), Namespaces: map[string]namespaceCheckpoint{}}
	var raw string
	err := conn.QueryRow(ctx, `SELECT value FROM public.server_settings WHERE key=$1`, config.MediaImageSweepCheckpointKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return start, nil
	}
	if err != nil {
		return start, fmt.Errorf("read media image sweep checkpoint: %w", err)
	}
	var saved sweepCheckpoint
	if err := json.Unmarshal([]byte(raw), &saved); err != nil {
		return start, fmt.Errorf("decode media image sweep checkpoint: %w", err)
	}
	if saved.Identity != start.Identity || saved.Namespaces == nil {
		return start, nil
	}
	return saved, nil
}

// The sweep advisory lock covers both listing and checkpoint writes.
func (s *Sweeper) saveCheckpoint(ctx context.Context, conn *pgxpool.Conn, checkpoint sweepCheckpoint) error {
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode media image sweep checkpoint: %w", err)
	}
	_, err = conn.Exec(ctx, `INSERT INTO public.server_settings(key,value) VALUES($1,$2)
		ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, config.MediaImageSweepCheckpointKey, string(encoded))
	if err != nil {
		return fmt.Errorf("save media image sweep checkpoint: %w", err)
	}
	return nil
}
