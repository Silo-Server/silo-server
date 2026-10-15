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
	result, err := s.list(ctx, nodeID)
	if err != nil {
		return empty, err
	}
	if result.Truncated {
		return empty, &APIError{Status: http.StatusServiceUnavailable, Message: "Too many node observations to enumerate completely"}
	}
	return result, nil
}

// adminNodeSessionsLimit bounds one read of the session records.
const adminNodeSessionsLimit = 50_000

// list reads every node's session records, or with nodeID > 0 that node's
// own: those the core writes under its id from its reports, and those under
// the hash of its URL that name that URL. The hash alone would also match any
// node whose URL shares it.
func (s *AdminNodeSessionsService) list(ctx context.Context, nodeID int) (nodesessions.ListResult, error) {
	if nodeID <= 0 {
		return nodesessions.ListAll(ctx, s.Redis, adminNodeSessionsLimit)
	}
	node, err := s.Nodes.GetByID(ctx, nodeID)
	if errors.Is(err, nodepool.ErrNodeNotFound) {
		return nodesessions.ListResult{}, &APIError{Status: http.StatusNotFound, Message: "Node not found"}
	}
	if err != nil {
		return nodesessions.ListResult{}, err
	}
	if node == nil {
		return nodesessions.ListResult{}, &APIError{Status: http.StatusNotFound, Message: "Node not found"}
	}
	return nodesessions.ListNode(ctx, s.Redis, nodeID, node.URL, adminNodeSessionsLimit)
}
