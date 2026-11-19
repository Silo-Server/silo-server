package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type adminNodesReadStub struct {
	nodes []*nodepool.Node
	calls int
}

func (s *adminNodesReadStub) ReadAdminNodes(context.Context) ([]*nodepool.Node, error) {
	s.calls++
	return s.nodes, nil
}
