package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type adminSectionLister interface {
	ListEnabledSections(context.Context, SectionType) ([]*PageSection, error)
}

// SectionDemandLister finds the effective configs of every enabled section of
// a type, from server sections and profile overrides, so background refreshers
// (trending snapshots, seasonal lists) know what to fetch. It deliberately
// reads current state on every scheduled run so missed save-time refreshes
// recover without a separate demand index.
type SectionDemandLister struct {
	admin  adminSectionLister
	users  userstore.UserLister
	stores userstore.UserStoreProvider
	logger *slog.Logger
}

func NewSectionDemandLister(admin adminSectionLister, users userstore.UserLister, stores userstore.UserStoreProvider) *SectionDemandLister {
	return &SectionDemandLister{admin: admin, users: users, stores: stores, logger: slog.Default().With("component", "sections.section_demand")}
}

// ListTrendingDiscoverConfigs lists the effective trending_discover configs.
func (l *SectionDemandLister) ListTrendingDiscoverConfigs(ctx context.Context) ([]json.RawMessage, error) {
	return l.ListConfigs(ctx, SectionTrendingDiscover)
}

// ListSeasonalConfigs lists the effective seasonal_themed configs.
func (l *SectionDemandLister) ListSeasonalConfigs(ctx context.Context) ([]json.RawMessage, error) {
	return l.ListConfigs(ctx, SectionSeasonalThemed)
}

// ListConfigs returns the effective config of every enabled section of
// sectionType: server sections, profile overrides of them, and sections a
// profile added itself.
func (l *SectionDemandLister) ListConfigs(ctx context.Context, sectionType SectionType) ([]json.RawMessage, error) {
	adminSections, err := l.admin.ListEnabledSections(ctx, sectionType)
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
		return nil, fmt.Errorf("listing users for section demand: %w", err)
	}
	for _, user := range users {
		store, err := l.stores.ForUser(ctx, user.ID)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			l.logger.WarnContext(ctx, "section demand: skipping unavailable user store", "user_id", user.ID, "error", err)
			continue
		}
		enumerator, ok := store.(userstore.SectionOverrideEnumerator)
		if !ok {
			l.logger.WarnContext(ctx, "section demand: skipping user store without override enumeration", "user_id", user.ID)
			continue
		}
		overrides, err := enumerator.ListAllSectionOverrides(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			l.logger.WarnContext(ctx, "section demand: skipping unreadable section overrides", "user_id", user.ID, "error", err)
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

			overrideType := override.SectionType
			config := override.Config
			if override.IsUserAdded || override.SectionID == "" {
				if override.UserSectionType != "" {
					overrideType = override.UserSectionType
				}
				if override.UserConfig != "" {
					config = override.UserConfig
				}
			}
			if overrideType == string(sectionType) {
				configs = append(configs, json.RawMessage(config))
			}
		}
	}
	return configs, nil
}
