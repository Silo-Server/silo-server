package jellycompat

import (
	"crypto/sha1"
	"encoding/hex"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
)

// allDetailFields is a sentinel passed to itemFromList so detail views include all fields.
var allDetailFields = map[string]bool{
	"*": true, "overview": true, "genres": true, "premieredate": true,
	"studios": true, "tags": true, "taglines": true, "etag": true,
	"sortname": true, "productionlocations": true, "criticrating": true,
	"providerids": true, "externalurls": true, "remotetrailers": true,
	"datecreated": true, "mediastreams": true, "path": true,
}

type mapper struct {
	codec          *ResourceIDCodec
	serverID       string
	imageTagSigner *imageTagSigner
}

func newMapper(codec *ResourceIDCodec, cfg *config.Config) *mapper {
	serverID := ""
	imageTagSecret := ""
	if cfg != nil {
		serverID = cfg.JellyfinCompat.ServerID
		imageTagSecret = cfg.Auth.JWTSecret
	}
	return &mapper{codec: codec, serverID: serverID, imageTagSigner: newImageTagSigner(imageTagSecret)}
}

func (m *mapper) viewFromLibrary(library upstreamUserLibrary) baseItemDTO {
	imgTags := map[string]string{}
	routeID := m.codec.EncodeIntID(EncodedIDLibrary, int64(library.ID))
	if library.PosterPath != "" {
		imgTags["Primary"] = m.imageTagSigner.Tag(
			imageTagSeed(routeID, "Primary", compatCardImageSize, library.PosterPath, "", time.Time{}),
			library.PosterURL,
		)
	}

	return baseItemDTO{
		ID:             routeID,
		Type:           "CollectionFolder",
		MediaType:      "Unknown",
		IsFolder:       true,
		Name:           library.Name,
		ServerID:       m.serverID,
		CollectionType: libraryCollectionType(library.Type),
		SortName:       strings.ToLower(library.Name),
		ImageTags:      imgTags,
		UserData: &itemUserDataDTO{
			Key:    routeID,
			ItemID: routeID,
		},
	}
}

func (m *mapper) itemFromList(item upstreamListItem, isFavorite bool, progress *upstreamProgress, fields map[string]bool) baseItemDTO {
	_, allFields := fields["*"] // detail views pass allDetailFields sentinel

	dto := baseItemDTO{
		ID:              m.codec.EncodeStringID(EncodedIDItem, item.ContentID),
		Type:            jellyfinItemType(item.Type),
		IsFolder:        jellyfinIsFolder(item.Type),
		Name:            item.Title,
		ServerID:        m.serverID,
		ProductionYear:  item.Year,
		OfficialRating:  item.ContentRating,
		CommunityRating: item.RatingIMDB,
		ImageTags:       map[string]string{},
		UserData:        userDataDTO(m.codec.EncodeStringID(EncodedIDItem, item.ContentID), item.UserData, isFavorite, progress),
	}

	if mt := jellyfinMediaType(item.Type); mt != "" {
		dto.MediaType = mt
	}
	if item.Runtime > 0 {
		dto.RunTimeTicks = minutesToTicks(item.Runtime)
	}
	if item.Type == "movie" || item.Type == "episode" {
		dto.LocationType = "FileSystem"
		dto.VideoType = "VideoFile"
	}
	if item.SeriesID != "" {
		dto.SeriesID = m.codec.EncodeStringID(EncodedIDItem, item.SeriesID)
		dto.SeriesName = item.SeriesTitle
	}
	if item.SeasonNumber != nil {
		dto.ParentIndexNumber = item.SeasonNumber
	}
	if item.EpisodeNumber != nil {
		dto.IndexNumber = item.EpisodeNumber
	}
	if item.EpisodeCount != nil {
		dto.ChildCount = *item.EpisodeCount
		dto.RecursiveItemCount = *item.EpisodeCount
	}
	if item.SeasonCount != nil {
		dto.ChildCount = *item.SeasonCount
		dto.RecursiveItemCount = *item.SeasonCount
	}
	primaryPath, primaryThumbhash := listItemPrimaryImageSeedParts(item)
	if tags := imageTagsWithSeed(m.imageTagSigner,
		imageTagSeed(item.ContentID, "Primary", compatCardImageSize, primaryPath, primaryThumbhash, item.UpdatedAt),
		item.PosterURL,
	); tags != nil {
		dto.ImageTags = tags
	}
	if tags := backdropTagsWithSeed(m.imageTagSigner,
		imageTagSeed(item.ContentID, "Backdrop", compatCardImageSize, item.BackdropPath, item.BackdropThumbhash, item.UpdatedAt),
		item.BackdropURL,
	); tags != nil {
		dto.BackdropImageTags = tags
	}
	if ratio := primaryAspectRatio(item.Type); ratio != nil {
		dto.PrimaryImageAspectRatio = ratio
	}

	// Optional fields — only included when explicitly requested via Fields param.
	if allFields || fields["overview"] {
		dto.Overview = item.Overview
	}
	if allFields || fields["genres"] {
		dto.Genres = nonNilStrings(item.Genres)
		dto.GenreItems = m.genreItems(item.Genres)
	}
	// PremiereDate is always included (part of Jellyfin's minimal default set).
	if item.AirDate != "" {
		dto.PremiereDate = ensureRFC3339(item.AirDate)
	}
	if allFields || fields["etag"] {
		dto.Etag = itemEtag(item)
	}
	if allFields || fields["sortname"] {
		dto.SortName = firstNonEmpty(item.SortTitle, item.Title)
	}
	if allFields || fields["studios"] {
		dto.Studios = m.namePairs(item.Studios, EncodedIDStudio)
	}
	if allFields || fields["taglines"] {
		if item.Tagline != "" {
			dto.Taglines = []string{item.Tagline}
		}
	}
	if allFields || fields["tags"] {
		dto.Tags = []string{}
	}
	if allFields || fields["productionlocations"] {
		dto.ProductionLocations = append([]string{}, item.Countries...)
	}
	if allFields || fields["criticrating"] {
		dto.CriticRating = item.RatingTMDB
	}
	if allFields || fields["mediasourcecount"] {
		// The list path has no version data, so assume matched playable items
		// have exactly one source. Unmatched/file-missing items leave this
		// unset so clients fall back to the detail path (which reports the
		// real count via len(item.Versions)).
		if isPlayableItemType(item.Type) && item.Status == "matched" {
			dto.MediaSourceCount = 1
		}
	}
	if allFields || fields["providerids"] {
		dto.ProviderIDs = map[string]string{}
	}
	// Fields that are never populated in list view — only include in detail.
	if allFields {
		dto.PlayAccess = "Full"
		dto.EnableMediaSourceDisplay = true
		dto.DisplayPreferencesID = m.codec.EncodeStringID(EncodedIDItem, item.ContentID)
		dto.ExternalURLs = []map[string]any{}
		dto.RemoteTrailers = []map[string]any{}
		dto.ImageBlurHashes = map[string]map[string]string{}
		dto.LockedFields = []string{}
		dto.Chapters = []map[string]any{}
		dto.Trickplay = map[string]any{}
		dto.MediaStreams = []mediaStreamDTO{}
	}

	return dto
}

// stubDetailPerson, stubDetailMediaSource, and stubDetailMediaStream are the
// single placeholder elements injected into a list-mapped DTO by
// stubDetailListFields when the client requested a detail-only Fields value
// but the endpoint (currently Resume) deliberately skips the per-item
// GetItemDetail fetch. Using a non-empty single-element slice keeps the
// field present in JSON regardless of the omitempty tag, so strict client
// deserializers see the same shape they'd get from a populated catalog
// (every real item has at least one media source, stream, and cast/crew
// entry — the empty-array case never happens in practice, so we should not
// rely on omitempty doing anything useful for these fields).
//
// The values are minimal: each carries the required (non-omitempty) fields
// only. Clients that consume this data downstream will see a single source
// with a stub ID, a single Video stream at index 0, and a single placeholder
// person; standard Jellyfin clients refetch real MediaSources via
// /Items/{id}/PlaybackInfo on play, so this stub is never load-bearing.
var (
	stubDetailPerson      = personDTO{ID: "0", Name: ""}
	stubDetailMediaSource = mediaSourceDTO{ID: "0"}
	stubDetailMediaStream = mediaStreamDTO{Index: 0, Type: "Video"}
)

// stubDetailListFields sets the four detail-only fields on a list-mapped DTO
// to single-element placeholder slices when the client requested them via
// Fields=People|Chapters|MediaStreams|MediaSources. Used by the Resume
// hydrator, which deliberately skips per-item GetItemDetail fanout; clients
// that need real MediaSources refetch them via /Items/{id}/PlaybackInfo on
// play.
func stubDetailListFields(dto *baseItemDTO, fields map[string]bool) {
	if len(fields) == 0 {
		return
	}
	if fields["people"] && dto.People == nil {
		dto.People = []personDTO{stubDetailPerson}
	}
	if fields["chapters"] && dto.Chapters == nil {
		// Constructed fresh per call because map values are reference-typed
		// and could be mutated by downstream code.
		dto.Chapters = []map[string]any{{"Name": "", "StartPositionTicks": int64(0)}}
	}
	if fields["mediastreams"] && dto.MediaStreams == nil {
		dto.MediaStreams = []mediaStreamDTO{stubDetailMediaStream}
	}
	if fields["mediasources"] && dto.MediaSources == nil {
		dto.MediaSources = []mediaSourceDTO{stubDetailMediaSource}
	}
}

// itemFromDetail maps a detail payload into a full baseItemDTO, including every
// heavy field (People, MediaSources, MediaStreams, Chapters). Use this from
// single-item detail endpoints where the client expects the full payload.
func (m *mapper) itemFromDetail(item upstreamItemDetail, isFavorite bool, progress *upstreamProgress) baseItemDTO {
	return m.itemFromDetailWithFields(item, isFavorite, progress, nil)
}

// itemFromDetailWithFields is the field-aware variant used by list endpoints
// that fall into the detail path. A nil requestedFields map means "all fields"
// (legacy detail-endpoint behavior). When non-nil, heavy nested arrays are only
// populated if the client explicitly requested them via the Fields query
// parameter — matching Jellyfin's opt-in semantics for People, MediaSources,
// MediaStreams, and Chapters.
func (m *mapper) itemFromDetailWithFields(item upstreamItemDetail, isFavorite bool, progress *upstreamProgress, requestedFields map[string]bool) baseItemDTO {
	allFields := requestedFields == nil
	wantField := func(name string) bool {
		return allFields || requestedFields[name]
	}

	dto := m.itemFromList(upstreamListItem{
		ContentID:         item.ContentID,
		Type:              item.Type,
		Title:             item.Title,
		SortTitle:         item.SortTitle,
		Year:              item.Year,
		Genres:            item.Genres,
		ContentRating:     item.ContentRating,
		RatingIMDB:        item.RatingIMDB,
		Overview:          item.Overview,
		PosterURL:         item.PosterURL,
		BackdropURL:       item.BackdropURL,
		PosterPath:        item.PosterPath,
		PosterThumbhash:   item.PosterThumbhash,
		BackdropPath:      item.BackdropPath,
		BackdropThumbhash: item.BackdropThumbhash,
		LogoPath:          item.LogoPath,
		UpdatedAt:         item.UpdatedAt,
		SeasonCount:       item.SeasonCount,
		SeriesID:          item.SeriesID,
		SeriesTitle:       item.SeriesTitle,
		SeasonNumber:      item.SeasonNumber,
		EpisodeNumber:     item.EpisodeNumber,
		EpisodeCount:      item.EpisodeCount,
		Runtime:           item.Runtime,
		AirDate:           derefString(item.AirDate),
		UserData:          item.UserData,
	}, isFavorite, progress, allDetailFields)

	if wantField("people") {
		dto.People = make([]personDTO, 0, len(item.Cast)+len(item.Crew))
		for _, cast := range item.Cast {
			personID, _ := strconv.ParseInt(cast.PersonID, 10, 64)
			dto.People = append(dto.People, personDTO{
				ID:              m.codec.EncodeIntID(EncodedIDPerson, personID),
				Name:            cast.Name,
				Role:            cast.Character,
				Type:            "Actor",
				PrimaryImageTag: tagValue(cast.PhotoURL),
			})
		}
		for _, crew := range item.Crew {
			personID, _ := strconv.ParseInt(crew.PersonID, 10, 64)
			dto.People = append(dto.People, personDTO{
				ID:              m.codec.EncodeIntID(EncodedIDPerson, personID),
				Name:            crew.Name,
				Role:            crew.Job,
				Type:            crew.Job,
				PrimaryImageTag: tagValue(crew.PhotoURL),
			})
		}
	}
	if item.SeriesID != "" {
		dto.SeriesID = m.codec.EncodeStringID(EncodedIDItem, item.SeriesID)
	}
	if item.Type == "season" {
		dto.ID = m.codec.EncodeStringID(EncodedIDSeason, item.ContentID)
	}
	dto.ServerID = m.serverID
	dto.CanDelete = false
	dto.OriginalTitle = firstNonEmpty(item.OriginalTitle, item.Title)
	dto.SortName = firstNonEmpty(item.SortTitle, item.OriginalTitle, item.Title)
	dto.ForcedSortName = dto.SortName
	dto.CriticRating = item.RatingTMDB
	dto.Studios = m.namePairs(item.Studios, EncodedIDStudio)
	dto.ProductionLocations = append([]string{}, item.Countries...)
	if item.Tagline != "" {
		dto.Taglines = []string{item.Tagline}
	}
	if item.AirDate != nil {
		dto.PremiereDate = ensureRFC3339(*item.AirDate)
	} else if item.Year > 0 {
		dto.PremiereDate = syntheticPremiereDate(item.Year)
	}
	if isPlayableItemType(item.Type) && len(item.Versions) > 0 {
		dto.MediaSourceCount = len(item.Versions)
		firstVersion := item.Versions[0]
		if firstVersion.Duration > 0 {
			dto.RunTimeTicks = secondsToTicks(float64(firstVersion.Duration))
		}
		dto.DateCreated = formatCompatTime(firstVersion.AddedAt)
		dto.CanDownload = true
		dto.HasSubtitles = versionsHaveSubtitles(item.Versions)
		dto.SupportsSync = false
		dto.Container = strings.ToLower(firstVersion.Container)
		dto.LocationType = "FileSystem"
		dto.VideoType = "VideoFile"
		dto.Path = compatMediaPath(firstVersion)
		if len(firstVersion.VideoTracks) > 0 {
			dto.Width = firstVersion.VideoTracks[0].Width
			dto.Height = firstVersion.VideoTracks[0].Height
			dto.IsHD = firstVersion.VideoTracks[0].Height >= 720 || firstVersion.VideoTracks[0].Width >= 1280
		}
		routeItemID := m.codec.EncodeStringID(EncodedIDItem, item.ContentID)
		wantMediaSources := wantField("mediasources")
		wantMediaStreams := wantField("mediastreams")
		if wantMediaSources {
			dto.MediaSources = make([]mediaSourceDTO, 0, len(item.Versions))
		}
		// Register every version's file ID as owned by this item, even when the
		// caller didn't ask for MediaSources/MediaStreams. Skipping this breaks
		// later /Items/{mediaSourceId} lookups (LookupMediaSourceOwner returns
		// ok=false → 404) whenever the detail was first materialized through a
		// list endpoint without those Fields.
		for _, version := range item.Versions {
			m.codec.RegisterMediaSourceOwner(int64(version.FileID), item.ContentID)
			if !wantMediaSources && !wantMediaStreams {
				continue
			}
			sourceID := m.codec.EncodeIntID(EncodedIDMediaSource, int64(version.FileID))
			streams := buildMediaStreams(routeItemID, sourceID, version)
			if wantMediaStreams {
				dto.MediaStreams = append(dto.MediaStreams, streams...)
			}
			if wantMediaSources {
				dto.MediaSources = append(dto.MediaSources, detailMediaSourceDTO(sourceID, version, streams))
			}
		}
		if wantField("chapters") {
			dto.Chapters = compatChapters(firstVersion.Chapters, firstVersion.AddedAt)
		}
	}
	slog.Info("jellycompat item detail mapped",
		"content_id", item.ContentID,
		"type", item.Type,
		"is_folder", dto.IsFolder,
		"media_type", dto.MediaType,
		"versions", len(item.Versions),
		"media_sources", len(dto.MediaSources),
	)
	return dto
}

func (m *mapper) seasonFromUpstream(season upstreamSeason, seriesID string, isFavorite bool) baseItemDTO {
	dto := baseItemDTO{
		ID:                 m.codec.EncodeStringID(EncodedIDSeason, season.ContentID),
		Type:               "Season",
		Name:               season.Title,
		IsFolder:           true,
		ServerID:           m.serverID,
		ImageTags:          map[string]string{},
		SeriesID:           m.codec.EncodeStringID(EncodedIDItem, seriesID),
		ParentID:           m.codec.EncodeStringID(EncodedIDItem, seriesID),
		UserData:           userDataDTO(m.codec.EncodeStringID(EncodedIDSeason, season.ContentID), season.UserData, isFavorite, nil),
		ChildCount:         season.EpisodeCount,
		RecursiveItemCount: season.EpisodeCount,
	}
	dto.IndexNumber = &season.SeasonNumber
	if tags := imageTagsWithSeed(m.imageTagSigner,
		imageTagSeed(season.ContentID, "Primary", compatCardImageSize, season.PosterPath, season.PosterThumbhash, season.UpdatedAt),
		season.PosterURL,
	); tags != nil {
		dto.ImageTags = tags
	}
	return dto
}

func (m *mapper) episodeFromUpstream(ep upstreamEpisode, isFavorite bool, progress *upstreamProgress) baseItemDTO {
	dto := baseItemDTO{
		ID:           m.codec.EncodeStringID(EncodedIDItem, ep.ContentID),
		Type:         "Episode",
		MediaType:    "Video",
		Name:         ep.Title,
		ServerID:     m.serverID,
		Overview:     ep.Overview,
		LocationType: "FileSystem",
		VideoType:    "VideoFile",
		RunTimeTicks: minutesToTicks(ep.Runtime),
		ImageTags:    map[string]string{},
		SeriesName:   ep.SeriesTitle,
		UserData:     userDataDTO(m.codec.EncodeStringID(EncodedIDItem, ep.ContentID), ep.UserData, isFavorite, progress),
	}
	dto.IndexNumber = &ep.EpisodeNumber
	dto.ParentIndexNumber = &ep.SeasonNumber
	if ep.SeriesID != "" {
		dto.SeriesID = m.codec.EncodeStringID(EncodedIDItem, ep.SeriesID)
	}
	if ep.SeasonID != "" {
		dto.SeasonID = m.codec.EncodeStringID(EncodedIDSeason, ep.SeasonID)
		dto.ParentID = m.codec.EncodeStringID(EncodedIDSeason, ep.SeasonID)
	}
	if tags := imageTagsWithSeed(m.imageTagSigner,
		imageTagSeed(ep.ContentID, "Primary", compatCardImageSize, ep.StillPath, ep.StillThumbhash, ep.UpdatedAt),
		ep.StillURL,
	); tags != nil {
		dto.ImageTags = tags
	}
	return dto
}

type seriesImageSet struct {
	ContentID         string
	PosterURL         string
	PosterPath        string
	PosterThumbhash   string
	BackdropURL       string
	BackdropPath      string
	BackdropThumbhash string
	UpdatedAt         time.Time
}

// applySeriesImages sets series/parent image tags on an episode DTO so clients
// can display the series poster and backdrop in Continue Watching / Next Up.
func (m *mapper) applySeriesImages(dto *baseItemDTO, series seriesImageSet) {
	if dto.SeriesID == "" {
		return
	}
	if series.PosterURL != "" {
		dto.SeriesPrimaryImageTag = m.imageTagSigner.Tag(
			imageTagSeed(series.ContentID, "Primary", compatCardImageSize, series.PosterPath, series.PosterThumbhash, series.UpdatedAt),
			series.PosterURL,
		)
	}
	if series.BackdropURL != "" {
		tag := m.imageTagSigner.Tag(
			imageTagSeed(series.ContentID, "Backdrop", compatCardImageSize, series.BackdropPath, series.BackdropThumbhash, series.UpdatedAt),
			series.BackdropURL,
		)
		dto.ParentBackdropImageTags = []string{tag}
		dto.ParentBackdropItemID = dto.SeriesID
		dto.ParentThumbImageTag = tag
		dto.ParentThumbItemID = dto.SeriesID
	}
}

func userDataDTO(itemID string, data *catalog.SeasonUserData, isFavorite bool, progress *upstreamProgress) *itemUserDataDTO {
	dto := &itemUserDataDTO{IsFavorite: isFavorite, ItemID: itemID, Key: itemID}

	if data != nil {
		dto.PlaybackPositionTicks = resumePositionTicks(data.PositionSeconds, data.DurationSeconds, data.Played)
		if data.DurationSeconds > 0 {
			dto.PlayedPercentage = (data.PositionSeconds / data.DurationSeconds) * 100
		}
		dto.Played = data.Played
		dto.UnplayedItemCount = data.UnplayedCount
		if data.Played {
			dto.PlayCount = 1
		}
	}

	if progress != nil {
		dto.PlaybackPositionTicks = resumePositionTicks(progress.PositionSeconds, progress.DurationSeconds, progress.Completed)
		if progress.DurationSeconds > 0 {
			dto.PlayedPercentage = (progress.PositionSeconds / progress.DurationSeconds) * 100
		}
		dto.Played = progress.Completed
		if progress.Completed {
			dto.PlayCount = 1
		}
		dto.LastPlayedDate = progress.UpdatedAt
	}

	return dto
}

func jellyfinItemType(native string) string {
	switch strings.ToLower(native) {
	case "movie":
		return "Movie"
	case "series":
		return "Series"
	case "episode":
		return "Episode"
	case "season":
		return "Season"
	default:
		if native == "" {
			return ""
		}
		return strings.ToUpper(native[:1]) + strings.ToLower(native[1:])
	}
}

func libraryCollectionType(native string) string {
	switch native {
	case "movies":
		return "movies"
	case "series":
		return "tvshows"
	default:
		return native
	}
}

func jellyfinMediaType(native string) string {
	switch strings.ToLower(native) {
	case "movie", "episode":
		return "Video"
	default:
		return ""
	}
}

func jellyfinIsFolder(native string) bool {
	switch strings.ToLower(native) {
	case "series", "season":
		return true
	default:
		return false
	}
}

func isPlayableItemType(native string) bool {
	switch strings.ToLower(native) {
	case "movie", "episode":
		return true
	default:
		return false
	}
}

func (m *mapper) genreItems(genres []string) []namePairDTO {
	return m.namePairs(genres, EncodedIDGenre)
}

func (m *mapper) namePairs(values []string, kind EncodedIDType) []namePairDTO {
	items := make([]namePairDTO, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		items = append(items, namePairDTO{
			Name: trimmed,
			ID:   m.codec.EncodeStringID(kind, trimmed),
		})
	}
	return items
}

func primaryAspectRatio(native string) *float64 {
	switch strings.ToLower(native) {
	case "movie", "series":
		value := 2.0 / 3.0 // portrait poster
		return &value
	case "episode":
		value := 16.0 / 9.0 // landscape still
		return &value
	default:
		return nil
	}
}

func versionsHaveSubtitles(versions []catalog.FileVersion) bool {
	for _, version := range versions {
		if len(version.SubtitleTracks) > 0 {
			return true
		}
	}
	return false
}

func compatChapters(chapters []catalog.VersionChapter, addedAt time.Time) []map[string]any {
	if len(chapters) == 0 {
		return []map[string]any{}
	}

	imageDateModified := formatCompatTime(addedAt)
	if imageDateModified == "" {
		imageDateModified = formatCompatTime(time.Unix(0, 0))
	}

	items := make([]map[string]any, 0, len(chapters))
	for _, chapter := range chapters {
		item := map[string]any{
			"StartPositionTicks": secondsToTicks(chapter.StartSeconds),
			"EndPositionTicks":   secondsToTicks(chapter.EndSeconds),
			"Name":               chapter.Title,
			"ImageDateModified":  imageDateModified,
		}
		if chapter.ThumbnailURL != "" {
			item["ImagePath"] = chapter.ThumbnailURL
		}
		if chapter.ThumbnailThumbhash != "" {
			item["ImageTag"] = chapter.ThumbnailThumbhash
		}
		items = append(items, item)
	}
	return items
}

func minutesToTicks(minutes int) int64 {
	return int64(minutes) * 600_000_000
}

func secondsToTicks(seconds float64) int64 {
	return int64(seconds * 10_000_000)
}

// resumePositionTicks returns the PlaybackPositionTicks value for a UserData
// DTO. When the item is fully watched the position is reported as 0 so clients
// start fresh on the next play; otherwise the position is clamped to the item
// duration so a stale/overflowed stored position cannot drive the player past
// end-of-file (which stalls the HLS transcoder on resume).
func resumePositionTicks(position, duration float64, played bool) int64 {
	if played {
		return 0
	}
	if duration > 0 && position > duration {
		position = duration
	}
	if position < 0 {
		position = 0
	}
	return secondsToTicks(position)
}

func imageTagsWithSeed(signer *imageTagSigner, seed, imageURL string) map[string]string {
	if imageURL == "" {
		return nil
	}
	return map[string]string{"Primary": signer.Tag(seed, imageURL)}
}

func backdropTags(imageURL string) []string {
	return backdropTagsWithSeed(nil, "", imageURL)
}

func backdropTagsWithSeed(signer *imageTagSigner, seed, imageURL string) []string {
	if imageURL == "" {
		return nil
	}
	return []string{signer.Tag(seed, imageURL)}
}

func listItemPrimaryImageSeedParts(item upstreamListItem) (string, string) {
	if item.Type == "episode" && item.StillPath != "" {
		return item.StillPath, item.StillThumbhash
	}
	return firstNonEmpty(item.PosterPath, item.StillPath), item.PosterThumbhash
}

func imageTagSeed(routeID, imageType, size, rawPath, thumbhash string, updatedAt time.Time) string {
	rawPath = strings.TrimSpace(rawPath)
	thumbhash = strings.TrimSpace(thumbhash)
	if rawPath == "" && thumbhash == "" && updatedAt.IsZero() {
		return ""
	}
	parts := []string{
		strings.TrimSpace(routeID),
		strings.ToLower(strings.TrimSpace(imageType)),
		normalizeImageCacheSize(size),
		rawPath,
		thumbhash,
	}
	if !updatedAt.IsZero() {
		parts = append(parts, updatedAt.UTC().Format(time.RFC3339Nano))
	}
	return strings.Join(parts, "\x00")
}

func tagValue(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha1.Sum([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

func itemEtag(item upstreamListItem) string {
	return tagValue(strings.Join([]string{
		item.ContentID,
		item.Title,
		strconv.Itoa(item.Year),
		item.AirDate,
		firstNonEmpty(item.PosterPath, item.PosterURL),
		firstNonEmpty(item.BackdropPath, item.BackdropURL),
		item.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}, ":"))
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func detailMediaSourceDTO(sourceID string, version catalog.FileVersion, streams []mediaStreamDTO) mediaSourceDTO {
	return mediaSourceDTO{
		Protocol:                            "File",
		ID:                                  sourceID,
		Path:                                compatMediaPath(version),
		Type:                                "Default",
		Container:                           strings.ToLower(version.Container),
		Size:                                version.FileSize,
		Name:                                mediaSourceName(version),
		IsRemote:                            false,
		ETag:                                mediaSourceETag(version),
		RunTimeTicks:                        secondsToTicks(float64(version.Duration)),
		ReadAtNativeFramerate:               false,
		IgnoreDts:                           false,
		IgnoreIndex:                         false,
		GenPtsInput:                         false,
		SupportsTranscoding:                 true,
		SupportsDirectStream:                true,
		SupportsDirectPlay:                  true,
		IsInfiniteStream:                    false,
		UseMostCompatibleTranscodingProfile: false,
		RequiresOpening:                     false,
		RequiresClosing:                     false,
		RequiresLooping:                     false,
		SupportsProbing:                     true,
		VideoType:                           "VideoFile",
		HasSegments:                         false,
		Formats:                             []string{strings.ToLower(version.Container)},
		RequiredHTTPHeaders:                 map[string]string{},
		MediaAttachments:                    []map[string]any{},
		TranscodingSubProtocol:              "hls",
		Bitrate:                             version.Bitrate * 1000,
		DefaultAudioStreamIndex:             defaultAudioStreamIndex(version),
		DefaultSubtitleStreamIndex:          defaultSubtitleStreamIndex(version),
		MediaStreams:                        streams,
	}
}

func mediaSourceName(version catalog.FileVersion) string {
	name := strings.TrimSpace(version.FileName)
	if name == "" {
		name = filepath.Base(version.FilePath)
	}
	if name == "" {
		return ""
	}
	ext := filepath.Ext(name)
	return strings.TrimSuffix(name, ext)
}

func compatMediaPath(version catalog.FileVersion) string {
	if strings.TrimSpace(version.FilePath) != "" {
		return version.FilePath
	}
	name := mediaSourceName(version)
	if name == "" {
		return ""
	}
	ext := strings.ToLower(strings.TrimSpace(version.Container))
	if ext == "" {
		return filepath.ToSlash(filepath.Join("/silo", name))
	}
	return filepath.ToSlash(filepath.Join("/silo", name+"."+ext))
}

func formatCompatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func syntheticPremiereDate(year int) string {
	if year <= 0 {
		return ""
	}
	return time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

func ensureRFC3339(value string) string {
	if strings.Contains(value, "T") {
		return value
	}
	t, err := time.Parse("2006-01-02", value)
	if err != nil {
		return value
	}
	return t.UTC().Format(time.RFC3339)
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
