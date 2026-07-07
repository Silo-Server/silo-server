package models

import "time"

// ExtraKind classifies supplemental video content. It is the shared vocabulary
// between remote provider videos (item_videos.kind) and scanner-discovered
// local extras (media_extras.kind). Values are lowercase snake_case strings
// rather than integer enums so plugins and future providers can emit kinds the
// server does not know yet; NormalizeExtraKind folds those to ExtraKindOther.
type ExtraKind string

const (
	ExtraKindTrailer         ExtraKind = "trailer"
	ExtraKindTeaser          ExtraKind = "teaser"
	ExtraKindFeaturette      ExtraKind = "featurette"
	ExtraKindClip            ExtraKind = "clip"
	ExtraKindBehindTheScenes ExtraKind = "behind_the_scenes"
	ExtraKindBloopers        ExtraKind = "bloopers"
	// ExtraKindDeletedScene is local-only: TMDB has no matching video type,
	// but Jellyfin/Plex folder conventions do.
	ExtraKindDeletedScene ExtraKind = "deleted_scene"
	ExtraKindOther        ExtraKind = "other"
)

// AllExtraKinds lists every known kind, in display order. This is the source
// of truth mirrored by the media_folders.trailer_kinds column default.
var AllExtraKinds = []ExtraKind{
	ExtraKindTrailer,
	ExtraKindTeaser,
	ExtraKindFeaturette,
	ExtraKindClip,
	ExtraKindBehindTheScenes,
	ExtraKindBloopers,
	ExtraKindDeletedScene,
	ExtraKindOther,
}

// NormalizeExtraKind maps an arbitrary kind string onto the known vocabulary,
// folding unknown values to ExtraKindOther so newer providers degrade safely.
func NormalizeExtraKind(raw string) ExtraKind {
	switch ExtraKind(raw) {
	case ExtraKindTrailer, ExtraKindTeaser, ExtraKindFeaturette, ExtraKindClip,
		ExtraKindBehindTheScenes, ExtraKindBloopers, ExtraKindDeletedScene:
		return ExtraKind(raw)
	default:
		return ExtraKindOther
	}
}

// ItemVideo is a row in item_videos: a remote promotional/supplemental video
// (YouTube trailer, teaser, ...) attached to a media item by a metadata
// provider. Rows are replaced wholesale on refresh, keyed for dedup by
// (ContentID, Provider, ProviderKey).
type ItemVideo struct {
	ID          int64
	ContentID   string
	Provider    string // provider capability slug, e.g. "tmdb"
	ProviderKey string // provider-native video id (dedup key)
	Kind        ExtraKind
	Site        string // hosting site, e.g. "youtube"
	SiteKey     string // site-native video key (YouTube video id)
	Name        string
	Language    string // ISO 639-1, empty when unknown
	IsOfficial  bool
	SizeHint    int // vertical resolution hint (e.g. 1080), 0 when unknown
	PublishedAt *time.Time
	SortOrder   int
}

// MediaExtra is a row in media_extras: a scanner-discovered local extra
// (featurette, deleted scene, local trailer file, ...) belonging to a parent
// movie or series. Its ContentID is minted with contentid.ForLocal on the
// backing file path, making it a playable watch target through
// GetWatchDetail's fallback chain; access control is the parent's.
type MediaExtra struct {
	ContentID string
	ParentID  string // media_items.content_id of the owning movie/series
	Kind      ExtraKind
	Title     string
	SortOrder int
}
