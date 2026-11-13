package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type WatchProviderService interface {
	ListProviders() []watchsync.ProviderSummary
	StartDeviceAuth(ctx context.Context, userID int, profileID string, providerKey string) (watchsync.DeviceAuthSession, error)
	PollDeviceAuth(ctx context.Context, userID int, profileID string, providerKey string, sessionID string) (watchsync.Connection, error)
	ConnectAPIKeyWithConfig(ctx context.Context, userID int, profileID string, providerKey string, apiKey string, connectionConfig watchsync.ConnectionConfigValues) (watchsync.Connection, error)
	GetConnectionStatus(ctx context.Context, userID int, profileID string, provider string) (watchsync.ConnectionStatus, error)
	UpdateConnection(ctx context.Context, userID int, profileID string, provider string, update watchsync.ConnectionUpdate) (watchsync.ConnectionStatus, error)
	DeleteConnection(ctx context.Context, userID int, profileID string, provider string) error
	RequestManualSync(ctx context.Context, userID int, profileID string, provider string) (watchsync.ManualSyncResult, error)
	ListSyncRuns(ctx context.Context, userID int, profileID string, provider string, limit int) ([]watchsync.SyncRun, error)
}
