package apiv2

import (
	"cmp"
	"context"
	"slices"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

type AdminAutoscanSourcesService interface {
	ReadAdminAutoscanSources(context.Context) ([]handlers.AdminAutoscanSourceView, error)
}
type AdminAutoscanPathRewrite struct {
	From string `json:"from"`
	To   string `json:"to"`
}
type AdminAutoscanSource struct {
	ID                      string                     `json:"id"`
	PluginID                string                     `json:"plugin_id"`
	CapabilityID            string                     `json:"capability_id"`
	ConnectionID            *string                    `json:"connection_id"`
	Enabled                 bool                       `json:"enabled"`
	DeliveryMode            string                     `json:"delivery_mode" enum:"poll,webhook"`
	PollIntervalSeconds     *int                       `json:"poll_interval_seconds,omitempty"`
	PathRewrites            []AdminAutoscanPathRewrite `json:"path_rewrites"`
	SourceConfig            map[string]string          `json:"source_config"`
	Label                   string                     `json:"label"`
	LastRunAt               *Instant                   `json:"last_run_at,omitempty"`
	LastError               *string                    `json:"last_error,omitempty"`
	WebhookConfigured       bool                       `json:"webhook_configured"`
	WebhookURL              string                     `json:"webhook_url,omitempty"`
	WebhookSecretSuffix     string                     `json:"webhook_secret_suffix,omitempty"`
	WebhookLastReceivedAt   *Instant                   `json:"webhook_last_received_at,omitempty"`
	WebhookLastErrorAt      *Instant                   `json:"webhook_last_error_at,omitempty"`
	WebhookLastErrorMessage string                     `json:"webhook_last_error_message,omitempty"`
}
type AdminAutoscanSourcesInput struct {
	LimitParam
	Cursor string `query:"cursor" maxLength:"8192"`
}
type AdminAutoscanSourcesOutput struct {
	Body Collection[AdminAutoscanSource]
}
type adminAutoscanSourcePosition struct{ Label, ID string }

func compareAdminAutoscanSourcePosition(a, b adminAutoscanSourcePosition) int {
	return cmp.Or(cmp.Compare(a.Label, b.Label), cmp.Compare(a.ID, b.ID))
}
func adminAutoscanSourceOf(s handlers.AdminAutoscanSourceView) AdminAutoscanSource {
	out := AdminAutoscanSource{
		ID:                      s.ID,
		PluginID:                s.PluginID,
		CapabilityID:            s.CapabilityID,
		ConnectionID:            s.ConnectionID,
		Enabled:                 s.Enabled,
		DeliveryMode:            s.DeliveryMode,
		PollIntervalSeconds:     s.PollIntervalSeconds,
		SourceConfig:            s.SourceConfig,
		Label:                   s.Label,
		LastRunAt:               instantPtr(s.LastRunAt),
		LastError:               s.LastError,
		WebhookConfigured:       s.WebhookConfigured,
		WebhookURL:              s.WebhookURL,
		WebhookSecretSuffix:     s.WebhookSecretSuffix,
		WebhookLastReceivedAt:   instantPtr(s.WebhookLastReceivedAt),
		WebhookLastErrorAt:      instantPtr(s.WebhookLastErrorAt),
		WebhookLastErrorMessage: s.WebhookLastErrorMessage,
	}
	out.PathRewrites = make([]AdminAutoscanPathRewrite, 0, len(s.PathRewrites))
	for _, p := range s.PathRewrites {
		out.PathRewrites = append(out.PathRewrites, AdminAutoscanPathRewrite{p.From, p.To})
	}
	return out
}
func registerAdminAutoscanSources(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp("GET", Prefix+"/admin/autoscan/sources", "listAdminAutoscanSources", "admin-autoscan", "Read configured scan sources and existing webhook URLs. Each response page uses full configured-source enumeration."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminAutoscanSourcesInput) (*AdminAutoscanSourcesOutput, error) {
		if reg.deps.AdminAutoscanSources == nil {
			return nil, unavailable("autoscan sources")
		}
		scope := CursorScope{OperationID: "listAdminAutoscanSources", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: strconv.Itoa(in.Limit), Sort: "label", Tiebreaker: "id"}
		var after adminAutoscanSourcePosition
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
		}
		rows, err := reg.deps.AdminAutoscanSources.ReadAdminAutoscanSources(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		rows = slices.Clone(rows)
		slices.SortFunc(rows, func(a, b handlers.AdminAutoscanSourceView) int {
			return compareAdminAutoscanSourcePosition(adminAutoscanSourcePosition{a.Label, a.ID}, adminAutoscanSourcePosition{b.Label, b.ID})
		})
		items := make([]AdminAutoscanSource, 0, in.Limit)
		next := ""
		var last adminAutoscanSourcePosition
		for _, row := range rows {
			pos := adminAutoscanSourcePosition{row.Label, row.ID}
			if in.Cursor != "" && compareAdminAutoscanSourcePosition(pos, after) <= 0 {
				continue
			}
			if len(items) == in.Limit {
				next, err = cursors.Encode(scope, last)
				if err != nil {
					return nil, serviceProblem(err)
				}
				break
			}
			items = append(items, adminAutoscanSourceOf(row))
			last = pos
		}
		return &AdminAutoscanSourcesOutput{Body: Paginated(items, next)}, nil
	})
}
