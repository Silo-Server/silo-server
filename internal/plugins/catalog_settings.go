package plugins

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type CatalogSettings struct {
	IncludeApprovedCommunityPlugins bool
	ApprovedCommunityPluginCount    int
	InstalledCommunityPluginCount   int
	MigratedPluginCount             int
	CommunityUpdatesPaused          bool
}

type ManagedRepositoryReconcileResult struct {
	RepositoriesCreated int
}

func (s *RepositoryStore) ReconcileManaged(ctx context.Context) (ManagedRepositoryReconcileResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ManagedRepositoryReconcileResult{}, fmt.Errorf("begin managed plugin repository reconciliation: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO server_settings (key, value)
		VALUES ($1, 'false')
		ON CONFLICT (key) DO NOTHING
	`, IncludeApprovedCommunityPluginsSetting); err != nil {
		return ManagedRepositoryReconcileResult{}, fmt.Errorf("seed approved community plugin setting: %w", err)
	}

	includeCommunity, err := readIncludeApprovedCommunityForUpdate(ctx, tx)
	if err != nil {
		return ManagedRepositoryReconcileResult{}, err
	}
	created, err := reconcileManagedRepositories(ctx, tx, includeCommunity)
	if err != nil {
		return ManagedRepositoryReconcileResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return ManagedRepositoryReconcileResult{}, fmt.Errorf("commit managed plugin repository reconciliation: %w", err)
	}
	return ManagedRepositoryReconcileResult{RepositoriesCreated: created}, nil
}

func (s *RepositoryStore) GetCatalogSettings(ctx context.Context) (CatalogSettings, error) {
	includeCommunity, err := readIncludeApprovedCommunity(ctx, s.pool)
	if err != nil {
		return CatalogSettings{}, err
	}

	var installedCommunityCount int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM plugin_installations AS installation
		JOIN plugin_repositories AS repository ON repository.id = installation.repository_id
		WHERE repository.source_kind = $1
	`, RepositorySourceApprovedCommunity).Scan(&installedCommunityCount); err != nil {
		return CatalogSettings{}, fmt.Errorf("count installed approved community plugins: %w", err)
	}

	migratedPluginCount, err := readIntegerSetting(ctx, s.pool, MigratedApprovedCommunityCountSetting)
	if err != nil {
		return CatalogSettings{}, err
	}

	return CatalogSettings{
		IncludeApprovedCommunityPlugins: includeCommunity,
		ApprovedCommunityPluginCount:    len(approvedCommunityPluginIDs),
		InstalledCommunityPluginCount:   installedCommunityCount,
		MigratedPluginCount:             migratedPluginCount,
		CommunityUpdatesPaused:          !includeCommunity && installedCommunityCount > 0,
	}, nil
}

func (s *RepositoryStore) SetIncludeApprovedCommunity(ctx context.Context, include bool) (CatalogSettings, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CatalogSettings{}, fmt.Errorf("begin approved community plugin setting update: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO server_settings (key, value)
		VALUES ($1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value
	`, IncludeApprovedCommunityPluginsSetting, strconv.FormatBool(include)); err != nil {
		return CatalogSettings{}, fmt.Errorf("update approved community plugin setting: %w", err)
	}

	if _, err := reconcileManagedRepositories(ctx, tx, include); err != nil {
		return CatalogSettings{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CatalogSettings{}, fmt.Errorf("commit approved community plugin setting update: %w", err)
	}

	return s.GetCatalogSettings(ctx)
}

type catalogSettingsQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type catalogSettingsExecutor interface {
	catalogSettingsQuerier
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func readIncludeApprovedCommunity(ctx context.Context, querier catalogSettingsQuerier) (bool, error) {
	return readIncludeApprovedCommunityQuery(ctx, querier, `SELECT value FROM server_settings WHERE key = $1`)
}

func readIncludeApprovedCommunityForUpdate(ctx context.Context, querier catalogSettingsQuerier) (bool, error) {
	return readIncludeApprovedCommunityQuery(ctx, querier, `SELECT value FROM server_settings WHERE key = $1 FOR UPDATE`)
}

func readIncludeApprovedCommunityQuery(ctx context.Context, querier catalogSettingsQuerier, query string) (bool, error) {
	var value string
	err := querier.QueryRow(ctx, query, IncludeApprovedCommunityPluginsSetting).Scan(&value)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("read approved community plugin setting: %w", err)
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, nil
	}
	return parsed, nil
}

func readIntegerSetting(ctx context.Context, querier catalogSettingsQuerier, key string) (int, error) {
	var value string
	err := querier.QueryRow(ctx, `SELECT value FROM server_settings WHERE key = $1`, key).Scan(&value)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("read plugin setting %q: %w", key, err)
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0, nil
	}
	return parsed, nil
}

func reconcileManagedRepositories(ctx context.Context, executor catalogSettingsExecutor, includeCommunity bool) (int, error) {
	created := 0
	for _, definition := range managedRepositoryDefinitions {
		var exists bool
		if err := executor.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plugin_repositories WHERE url = $1)`, definition.URL).Scan(&exists); err != nil {
			return 0, fmt.Errorf("check managed plugin repository %q: %w", definition.Key, err)
		}

		enabled := definition.Key == OfficialRepositoryManagedKey || includeCommunity
		if _, err := executor.Exec(ctx, `
			INSERT INTO plugin_repositories (url, display_name, enabled, managed_key, source_kind)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (url) DO UPDATE SET
				display_name = EXCLUDED.display_name,
				enabled = EXCLUDED.enabled,
				managed_key = EXCLUDED.managed_key,
				source_kind = EXCLUDED.source_kind,
				updated_at = NOW()
		`, definition.URL, definition.DisplayName, enabled, definition.Key, definition.SourceKind); err != nil {
			return 0, fmt.Errorf("reconcile managed plugin repository %q: %w", definition.Key, err)
		}
		if !exists {
			created++
		}
	}
	return created, nil
}
