package bridgeimport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func importSections(ctx context.Context, source *sql.Tx, target pgx.Tx, userID int) (TableVerification, error) {
	rows, err := source.QueryContext(ctx, `SELECT id,profile_id,scope,COALESCE(library_id,''),COALESCE(section_id,''),position,hidden,removed,COALESCE(section_type,''),COALESCE(title,''),featured,item_limit,COALESCE(config,''),is_user_added,COALESCE(user_section_type,''),COALESCE(user_config,''),COALESCE(user_title,''),created_at,updated_at FROM profile_section_overrides ORDER BY scope,COALESCE(library_id,''),id`)
	if err != nil {
		return TableVerification{}, importFailure(ctx, "cannot read section overrides")
	}
	defer rows.Close() //nolint:errcheck
	digest := sha256.New()
	var count int64
	var key string
	var group []userstore.SectionOverride
	var size int
	flush := func() error {
		if len(group) == 0 {
			return nil
		}
		encoded, err := json.Marshal(group)
		if err != nil {
			return errors.New("cannot encode section overrides")
		}
		if _, err := target.Exec(ctx, "INSERT INTO user_settings(user_id,key,value) VALUES($1,$2,$3)", userID, key, string(encoded)); err != nil {
			return importFailure(ctx, "section override key conflicts with imported settings")
		}
		var stored string
		if err := target.QueryRow(ctx, "SELECT value FROM user_settings WHERE user_id=$1 AND key=$2", userID, key).Scan(&stored); err != nil || stored != string(encoded) {
			return importFailure(ctx, "section override verification failed")
		}
		group = nil
		size = 0
		return nil
	}
	for rows.Next() {
		var override userstore.SectionOverride
		var hidden, removed, added int64
		var featured sql.NullInt64
		if err := rows.Scan(&override.ID, &override.ProfileID, &override.Scope, &override.LibraryID, &override.SectionID, &override.Position, &hidden, &removed, &override.SectionType, &override.Title, &featured, &override.ItemLimit, &override.Config, &added, &override.UserSectionType, &override.UserConfig, &override.UserTitle, &override.CreatedAt, &override.UpdatedAt); err != nil {
			return TableVerification{}, importFailure(ctx, "invalid section override representation")
		}
		for _, flag := range []int64{hidden, removed, added} {
			if flag != 0 && flag != 1 {
				return TableVerification{}, errors.New("invalid section override boolean")
			}
		}
		if featured.Valid {
			if featured.Int64 != 0 && featured.Int64 != 1 {
				return TableVerification{}, errors.New("invalid section override boolean")
			}
			override.Featured = new(featured.Int64 == 1)
		}
		override.Hidden = hidden == 1
		override.Removed = removed == 1
		override.IsUserAdded = added == 1
		for _, instant := range []string{override.CreatedAt, override.UpdatedAt} {
			if _, err := importValue(SourceColumn{Kind: instantTextColumn}, instant); err != nil {
				return TableVerification{}, errors.New("invalid section override timestamp")
			}
		}
		if override.Scope != "home" && override.Scope != "library" {
			return TableVerification{}, errors.New("invalid section override scope")
		}
		for _, text := range []string{override.ID, override.ProfileID, override.LibraryID, override.SectionID, override.SectionType, override.Title, override.Config, override.UserSectionType, override.UserConfig, override.UserTitle} {
			if _, err := importValue(SourceColumn{Kind: textColumn}, text); err != nil {
				return TableVerification{}, errors.New("unrepresentable section override text")
			}
		}
		if override.Scope == "library" || override.LibraryID != "" {
			libraryID, err := strconv.Atoi(override.LibraryID)
			if err != nil || libraryID <= 0 {
				return TableVerification{}, errors.New("invalid section library identity")
			}
			var exists bool
			if err := target.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM media_folders WHERE id=$1)", libraryID).Scan(&exists); err != nil || !exists {
				return TableVerification{}, importFailure(ctx, "unresolved section library identity")
			}
		}
		nextKey := "section_overrides:" + override.Scope + ":" + override.LibraryID
		if nextKey != key {
			if err := flush(); err != nil {
				return TableVerification{}, err
			}
			key = nextKey
		}
		encoded, err := json.Marshal(override)
		if err != nil {
			return TableVerification{}, errors.New("invalid section override")
		}
		if err := validateImportJSON(string(encoded)); err != nil {
			return TableVerification{}, errors.New("unrepresentable section override")
		}
		size += len(encoded)
		if size > maxImportedValueBytes {
			return TableVerification{}, errors.New("section override group exceeds import limit")
		}
		group = append(group, override)
		_, _ = digest.Write(append(encoded, '\n'))
		count++
	}
	if err := rows.Err(); err != nil {
		return TableVerification{}, importFailure(ctx, "cannot read section overrides")
	}
	if err := flush(); err != nil {
		return TableVerification{}, err
	}
	return TableVerification{Rows: count, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}
