package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/literaryworks"
)

type LiteraryWorkService interface {
	GetWork(ctx context.Context, workID string, filter catalog.AccessFilter) (*literaryworks.DetailResponse, error)
	ListCandidates(ctx context.Context, contentID string, limit int) ([]literaryworks.Candidate, error)
	LinkItems(ctx context.Context, workID string, contentIDs []string) (string, error)
	UnlinkItem(ctx context.Context, workID, contentID string) error
	ConfirmMatch(ctx context.Context, sourceContentID, targetContentID string, userID int) (string, error)
	IgnoreMatch(ctx context.Context, sourceContentID, targetContentID string, userID int) error
}

type LiteraryWorkHandler struct {
	Service LiteraryWorkService
}

type literaryWorkLinkRequest struct {
	WorkID     string   `json:"work_id"`
	ContentIDs []string `json:"content_ids"`
}

type literaryWorkDecisionRequest struct {
	SourceContentID string `json:"source_content_id"`
	TargetContentID string `json:"target_content_id"`
}
