package apiv2

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminAutoscanInspectionService interface {
	ReadAdminAutoscanSettings(context.Context) (handlers.AdminAutoscanSettingsView, error)
	ReadAdminAutoscanStatus(context.Context) (handlers.AdminAutoscanStatusView, error)
}
type AdminAutoscanSettings struct {
	Enabled                    bool `json:"enabled"`
	DefaultPollIntervalSeconds int  `json:"default_poll_interval_seconds"`
	DebounceSeconds            int  `json:"debounce_seconds"`
}
type AdminAutoscanSettingsReadInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminAutoscanSettingsOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   AdminAutoscanSettings
}
type AdminAutoscanStatusSource struct {
	ID           string                     `json:"id"`
	PluginID     string                     `json:"plugin_id"`
	CapabilityID string                     `json:"capability_id"`
	ConnectionID *string                    `json:"connection_id"`
	Enabled      bool                       `json:"enabled"`
	Label        string                     `json:"label"`
	PathRewrites []AdminAutoscanPathRewrite `json:"path_rewrites"`
	LastRunAt    *Instant                   `json:"last_run_at,omitempty"`
	LastError    *string                    `json:"last_error,omitempty"`
}
type AdminAutoscanRunningPoll struct {
	ID           ID      `json:"id"`
	SourceID     *string `json:"source_id"`
	PluginID     string  `json:"plugin_id"`
	CapabilityID string  `json:"capability_id"`
	StartedAt    Instant `json:"started_at"`
	ElapsedMS    int64   `json:"elapsed_ms"`
	MarkerBefore *string `json:"marker_before,omitempty"`
}
type AdminAutoscanStatus struct {
	Enabled       bool                        `json:"enabled"`
	Sources       []AdminAutoscanStatusSource `json:"sources"`
	RunningPolls  []AdminAutoscanRunningPoll  `json:"running_polls"`
	ActiveScans   int                         `json:"active_scans"`
	AcceptedScans int                         `json:"accepted_scans"`
	RunningScans  int                         `json:"running_scans"`
	LatestEventAt *Instant                    `json:"latest_event_at,omitempty"`
}
type AdminAutoscanStatusOutput struct{ Body AdminAutoscanStatus }

func adminAutoscanSettingsTag(ctx context.Context, s AdminAutoscanSettings) EntityTag {
	return RenderETag("admin-autoscan-settings:"+strconv.Itoa(claimsFrom(ctx).UserID)+":"+profileFrom(ctx)+":"+viewerScopeDigest(ctx), fmt.Sprintf("%t:%d:%d", s.Enabled, s.DefaultPollIntervalSeconds, s.DebounceSeconds), 1)
}
func registerAdminAutoscanInspection(reg *Registry) {
	op := func(path, id, summary string) Operation {
		return Operation{Operation: humaOp("GET", Prefix+"/admin/autoscan/"+path, id, "admin-autoscan", summary), Class: ClassActingAdmin, ServiceBacked: true}
	}
	settings := op("settings", "getAdminAutoscanSettings", "Read desired autoscan configuration independently of scheduler state.")
	settings.Conditional = true
	Register(reg, settings, func(ctx context.Context, in *AdminAutoscanSettingsReadInput) (*AdminAutoscanSettingsOutput, error) {
		if reg.deps.AdminAutoscanInspection == nil {
			return nil, unavailable("autoscan")
		}
		s, err := reg.deps.AdminAutoscanInspection.ReadAdminAutoscanSettings(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		body := AdminAutoscanSettings{s.Enabled, s.DefaultPollIntervalSeconds, s.DebounceSeconds}
		tag := adminAutoscanSettingsTag(ctx, body)
		out := &AdminAutoscanSettingsOutput{ETag: tag.String(), Body: body}
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
	Register(reg, op("status", "getAdminAutoscanStatus", "Read existing source, running-poll and queue observations; not an atomic scheduler snapshot."), func(ctx context.Context, _ *struct{}) (*AdminAutoscanStatusOutput, error) {
		if reg.deps.AdminAutoscanInspection == nil {
			return nil, unavailable("autoscan")
		}
		s, err := reg.deps.AdminAutoscanInspection.ReadAdminAutoscanStatus(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		out := AdminAutoscanStatus{Enabled: s.Enabled, ActiveScans: s.ActiveScans, AcceptedScans: s.AcceptedScans, RunningScans: s.RunningScans, LatestEventAt: instantPtr(s.LatestEventAt), Sources: make([]AdminAutoscanStatusSource, 0, len(s.Sources)), RunningPolls: make([]AdminAutoscanRunningPoll, 0, len(s.RunningPolls))}
		for _, source := range s.Sources {
			row := AdminAutoscanStatusSource{ID: source.ID, PluginID: source.PluginID, CapabilityID: source.CapabilityID, ConnectionID: source.ConnectionID, Enabled: source.Enabled, Label: source.Label, LastRunAt: instantPtr(source.LastRunAt), LastError: source.LastError, PathRewrites: make([]AdminAutoscanPathRewrite, 0, len(source.PathRewrites))}
			for _, p := range source.PathRewrites {
				row.PathRewrites = append(row.PathRewrites, AdminAutoscanPathRewrite{p.From, p.To})
			}
			out.Sources = append(out.Sources, row)
		}
		for _, poll := range s.RunningPolls {
			out.RunningPolls = append(out.RunningPolls, AdminAutoscanRunningPoll{ID: IDFromInt(poll.ID), SourceID: poll.SourceID, PluginID: poll.PluginID, CapabilityID: poll.CapabilityID, StartedAt: NewInstant(poll.StartedAt), ElapsedMS: poll.ElapsedMS, MarkerBefore: poll.MarkerBefore})
		}
		return &AdminAutoscanStatusOutput{Body: out}, nil
	})
}
