package apiv2

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
)

// AdminCatalogItemsService is the slice of *catalog.ItemRepository
// listAdminCatalogItems uses.
type AdminCatalogItemsService interface {
	ListAdminCatalogItems(context.Context, catalogpkg.AdminCatalogItemFilter, string, int) (catalogpkg.AdminCatalogItemPage, error)
}

// adminCatalogItemsMaxContentIDs bounds the content_ids filter: one full page.
const adminCatalogItemsMaxContentIDs = 200

// AdminCatalogItemsInput is the listAdminCatalogItems query.
type AdminCatalogItemsInput struct {
	LimitParam
	Cursor     string   `query:"cursor" maxLength:"8192" doc:"Opaque cursor from page.next_cursor; valid only with the filters that produced it"`
	LibraryID  ID       `query:"library_id" doc:"Only items present in this library; added_at and added_after then use the item's first-seen time in this library" example:"1"`
	Type       string   `query:"type" enum:"movie,series,episode" doc:"Only items of this type"`
	AddedAfter string   `query:"added_after" format:"date-time" doc:"Only items whose added_at is at or after this instant"`
	ContentIDs []string `query:"content_ids,explode" maxItems:"200" doc:"Only these items, one content_ids parameter per id; ids that are not present movies, series or episodes are left out" example:"[\"movie-tmdb-949\"]"`
}

// AdminCatalogItem is one movie, series or episode present in an enabled
// library, with the files that make it playable.
type AdminCatalogItem struct {
	ContentID     string                    `json:"content_id" example:"movie-tmdb-949"`
	Type          string                    `json:"type" enum:"movie,series,episode"`
	Title         string                    `json:"title" doc:"Episode title for an episode; empty when the episode has none"`
	Year          int                       `json:"year,omitempty" doc:"Release year; an episode's air-date year. Absent when unknown"`
	Status        string                    `json:"status,omitempty" doc:"Metadata match state of a movie or series; absent for an episode" example:"matched"`
	ImdbID        string                    `json:"imdb_id,omitempty"`
	TmdbID        string                    `json:"tmdb_id,omitempty"`
	TvdbID        string                    `json:"tvdb_id,omitempty"`
	SeriesID      string                    `json:"series_id,omitempty" doc:"An episode's series content_id; absent otherwise"`
	SeriesTitle   string                    `json:"series_title,omitempty" doc:"An episode's series title; absent otherwise"`
	SeasonNumber  *int                      `json:"season_number,omitempty" doc:"An episode's season; absent otherwise"`
	EpisodeNumber *int                      `json:"episode_number,omitempty" doc:"An episode's number in its season; absent otherwise"`
	LibraryIDs    []ID                      `json:"library_ids" doc:"Every enabled library the item is present in, whatever library_id filters"`
	AddedAt       Instant                   `json:"added_at" doc:"When Silo first saw the item: the earliest first-seen time across its libraries, or its first-seen time in library_id when that filter is set. Not a change cursor; metadata and file changes do not move it"`
	Versions      []AdminCatalogItemVersion `json:"versions" doc:"Each present file in an enabled library, ordered by file_id. Empty for a series, never null"`
}

// AdminCatalogItemVersion is one present file. Video facts come from the
// file's primary video track; audio facts from the audio track playback picks
// when the viewer has no language preference.
type AdminCatalogItemVersion struct {
	FileID        ID     `json:"file_id" example:"120"`
	LibraryID     ID     `json:"library_id" example:"1"`
	Resolution    string `json:"resolution,omitempty" example:"2160p"`
	CodecVideo    string `json:"codec_video,omitempty" example:"hevc"`
	DynamicRange  string `json:"dynamic_range,omitempty" enum:"sdr,hdr10,hdr10_plus,hlg,dolby_vision,hdr_unknown" doc:"Absent when the file was not probed"`
	CodecAudio    string `json:"codec_audio,omitempty" example:"truehd"`
	AudioChannels int    `json:"audio_channels,omitempty" example:"8"`
	Atmos         bool   `json:"atmos" doc:"The selected audio track carries Dolby Atmos"`
	Container     string `json:"container,omitempty" example:"mkv"`
	BitrateKbps   int    `json:"bitrate_kbps,omitempty" doc:"Overall file bitrate in kilobits per second"`
	FileSize      int64  `json:"file_size" doc:"Bytes"`
	Edition       string `json:"edition,omitempty" doc:"Edition label, such as Director's Cut" example:"Extended"`
}

// AdminCatalogItemCollection is the named envelope the contract carries.
type AdminCatalogItemCollection struct {
	Collection[AdminCatalogItem]
}
type AdminCatalogItemCollectionOutput struct {
	Body AdminCatalogItemCollection
}

// AdminCatalogCapabilities describes the administrator catalog reads this
// server offers.
type AdminCatalogCapabilities struct {
	Capability
	ItemListing           bool `json:"item_listing" doc:"listAdminCatalogItems is served"`
	ItemListingContentIDs int  `json:"item_listing_max_content_ids" doc:"Most content_ids one listAdminCatalogItems request accepts"`
	ItemChangeEvents      bool `json:"item_change_events" doc:"catalog.item.changed on the v2 events socket also carries change added, removed and files_changed to administrators"`
}
type AdminCatalogCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminCatalogCapabilities
}

const opListAdminCatalogItems = "listAdminCatalogItems"

func registerAdminCatalogItems(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/catalog/capabilities", "getAdminCatalogCapabilities", "admin-catalog", "Discover the administrator catalog reads this server offers."), Class: ClassActingAdmin}, func(context.Context, *CapabilityInput) (*AdminCatalogCapabilitiesOutput, error) {
		served := reg.deps.AdminCatalogItems != nil
		return &AdminCatalogCapabilitiesOutput{Body: AdminCatalogCapabilities{
			Capability:            Capability{State: configuredCapabilityState(served)},
			ItemListing:           served,
			ItemListingContentIDs: adminCatalogItemsMaxContentIDs,
			ItemChangeEvents:      served,
		}}, nil
	})

	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/catalog/items", opListAdminCatalogItems, "admin-catalog", "List the movies, series and episodes present in enabled libraries, with their files, in content_id order. Each page is one consistent read; later pages read the live catalog."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminCatalogItemsInput) (*AdminCatalogItemCollectionOutput, error) {
		if reg.deps.AdminCatalogItems == nil {
			return nil, unavailable("catalog listing")
		}
		filter := catalogpkg.AdminCatalogItemFilter{Type: in.Type}
		if in.LibraryID != "" {
			n, err := intOfID(in.LibraryID)
			if err != nil || n <= 0 || n > math.MaxInt32 {
				return nil, NewProblem(TypeValidationFailed, "Invalid library_id.")
			}
			filter.LibraryID = n
		}
		if value := strings.TrimSpace(in.AddedAfter); value != "" {
			at, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return nil, NewProblem(TypeValidationFailed, "Invalid added_after.")
			}
			filter.AddedAfter = new(at.UTC())
		}
		// content_ids is a set: sorted and deduplicated, so its cursor survives reordering.
		for _, id := range in.ContentIDs {
			if id = strings.TrimSpace(id); id != "" {
				filter.ContentIDs = append(filter.ContentIDs, id)
			}
		}
		if len(in.ContentIDs) > 0 && len(filter.ContentIDs) == 0 {
			return nil, NewProblem(TypeValidationFailed, "Invalid content_ids.")
		}
		slices.Sort(filter.ContentIDs)
		filter.ContentIDs = slices.Compact(filter.ContentIDs)
		filterJSON, _ := json.Marshal(struct {
			Filter catalogpkg.AdminCatalogItemFilter
			Limit  int
		}{filter, in.Limit})
		scope := adminPolicyListScope(ctx, opListAdminCatalogItems, string(filterJSON), "content_id", "content_id")
		after := ""
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
			if after == "" {
				return nil, NewProblem(TypeInvalidCursor, "Invalid catalog item cursor.")
			}
		}
		page, err := reg.deps.AdminCatalogItems.ListAdminCatalogItems(ctx, filter, after, in.Limit)
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Unable to list catalog items.")
		}
		items := make([]AdminCatalogItem, 0, len(page.Items))
		for _, row := range page.Items {
			item := AdminCatalogItem{
				ContentID: row.ContentID, Type: row.Type, Title: row.Title, Year: row.Year, Status: row.Status,
				ImdbID: row.ImdbID, TmdbID: row.TmdbID, TvdbID: row.TvdbID,
				SeriesID: row.SeriesID, SeriesTitle: row.SeriesTitle, SeasonNumber: row.SeasonNumber, EpisodeNumber: row.EpisodeNumber,
				LibraryIDs: make([]ID, 0, len(row.LibraryIDs)), AddedAt: NewInstant(row.AddedAt),
				Versions: make([]AdminCatalogItemVersion, 0, len(row.Versions)),
			}
			for _, id := range row.LibraryIDs {
				item.LibraryIDs = append(item.LibraryIDs, IDFromInt(int64(id)))
			}
			for _, v := range row.Versions {
				item.Versions = append(item.Versions, AdminCatalogItemVersion{
					FileID: IDFromInt(int64(v.FileID)), LibraryID: IDFromInt(int64(v.LibraryID)),
					Resolution: v.Resolution, CodecVideo: v.CodecVideo, DynamicRange: v.DynamicRange,
					CodecAudio: v.CodecAudio, AudioChannels: v.AudioChannels, Atmos: v.Atmos, Container: v.Container,
					BitrateKbps: v.BitrateKbps, FileSize: v.FileSize, Edition: v.Edition,
				})
			}
			items = append(items, item)
		}
		next := ""
		if page.HasMore {
			if len(page.Items) == 0 {
				return nil, NewProblem(TypeInternalError, "Unable to page catalog items.")
			}
			next, err = cursors.Encode(scope, page.Items[len(page.Items)-1].ContentID)
			if err != nil {
				return nil, NewProblem(TypeInternalError, "Unable to encode catalog item cursor.")
			}
		}
		return &AdminCatalogItemCollectionOutput{Body: AdminCatalogItemCollection{Collection: Paginated(items, next)}}, nil
	})
}
