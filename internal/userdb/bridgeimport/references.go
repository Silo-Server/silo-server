package bridgeimport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func validateSourceOwnership(ctx context.Context, source *sql.Tx) error {
	var profiles, primary int
	if err := source.QueryRowContext(ctx, "SELECT count(*),count(*) FILTER(WHERE is_primary=1) FROM profiles").Scan(&profiles, &primary); err != nil {
		return importFailure(ctx, "cannot inspect source profiles")
	}
	if profiles > 0 && primary != 1 {
		return errors.New("source primary profile is ambiguous")
	}
	for _, mapping := range Manifest() {
		if mapping.Target == "" {
			continue
		}
		for _, column := range sourceColumns[mapping.Source] {
			parent, key := "", ""
			switch column.Name {
			case "profile_id", "creator_profile_id":
				parent = "profiles"
				key = "id"
			case "collection_id":
				if mapping.Source == "personal_collection_items" || mapping.Source == "personal_collection_profiles" {
					parent = "personal_collections"
					key = "id"
				}
			}
			if parent == "" {
				continue
			}
			var invalid bool
			query := "SELECT EXISTS(SELECT 1 FROM " + pgx.Identifier{mapping.Source}.Sanitize() + " s WHERE s." + pgx.Identifier{column.Name}.Sanitize() + " IS NOT NULL AND NOT EXISTS(SELECT 1 FROM " + pgx.Identifier{parent}.Sanitize() + " p WHERE p." + pgx.Identifier{key}.Sanitize() + "=s." + pgx.Identifier{column.Name}.Sanitize() + "))"
			if err := source.QueryRowContext(ctx, query).Scan(&invalid); err != nil {
				return importFailure(ctx, "cannot inspect source ownership")
			}
			if invalid {
				return fmt.Errorf("orphaned source identity in %s", mapping.Source)
			}
		}
	}
	// Tombstones may outlive collections, but every live collection must have its
	// schema-22 witness. Otherwise the new target trigger would invent state.
	var missing bool
	if err := source.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM personal_collections c WHERE NOT EXISTS(SELECT 1 FROM personal_collection_revisions r WHERE r.collection_id=c.id))").Scan(&missing); err != nil {
		return importFailure(ctx, "cannot inspect source witnesses")
	}
	if missing {
		return errors.New("source collection witness is missing")
	}
	var witnesses int
	if err := source.QueryRowContext(ctx, "SELECT count(*) FROM personal_collection_order_revision WHERE singleton=1").Scan(&witnesses); err != nil || witnesses != 1 {
		return errors.New("source order witness is missing")
	}
	return nil
}

func validateTargetReferences(ctx context.Context, tx pgx.Tx, userID int) error {
	for _, mapping := range Manifest() {
		if mapping.Target == "" || mapping.Source == sourceSectionOverrides {
			continue
		}
		for _, column := range sourceColumns[mapping.Source] {
			ref := ""
			switch column.Name {
			case "library_id":
				ref = "SELECT 1 FROM media_folders p WHERE p.id=s.library_id"
			case "media_item_id":
				ref = "SELECT 1 FROM media_items p WHERE p.content_id=s.media_item_id UNION ALL SELECT 1 FROM episodes p WHERE p.content_id=s.media_item_id"
			case "series_id":
				ref = "SELECT 1 FROM media_items p WHERE p.content_id=s.series_id"
			case "last_file_id":
				ref = "SELECT 1 FROM media_files p WHERE p.id=s.last_file_id"
			}
			if ref == "" {
				continue
			}
			var invalid bool
			query := "SELECT EXISTS(SELECT 1 FROM " + pgx.Identifier{mapping.Target}.Sanitize() + " s WHERE user_id=$1 AND " + pgx.Identifier{column.Name}.Sanitize() + " IS NOT NULL AND NOT EXISTS(" + ref + "))"
			if err := tx.QueryRow(ctx, query, userID).Scan(&invalid); err != nil {
				return importFailure(ctx, "cannot verify catalog references")
			}
			if invalid {
				return fmt.Errorf("unresolved catalog reference in %s", mapping.Source)
			}
		}
	}
	var invalid bool
	for _, predicate := range []string{
		`collection_kind='user' AND NOT EXISTS(SELECT 1 FROM user_personal_collections c WHERE c.user_id=s.user_id AND c.id=s.collection_id)`,
		`collection_kind='library' AND NOT EXISTS(SELECT 1 FROM library_collections c WHERE c.id=s.collection_id)`,
	} {
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM user_collection_sort_preferences s WHERE user_id=$1 AND "+predicate+")", userID).Scan(&invalid); err != nil {
			return importFailure(ctx, "cannot verify sort preference references")
		}
		if invalid {
			return errors.New("unresolved collection sort reference")
		}
	}
	return nil
}
