// Package usercollections owns the import + sync logic for personal,
// profile-scoped collections (the user-facing analogue of the admin
// library_collections subsystem).
//
// User collections live in user_personal_collections and use external sources
// (TMDB / Trakt / MDBList) the same way admin collections do, but resolved
// against the entire catalog. Per-user library access is enforced at read
// time by the catalog resolver, so the sync service does not have to scope
// item resolution to a particular set of libraries.
package usercollections

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/collectionutil"
)

// NormalizeMDBListURL accepts either a list page URL
// (https://mdblist.com/lists/user/slug) or its JSON variant and returns the
// canonical JSON URL. Trailing slashes are tolerated. Empty input is
// returned unchanged so callers can keep their own validation.
func NormalizeMDBListURL(url string) string {
	return collectionutil.NormalizeMDBListURL(url)
}

type SourceMode string

const (
	SourceModeMDBList     SourceMode = "mdblist_json"
	SourceModeTMDBPreset  SourceMode = "tmdb_preset"
	SourceModeTraktPreset SourceMode = "trakt_preset"
)

// MinSyncIntervalHours is the smallest interval (in hours between fires) that
// a user is allowed to schedule. Stricter than admin to keep TMDB/Trakt API
// quota bounded across many users.
const MinSyncIntervalHours = 24

var ErrSyncUnsupported = errors.New("collection cannot be synced")

type SourceConfig struct {
	Mode       SourceMode `json:"mode"`
	URL        string     `json:"url,omitempty"`
	Preset     string     `json:"preset,omitempty"`
	Provider   string     `json:"provider,omitempty"`
	MediaType  string     `json:"media_type,omitempty"`
	TimeWindow string     `json:"time_window,omitempty"`
	ProfileID  string     `json:"profile_id,omitempty"`
	Limit      *int       `json:"limit,omitempty"`
	// LibraryIDs narrows sync resolution to these libraries. Empty/nil means
	// resolve against every library the requesting user can access.
	LibraryIDs []int `json:"library_ids,omitempty"`
}

func MarshalSourceConfig(cfg SourceConfig) (string, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func ParseSourceConfig(raw string) (SourceConfig, error) {
	var cfg SourceConfig
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// DisplayURL produces a stable, human-friendly identifier for the source —
// stored on the row so list views can render it without reparsing
// SourceConfig. Mirrors the admin tmdb://… / trakt://… scheme.
func (c SourceConfig) DisplayURL() string {
	switch c.Mode {
	case SourceModeMDBList:
		return c.URL
	case SourceModeTMDBPreset:
		if c.Preset == "trending" {
			return fmt.Sprintf("tmdb://%s/%s/%s", c.Preset, c.MediaType, c.TimeWindow)
		}
		return fmt.Sprintf("tmdb://%s/%s", c.Preset, c.MediaType)
	case SourceModeTraktPreset:
		if c.Preset == "recommended" {
			return fmt.Sprintf("trakt://%s/%s/%s", c.Preset, c.MediaType, c.ProfileID)
		}
		return fmt.Sprintf("trakt://%s/%s", c.Preset, c.MediaType)
	default:
		return ""
	}
}
