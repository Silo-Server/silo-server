package adminjob

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/metadata"
)

const (
	JobTypeArtworkStorageRefresh = "artwork_storage_refresh"
	JobTypeArtworkPurge          = "artwork_storage_purge"
	JobTypeArtworkStorageImport  = "artwork_storage_import"

	ArtworkPurgeModeEdgeOnly         = metadata.ArtworkPurgeModeEdgeOnly
	ArtworkPurgeModeSafeMaterialized = metadata.ArtworkPurgeModeSafeMaterialized
)

type ArtworkPurgeScope = metadata.ArtworkPurgeScope
type ArtworkPurgeRequest = metadata.ArtworkPurgeRequest
type ArtworkPurgeCheckpoint = metadata.ArtworkPurgeCheckpoint
type ArtworkPurgeResult = metadata.ArtworkPurgeResult

type artworkPurgeExecutor interface {
	Execute(
		ctx context.Context,
		req ArtworkPurgeRequest,
		checkpoint *ArtworkPurgeCheckpoint,
		save func(ArtworkPurgeCheckpoint) error,
		progress func(current, total int, message string),
	) (*ArtworkPurgeResult, error)
}

func decodeArtworkPurgeRequest(data json.RawMessage) (ArtworkPurgeRequest, error) {
	var req ArtworkPurgeRequest
	if len(data) == 0 {
		return req, fmt.Errorf("missing artwork purge payload")
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return req, fmt.Errorf("invalid artwork purge request payload: %w", err)
	}
	if err := (&req).Validate(); err != nil {
		return req, err
	}
	return req, nil
}

func decodeArtworkInventoryCheckpoint(data json.RawMessage) (*metadata.ArtworkInventoryCheckpoint, error) {
	if len(data) == 0 || string(data) == "{}" {
		return nil, nil
	}
	var checkpoint metadata.ArtworkInventoryCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, fmt.Errorf("invalid artwork inventory checkpoint: %w", err)
	}
	return &checkpoint, nil
}

func decodeArtworkPurgeCheckpoint(data json.RawMessage) (*ArtworkPurgeCheckpoint, error) {
	if len(data) == 0 || string(data) == "{}" {
		return nil, nil
	}
	var checkpoint ArtworkPurgeCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, fmt.Errorf("invalid artwork purge checkpoint: %w", err)
	}
	return &checkpoint, nil
}
