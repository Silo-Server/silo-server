package apiv2

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

type AdminPluginRepositoriesService interface {
	List(context.Context) ([]*plugins.Repository, error)
}
type AdminPluginRepository struct {
	ID            string   `json:"id"`
	URL           string   `json:"url"`
	DisplayName   string   `json:"display_name"`
	Enabled       bool     `json:"enabled"`
	SourceKind    string   `json:"source_kind"`
	Managed       bool     `json:"managed"`
	LastFetchedAt *Instant `json:"last_fetched_at,omitempty"`
	CreatedAt     Instant  `json:"created_at"`
	UpdatedAt     Instant  `json:"updated_at"`
}
type AdminPluginRepositoriesInput struct {
	LimitParam
	Cursor string `query:"cursor" maxLength:"8192"`
}
type AdminPluginRepositoriesOutput struct {
	Body Collection[AdminPluginRepository]
}

func registerAdminPluginRepositories(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, Operation{Operation: humaOp("GET", Prefix+"/admin/plugins/repositories", "listAdminPluginRepositories", "admin-plugins", "Read stored repository configuration without remote catalog fetches. Every page enumerates the full stored list; continuation is live, not a snapshot."), Class: ClassActingAdmin, ServiceBacked: true}, func(ctx context.Context, in *AdminPluginRepositoriesInput) (*AdminPluginRepositoriesOutput, error) {
		if reg.deps.AdminPluginRepositories == nil {
			return nil, unavailable("plugin repositories")
		}
		scope := CursorScope{OperationID: "listAdminPluginRepositories", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx) + "/" + viewerScopeDigest(ctx), Filter: strconv.Itoa(in.Limit), Sort: "id", Tiebreaker: "id"}
		var after int
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
		}
		rows, err := reg.deps.AdminPluginRepositories.List(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		rows = slices.Clone(rows)
		seen := map[int]bool{}
		for _, r := range rows {
			if r == nil || r.ID <= 0 || seen[r.ID] {
				return nil, serviceProblem(errors.New("invalid repository identity"))
			}
			seen[r.ID] = true
		}
		slices.SortFunc(rows, func(a, b *plugins.Repository) int { return cmp.Compare(a.ID, b.ID) })
		items := make([]AdminPluginRepository, 0, in.Limit)
		next := ""
		last := 0
		for _, r := range rows {
			if r.ID <= after {
				continue
			}
			if len(items) == in.Limit {
				next, err = cursors.Encode(scope, last)
				if err != nil {
					return nil, serviceProblem(err)
				}
				break
			}
			item := adminPluginRepositoryOf(r)
			items = append(items, item)
			last = r.ID
		}
		return &AdminPluginRepositoriesOutput{Body: Paginated(items, next)}, nil
	})
}

func adminPluginRepositoryOf(r *plugins.Repository) AdminPluginRepository {
	item := AdminPluginRepository{ID: strconv.Itoa(r.ID), URL: r.URL, DisplayName: r.DisplayName, Enabled: r.Enabled, SourceKind: r.SourceKind, Managed: r.ManagedKey != nil, CreatedAt: NewInstant(r.CreatedAt), UpdatedAt: NewInstant(r.UpdatedAt)}
	if r.LastFetchedAt != nil {
		item.LastFetchedAt = new(NewInstant(*r.LastFetchedAt))
	}
	return item
}
