package requests

import (
	"context"
	"time"
)

type Store interface {
	GetSettings(ctx context.Context) (Settings, error)
	UpdateSettings(ctx context.Context, settings Settings) (Settings, error)
	GetUserLimit(ctx context.Context, userID int) (*UserLimit, error)
	UpsertUserLimit(ctx context.Context, limit UserLimit) (*UserLimit, error)
	CountUserRequestsSince(ctx context.Context, userID int, since time.Time) (int, error)
	ListActiveByTMDB(ctx context.Context, mediaType MediaType, tmdbIDs []int) (map[int]*Request, error)
	CreateRequest(ctx context.Context, input CreateRequestRecord) (*Request, error)
	GetRequest(ctx context.Context, id string) (*Request, error)
	ListReconciliationCandidates(ctx context.Context, limit int) ([]*Request, error)
	ListMine(ctx context.Context, userID int, filter ListFilter) ([]*Request, error)
	ListAdmin(ctx context.Context, filter ListFilter) ([]*Request, error)
	SetStatus(ctx context.Context, id string, status Status, actor Viewer) (*Request, error)
	MarkQueued(ctx context.Context, id string, update QueueUpdate, actor Viewer) (*Request, error)
	SetOutcome(ctx context.Context, id string, outcome Outcome, actor Viewer, message string) (*Request, error)
	ListIntegrations(ctx context.Context) ([]Integration, error)
	UpsertIntegration(ctx context.Context, integration Integration) (*Integration, error)
}

type CreateRequestRecord struct {
	ID        string
	Input     CreateRequestInput
	Status    Status
	Outcome   Outcome
	Requester Viewer
	Now       time.Time
}
