package handlers

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
)

// The native playback routes compile through the two wrappers below and
// nowhere else; a forbidigo rule in .golangci.yml fails any other call from
// this package. The wrappers apply one rule, so no path that picks, reuses or
// counts a route can skip it: a device that takes no proxy origin is never
// offered a proxy-egress shape.

// deviceRouteRequestV3 is request for the device on ctx. A device whose
// connection path is direct_only, or whose state could not be read, takes no
// proxy origin, so every proxy-egress shape is left out. The rule applies to
// shapes, not nodes: it holds for every proxy, and availability checks and
// transport reuse compile shapes without reserving a node.
func deviceRouteRequestV3(ctx context.Context, request noderouting.Request) noderouting.Request {
	if directMediaFromContext(ctx).DirectOnly {
		request.ProxyAllowed = false
	}
	return request
}

// routeCandidatesV3 is noderouting.Candidates for the device on ctx.
func routeCandidatesV3(ctx context.Context, request noderouting.Request) (noderouting.Result, error) {
	result, err := noderouting.Candidates(deviceRouteRequestV3(ctx, request))
	if err != nil {
		return noderouting.Result{}, fmt.Errorf("compile native route candidates: %w", err)
	}
	return result, nil
}

// resolveRouteV3 is noderouting.Resolve for the device on ctx.
func resolveRouteV3(ctx context.Context, planner nodepool.RoutePlanner, request noderouting.ResolveRequest) (noderouting.Decision, error) {
	request.Request = deviceRouteRequestV3(ctx, request.Request)
	decision, err := noderouting.Resolve(planner, request)
	if err != nil {
		return noderouting.Decision{}, fmt.Errorf("resolve native route: %w", err)
	}
	return decision, nil
}
