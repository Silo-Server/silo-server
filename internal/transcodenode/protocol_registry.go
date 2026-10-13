package transcodenode

import (
	"time"

	"github.com/Silo-Server/silo-server/internal/downloadstorage"
	"github.com/Silo-Server/silo-server/internal/nodemetrics"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

type statusResponse struct {
	Status      string                           `json:"status"`
	ActiveJobs  int32                            `json:"active_jobs"`
	Sessions    []string                         `json:"sessions"`
	System      *nodemetrics.SystemStats         `json:"system,omitempty"`
	GPU         []nodemetrics.GPUStats           `json:"gpu,omitempty"`
	Attribution *nodemetrics.ResourceAttribution `json:"attribution,omitempty"`
	SampledAt   time.Time                        `json:"sampled_at,omitzero"`
	// Artifacts is the last measurement of the prepared-download directory,
	// path included: this route requires the node bearer.
	Artifacts *downloadstorage.Usage `json:"artifacts,omitempty"`
}

// ProtocolReads preserves this listener's status shape independently of the
// proxy status shape. Both workers report the shared hardware capability DTO.
func ProtocolReads(schemas huma.Registry) []workerprotocol.Operation {
	return []workerprotocol.Operation{
		workerprotocol.JSONRead[playback.HWAccelInfo](schemas, "transcode_node", "/hw-capabilities", "(*internal/transcodenode.Server).handleHWCapabilities", 401, 503),
		workerprotocol.JSONRead[statusResponse](schemas, "transcode_node", "/status", "(*internal/transcodenode.Server).handleStatus", 401, 503),
		artifactListOperation(schemas),
	}
}

func artifactListOperation(schemas huma.Registry) workerprotocol.Operation {
	op := workerprotocol.JSONRead[downloadstorage.Listing](schemas, "transcode_node", "/downloads/artifacts", "(*internal/transcodenode.Server).handleListDownloadArtifacts", 401, 503)
	op.Description = "List the prepared-download directory with its measurement, including the directory path. One listing runs at a time; a second, or one the volume does not answer within 15 seconds, gets 503."
	return op
}

// ProtocolControls describes the existing worker admin commands, not native API aliases.
func ProtocolControls(schemas huma.Registry) []workerprotocol.Operation {
	reprobe := workerprotocol.JSONRead[reprobeCapabilitiesResponse](schemas, "transcode_node", "/admin/reprobe-capabilities", "(*internal/transcodenode.Server).handleReprobeCapabilities", 401, 409, 503)
	reprobe.Method = "POST"
	reprobe.RetrySafety = "non_retryable"
	reprobe.Description = "Rebuild the capability snapshot. Active jobs or probes refuse with 409; an incomplete probe retains the prior published hash. No durable replay receipt."
	return []workerprotocol.Operation{
		workerprotocol.EmptyCommand("transcode_node", "/admin/force-reload", "(*internal/transcodenode.Server).handleForceReload", "Reload configuration and tear down active sessions and delivery authority. Failure can follow partial effects.", 503),
		workerprotocol.EmptyCommand("transcode_node", "/admin/reload-config", "(*internal/transcodenode.Server).handleReloadConfig", "Reload configuration without tearing down active sessions. No durable replay receipt.", 503),
		reprobe,
	}
}
