package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type trendingAdminSectionLister interface {
	ListTrendingDiscoverSections(context.Context) ([]*PageSection, error)
}

// TrendingDemandLister discovers snapshot demand from server sections and
// profile overrides. It deliberately reads current state on every scheduled
// run so missed save-time refreshes recover without a separate demand index.
type TrendingDemandLister struct {
	admin  trendingAdminSectionLister
	users  userstore.UserLister
	stores userstore.UserStoreProvider
	logger *slog.Logger
}

func NewTrendingDemandLister(admin trendingAdminSectionLister, users userstore.UserLister, stores userstore.UserStoreProvider) *TrendingDemandLister {
	return &TrendingDemandLister{admin: admin, users: users, stores: stores, logger: slog.Default().With("component", "sections.trending_demand")}
}

func (l *TrendingDemandLister) ListTrendingDiscoverConfigs(ctx context.Context) ([]json.RawMessage, error) {
	adminSections, err := l.admin.ListTrendingDiscoverSections(ctx)
	if err != nil {
		return nil, err
	}
	configs := make([]json.RawMessage, 0, len(adminSections))
	adminByID := make(map[string]*PageSection, len(adminSections))
	for _, section := range adminSections {
		configs = append(configs, section.Config)
		adminByID[section.ID] = section
	}
	if l.users == nil || l.stores == nil {
		return configs, nil
	}

	users, err := l.users.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing users for trending demand: %w", err)
	}
	for _, user := range users {
		store, err := l.stores.ForUser(ctx, user.ID)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			l.logger.WarnContext(ctx, "trending demand: skipping unavailable user store", "user_id", user.ID, "error", err)
			continue
		}
		enumerator, ok := store.(userstore.SectionOverrideEnumerator)
		if !ok {
			l.logger.WarnContext(ctx, "trending demand: skipping user store without override enumeration", "user_id", user.ID)
			continue
		}
		overrides, err := enumerator.ListAllSectionOverrides(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			l.logger.WarnContext(ctx, "trending demand: skipping unreadable section overrides", "user_id", user.ID, "error", err)
			continue
		}
		for _, override := range overrides {
			if override.Hidden || override.Removed {
				continue
			}
			if override.SectionID != "" {
				base := adminByID[override.SectionID]
				if base == nil {
					continue
				}
				if override.Config != "" && override.Config != jsonNullLiteral {
					configs = append(configs, json.RawMessage(override.Config))
				}
				continue
			}

			sectionType := override.SectionType
			config := override.Config
			if override.IsUserAdded || override.SectionID == "" {
				if override.UserSectionType != "" {
					sectionType = override.UserSectionType
				}
				if override.UserConfig != "" {
					config = override.UserConfig
				}
			}
			if sectionType == string(SectionTrendingDiscover) {
				configs = append(configs, json.RawMessage(config))
			}
		}
	}
	return configs, nil
}
