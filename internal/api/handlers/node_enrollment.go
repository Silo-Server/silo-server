package handlers

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/nodeenrollment"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

func (h *NodeHandler) SetEnrollmentClient(client *nodeenrollment.Client) { h.enrollment = client }

// EnrollAdminNode hands off ephemeral customer credentials before using the
// ordinary natural-URL configuration store. A retry verifies an existing
// binding, so registry failure never requires reusing the provider token.
func (h *NodeHandler) EnrollAdminNode(ctx context.Context, input nodepool.CreateNodeInput, handoff nodeenrollment.Input) (*nodepool.Node, error) {
	if h.configuration == nil || h.enrollment == nil {
		return nil, ErrAdminNodesUnavailable
	}
	if err := input.Validate(); err != nil {
		return nil, err
	}
	if input.Type != nodepool.NodeTypeTranscode {
		return nil, fmt.Errorf("%w: enrollment requires a transcode node", nodepool.ErrInvalidNodeInput)
	}
	receipt, err := h.enrollment.Enroll(ctx, input.URL, input.Name, handoff)
	if err != nil {
		return nil, err
	}
	if ceiling := receipt.EncodingCapacity.MaxJobs; ceiling > 0 && (input.MaxJobs == nil || *input.MaxJobs <= 0 || *input.MaxJobs > ceiling) {
		input.MaxJobs = &ceiling
	}
	return h.CreateAdminNode(ctx, input)
}
