package apiv2

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/nodepool"
)

type AdminNodeCommandsService interface {
	CheckAdminNode(context.Context, int) (handlers.AdminNodeCheckView, error)
	ReprobeAdminNode(http.ResponseWriter, *http.Request, int) (handlers.ReprobeNodeResult, error)
}
type AdminNodeCommandInput struct {
	ID string `path:"id" pattern:"^[1-9][0-9]*$" maxLength:"10"`
	requestCapture
}

type AdminNodeHealth struct {
	Healthy          bool   `json:"healthy"`
	ActiveJobs       int    `json:"active_jobs"`
	EgressKbps       int    `json:"egress_kbps"`
	CapabilitiesHash string `json:"capabilities_hash,omitempty"`
	HealthPersisted  bool   `json:"health_persisted" doc:"The URL-fenced persistence call returned successfully; it may have ignored a concurrently repointed node. Not a revision receipt."`
}
type AdminNodeHealthOutput struct{ Body AdminNodeHealth }
type AdminNodeReprobe struct {
	NodeID                ID     `json:"node_id"`
	NodeName              string `json:"node_name"`
	Status                string `json:"status" enum:"ok,error"`
	Error                 string `json:"error,omitempty"`
	Resolved              string `json:"resolved,omitempty"`
	CapabilityHash        string `json:"capability_hash,omitempty"`
	CapabilitiesRefreshed bool   `json:"capabilities_refreshed"`
}
type AdminNodeReprobeOutput struct{ Body AdminNodeReprobe }

func adminNodeCommandID(in *AdminNodeCommandInput) (int, error) {
	id, err := intOfID(ID(in.ID))
	if err != nil || id < 1 || id > 2147483647 {
		return 0, NewProblem(TypeValidationFailed, "Invalid node ID.")
	}
	return id, nil
}
func adminNodeCommandProblem(err error) error {
	switch {
	case errors.Is(err, handlers.ErrAdminNodesUnavailable):
		return unavailable("administrator nodes")
	case errors.Is(err, nodepool.ErrNodeNotFound):
		return NewProblem(TypeNotFound, "Node not found.")
	default:
		return NewProblem(TypeInternalError, "Node command could not be confirmed. Inspect node state before another explicit command.")
	}
}
func registerAdminNodeCommands(reg *Registry) {
	op := func(suffix, id, summary string, retry RetrySafety) Operation {
		return Operation{Operation: humaOp("POST", Prefix+"/admin/nodes/{id}/"+suffix, id, "admin-nodes", summary), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: retry}
	}
	Register(reg, op("check", "checkAdminNode", "Synchronously observe node health and apply URL-fenced persistence and process pool updates. Unhealthy is a successful observation; no durable job or hardware support guarantee.", RetrySafetyNaturalIdempotent), func(ctx context.Context, in *AdminNodeCommandInput) (*AdminNodeHealthOutput, error) {
		id, err := adminNodeCommandID(in)
		if err != nil {
			return nil, err
		}
		if reg.deps.AdminNodeCommands == nil {
			return nil, unavailable("administrator nodes")
		}
		out, err := reg.deps.AdminNodeCommands.CheckAdminNode(ctx, id)
		if err != nil {
			return nil, adminNodeCommandProblem(err)
		}
		return &AdminNodeHealthOutput{Body: AdminNodeHealth{Healthy: out.Healthy, ActiveJobs: out.ActiveJobs, EgressKbps: out.EgressKbps, CapabilitiesHash: out.CapabilitiesHash, HealthPersisted: out.HealthPersisted}}, nil
	})
	Register(reg, op("reprobe", "reprobeAdminNode", "Synchronously request a node hardware reprobe with existing policy-derived deadlines. The body distinguishes node refusal or uncertainty from success and reports inventory refresh separately. No automatic replay or durable completion receipt.", RetrySafetyNonRetryable), func(_ context.Context, in *AdminNodeCommandInput) (*AdminNodeReprobeOutput, error) {
		id, err := adminNodeCommandID(in)
		if err != nil {
			return nil, err
		}
		if reg.deps.AdminNodeCommands == nil {
			return nil, unavailable("administrator nodes")
		}
		out, err := reg.deps.AdminNodeCommands.ReprobeAdminNode(in.writer, in.request, id)
		if err != nil {
			return nil, adminNodeCommandProblem(err)
		}
		body := AdminNodeReprobe{NodeID: IDFromInt(int64(out.NodeID)), NodeName: out.NodeName, Status: out.Status, Resolved: out.Resolved, CapabilityHash: out.CapabilityHash, CapabilitiesRefreshed: out.CapabilitiesRefreshed}
		if out.Error != "" {
			body.Error = "Node reprobe was refused or could not be confirmed. Inspect node state before another explicit command."
		}
		return &AdminNodeReprobeOutput{Body: body}, nil
	})
}
