package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// EbookConfigGuard runs while the current configuration row is locked. Nil
// means the caller has never saved a configuration; the reader's default is {}.
type EbookConfigGuard func(*EbookReaderConfig) error

type EbookConfigGuardStore interface {
	ReplaceGuarded(context.Context, EbookReaderConfig, EbookConfigGuard) (*EbookReaderConfig, error)
}

func (h *EbookReaderHandler) ReaderConfig(ctx context.Context, userID int, profileID, contentID string, filter catalog.AccessFilter) (*EbookReaderConfig, error) {
	if h == nil || h.ConfigStore == nil || h.FileAuthorizer == nil || h.FileAuthorizer.ItemAccess == nil {
		return nil, &APIError{Status: http.StatusServiceUnavailable, Message: "Ebook reader config is not configured"}
	}
	if err := h.FileAuthorizer.ItemAccess.EnsureAccessible(ctx, contentID, filter); err != nil {
		return nil, err
	}
	config, err := h.ConfigStore.Get(ctx, userID, profileID, contentID)
	if err != nil {
		return nil, &APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to load ebook reader config"}
	}
	return config, nil
}

func (h *EbookReaderHandler) SaveReaderConfig(ctx context.Context, config EbookReaderConfig, filter catalog.AccessFilter, guard EbookConfigGuard) (*EbookReaderConfig, error) {
	if h == nil || h.ConfigStore == nil || h.FileAuthorizer == nil || h.FileAuthorizer.ItemAccess == nil {
		return nil, &APIError{Status: http.StatusServiceUnavailable, Message: "Ebook reader config is not configured"}
	}
	if !jsonObject(config.Config) {
		return nil, &APIError{Status: http.StatusBadRequest, Code: "bad_request", Field: "config", Message: "config must be a JSON object"}
	}
	if err := h.FileAuthorizer.ItemAccess.EnsureAccessible(ctx, config.ContentID, filter); err != nil {
		return nil, err
	}
	if guard != nil {
		store, ok := h.ConfigStore.(EbookConfigGuardStore)
		if !ok {
			return nil, &APIError{Status: http.StatusServiceUnavailable, Message: "Guarded ebook config is not configured"}
		}
		return store.ReplaceGuarded(ctx, config, guard)
	}
	config.UpdatedAt = time.Now().UTC()
	if err := h.ConfigStore.Upsert(ctx, config); err != nil {
		return nil, &APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to save ebook reader config"}
	}
	return &config, nil
}

// ReplaceGuarded locks the same row used by legacy upserts. Inserting the
// default only inside this transaction serializes concurrent first writes;
// failed guards roll that placeholder back, leaving no saved configuration.
func (s *PGEbookReaderConfigStore) ReplaceGuarded(ctx context.Context, config EbookReaderConfig, guard EbookConfigGuard) (*EbookReaderConfig, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("ebook reader config store is not configured")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	inserted, err := tx.Exec(ctx, `INSERT INTO ebook_reader_config (user_id,profile_id,content_id,config)
 VALUES($1,$2,$3,'{}'::jsonb) ON CONFLICT(user_id,profile_id,content_id) DO NOTHING`, config.UserID, config.ProfileID, config.ContentID)
	if err != nil {
		return nil, err
	}
	var current EbookReaderConfig
	err = tx.QueryRow(ctx, `SELECT user_id,profile_id,content_id,config,updated_at FROM ebook_reader_config
 WHERE user_id=$1 AND profile_id=$2 AND content_id=$3 FOR UPDATE`, config.UserID, config.ProfileID, config.ContentID).Scan(&current.UserID, &current.ProfileID, &current.ContentID, &current.Config, &current.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if inserted.RowsAffected() == 1 {
		err = guard(nil)
	} else {
		err = guard(&current)
	}
	if err != nil {
		return nil, err
	}
	// Use the database-returned timestamp/JSON representation for the next tag.
	err = tx.QueryRow(ctx, `UPDATE ebook_reader_config SET config=$4::jsonb,updated_at=clock_timestamp()
 WHERE user_id=$1 AND profile_id=$2 AND content_id=$3 RETURNING config,updated_at`, config.UserID, config.ProfileID, config.ContentID, json.RawMessage(config.Config)).Scan(&config.Config, &config.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &config, nil
}
