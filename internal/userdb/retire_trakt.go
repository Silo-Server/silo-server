package userdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// retireTraktSectionOverrides rewrites the profile section overrides that
// point at Trakt, as the Postgres store does in
// 20261016131356_retire_trakt.sql: a Trakt trending config becomes TMDB
// weekly trending, a row the profile added for Trakt trending is hidden, and
// a collection row drops its Trakt marker. A config that does not parse is
// left alone.
func retireTraktSectionOverrides(tx *sql.Tx) error {
	ctx := context.Background()
	overrides, err := listTraktSectionOverrides(ctx, tx)
	if err != nil {
		return err
	}
	for _, o := range overrides {
		config, configTrending := retireTraktSectionConfig(o.config)
		userConfig, userTrending := retireTraktSectionConfig(o.userConfig)
		hide := o.sectionID == "" && (configTrending || userTrending)
		if config == o.config && userConfig == o.userConfig && !hide {
			continue
		}
		var newConfig, newUserConfig sql.NullString
		if config != o.config {
			newConfig = sql.NullString{String: config, Valid: true}
		}
		if userConfig != o.userConfig {
			newUserConfig = sql.NullString{String: userConfig, Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE profile_section_overrides
SET config = COALESCE(?, config),
    user_config = COALESCE(?, user_config),
    hidden = MAX(hidden, ?)
WHERE id = ?`, newConfig, newUserConfig, hide, o.id); err != nil {
			return fmt.Errorf("rewriting Trakt section override %s: %w", o.id, err)
		}
	}
	return nil
}

type traktSectionOverride struct {
	id, sectionID, config, userConfig string
}

// listTraktSectionOverrides reads the overrides whose configs mention Trakt.
func listTraktSectionOverrides(ctx context.Context, tx *sql.Tx) ([]traktSectionOverride, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, COALESCE(section_id, ''), COALESCE(config, ''), COALESCE(user_config, '')
FROM profile_section_overrides
WHERE config LIKE '%trakt%' OR user_config LIKE '%trakt%'`)
	if err != nil {
		return nil, fmt.Errorf("listing Trakt section overrides: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var overrides []traktSectionOverride
	for rows.Next() {
		var o traktSectionOverride
		if err := rows.Scan(&o.id, &o.sectionID, &o.config, &o.userConfig); err != nil {
			return nil, fmt.Errorf("reading Trakt section overrides: %w", err)
		}
		overrides = append(overrides, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading Trakt section overrides: %w", err)
	}
	return overrides, nil
}

// retireTraktSectionConfig returns raw with its Trakt source replaced, and
// whether it was a Trakt trending config. Other configs come back unchanged.
func retireTraktSectionConfig(raw string) (string, bool) {
	var config map[string]json.RawMessage
	if raw == "" || json.Unmarshal([]byte(raw), &config) != nil || config == nil {
		return raw, false
	}
	trending := false
	switch {
	case jsonStringField(config, "source") == "trakt":
		config["source"] = json.RawMessage(`"tmdb"`)
		config["window"] = json.RawMessage(`"week"`)
		trending = true
	case jsonStringField(config, "source_provider") == "trakt":
		delete(config, "source_provider")
	default:
		return raw, false
	}
	rewritten, err := json.Marshal(config)
	if err != nil {
		return raw, false
	}
	return string(rewritten), trending
}

func jsonStringField(object map[string]json.RawMessage, key string) string {
	var value string
	if raw, ok := object[key]; !ok || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}
