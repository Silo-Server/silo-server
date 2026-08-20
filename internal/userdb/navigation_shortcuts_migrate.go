package userdb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	maxNavigationShortcuts         = 256
	maxMigratedNavigationLibraryID = int64(2_147_483_647)
)

type legacySidebarPin struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

// migrateSidebarPinsToNavigationShortcuts copies the subset of the legacy web
// map that has a concrete positive library id into the new cross-client item
// catalog. The old ui.sidebar_pins row remains untouched, including
// non-numeric well-known groups; a pre-existing nav.shortcuts row always wins.
func migrateSidebarPinsToNavigationShortcuts(tx *sql.Tx) error {
	rows, err := tx.Query(`
SELECT legacy.profile_id, legacy.value, legacy.created_at, legacy.updated_at
  FROM user_setting_values AS legacy
 WHERE legacy.key = 'ui.sidebar_pins'
   AND legacy.scope = 'profile'
   AND legacy.value <> 'null'
   AND NOT EXISTS (
       SELECT 1
         FROM user_setting_values AS current
        WHERE current.key = 'nav.shortcuts'
          AND current.scope = 'profile'
          AND current.profile_id = legacy.profile_id
   )
 ORDER BY legacy.profile_id`)
	if err != nil {
		return fmt.Errorf("reading sidebar pins for navigation shortcut migration: %w", err)
	}
	type source struct {
		profileID, raw, createdAt, updatedAt string
	}
	var sources []source
	for rows.Next() {
		var entry source
		if err := rows.Scan(&entry.profileID, &entry.raw, &entry.createdAt, &entry.updatedAt); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scanning sidebar pins for navigation shortcut migration: %w", err)
		}
		sources = append(sources, entry)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("closing sidebar pins migration rows: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating sidebar pins for navigation shortcut migration: %w", err)
	}

	for _, source := range sources {
		value, ok := navigationShortcutsFromSidebarPins(source.raw)
		if !ok {
			continue
		}
		if _, err := tx.Exec(`
INSERT OR IGNORE INTO user_setting_values
    (key, scope, profile_id, client_family, device_id, library_id, series_id,
     value, revision, created_at, updated_at)
VALUES ('nav.shortcuts', 'profile', ?, NULL, NULL, NULL, NULL, ?, 1, ?, ?)`,
			source.profileID, string(value), source.createdAt, source.updatedAt,
		); err != nil {
			return fmt.Errorf("seeding navigation shortcuts for profile %q: %w", source.profileID, err)
		}
	}
	return nil
}

func navigationShortcutsFromSidebarPins(raw string) (json.RawMessage, bool) {
	payload := []byte(raw)
	// The legacy web client sometimes persisted the whole object as a JSON
	// string. Match parseSidebarPins by unwrapping exactly one such layer before
	// interpreting the grouped pin map.
	var encoded string
	if err := json.Unmarshal(payload, &encoded); err == nil {
		payload = []byte(encoded)
	}

	// Decode the object one group at a time. A typed map[string][]T makes one
	// non-array group or one malformed member reject every valid sibling in the
	// profile. Raw group/member boundaries match PostgreSQL's jsonb_each plus
	// jsonb_array_elements behavior: malformed siblings are skipped in place.
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(payload, &legacy); err != nil {
		return nil, false
	}

	items := make([]map[string]any, 0)
	seen := make(map[string]struct{})
	// JSON object order is not semantic and Go deliberately randomizes map
	// iteration. Sort valid numeric groups by library id, matching the
	// PostgreSQL migration's ORDER BY rather than lexical string order.
	type libraryGroup struct {
		key string
		id  int
	}
	groups := make([]libraryGroup, 0, len(legacy))
	for group := range legacy {
		libraryID, err := strconv.ParseInt(group, 10, 32)
		if err != nil || libraryID <= 0 || libraryID > maxMigratedNavigationLibraryID ||
			strconv.FormatInt(libraryID, 10) != group {
			continue
		}
		groups = append(groups, libraryGroup{key: group, id: int(libraryID)})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].id < groups[j].id })

	for _, group := range groups {
		var encodedPins []json.RawMessage
		if err := json.Unmarshal(legacy[group.key], &encodedPins); err != nil {
			continue
		}
		for _, encodedPin := range encodedPins {
			var pin legacySidebarPin
			if err := json.Unmarshal(encodedPin, &pin); err != nil {
				continue
			}
			id := pin.ID
			label := pin.Label
			if strings.TrimSpace(id) == "" || len([]rune(id)) > 128 ||
				strings.TrimSpace(label) == "" || len([]rune(label)) > 256 {
				continue
			}
			identity := pin.Type + "\x00" + strconv.Itoa(group.id) + "\x00" + id
			if _, duplicate := seen[identity]; duplicate {
				continue
			}
			var item map[string]any
			switch pin.Type {
			case "section":
				item = map[string]any{
					"type": "section", "library_id": group.id,
					"section_id": id, "label": label,
				}
			case "collection":
				item = map[string]any{
					"type": "collection", "library_id": group.id,
					"collection_id": id, "label": label,
				}
			default:
				continue
			}
			seen[identity] = struct{}{}
			items = append(items, item)
			if len(items) == maxNavigationShortcuts {
				break
			}
		}
		if len(items) == maxNavigationShortcuts {
			break
		}
	}
	if len(items) == 0 {
		return nil, false
	}
	value, err := json.Marshal(map[string]any{"items": items})
	return value, err == nil
}
