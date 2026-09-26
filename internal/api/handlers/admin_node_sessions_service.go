package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/redis/go-redis/v9"
)

type AdminNodeSessionsService struct {
	Redis *redis.Client
	Nodes interface {
		GetByID(context.Context, int) (*nodepool.Node, error)
	}
}

func (s *AdminNodeSessionsService) Available() bool {
	return s != nil && s.Redis != nil && s.Nodes != nil
}

// Read preserves the owning reader's best-effort diagnostic semantics. The
// bridge continues to pass raw records through its original handler.
func (s *AdminNodeSessionsService) Read(ctx context.Context, nodeID int) (nodesessions.ListResult, error) {
	var empty nodesessions.ListResult
	if !s.Available() {
		return empty, &APIError{Status: http.StatusServiceUnavailable, Message: "Node session observations unavailable"}
	}
	nodeURL := ""
	if nodeID > 0 {
		node, err := s.Nodes.GetByID(ctx, nodeID)
		if errors.Is(err, nodepool.ErrNodeNotFound) {
			return empty, &APIError{Status: http.StatusNotFound, Message: "Node not found"}
		}
		if err != nil {
			return empty, err
		}
		if node == nil {
			return empty, &APIError{Status: http.StatusNotFound, Message: "Node not found"}
		}
		nodeURL = node.URL
	}
	result, err := nodesessions.ListAll(ctx, s.Redis, 50000)
	if err != nil {
		return empty, err
	}
	if result.Truncated {
		return empty, &APIError{Status: http.StatusServiceUnavailable, Message: "Too many node observations to enumerate completely"}
	}
	if nodeURL != "" {
		filtered := make([]nodesessions.SessionInfo, 0)
		for _, row := range result.Sessions {
			if row.NodeURL == nodeURL {
				filtered = append(filtered, row)
			}
		}
		result.Sessions = filtered
	}
	return result, nil
}
