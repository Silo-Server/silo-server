package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type AdminSubtitleListService interface {
	ListAdminSubtitlesPage(context.Context, handlers.AdminSubtitleListFilter, *handlers.AdminSubtitlePageKey, int) (handlers.AdminSubtitlePage, error)
}

type AdminSubtitleListInput struct {
	LimitParam
	Cursor      string `query:"cursor" maxLength:"8192"`
	Provider    string `query:"provider" maxLength:"128"`
	Language    string `query:"language" maxLength:"128"`
	UserID      ID     `query:"user_id"`
	MediaFileID ID     `query:"media_file_id"`
	Search      string `query:"q" maxLength:"1024" doc:"Case-insensitive release-name pattern; percent and underscore retain SQL wildcard semantics."`
}

type AdminStoredSubtitle struct {
	ID               ID      `json:"id"`
	MediaFileID      ID      `json:"media_file_id"`
	MediaContentID   string  `json:"media_content_id,omitempty"`
	Provider         string  `json:"provider"`
	Language         string  `json:"language"`
	Format           string  `json:"format"`
	ReleaseName      string  `json:"release_name"`
	Score            float64 `json:"score"`
	HearingImpaired  bool    `json:"hearing_impaired"`
	CreatedAt        Instant `json:"created_at"`
	DownloadedBy     *ID     `json:"downloaded_by,omitempty"`
	UploaderUsername string  `json:"uploader_username"`
	MediaTitle       string  `json:"media_title"`
	MediaType        string  `json:"media_type"`
	FilePath         string  `json:"file_path" doc:"Administrator-only source file path."`
}

type AdminStoredSubtitleCollection struct {
	Collection[AdminStoredSubtitle]
	Total             int `json:"total" minimum:"0"`
	Uploads           int `json:"uploads" minimum:"0"`
	ProviderDownloads int `json:"provider_downloads" minimum:"0"`
}
type AdminStoredSubtitleCollectionOutput struct{ Body AdminStoredSubtitleCollection }

const opListAdminStoredSubtitles = "listAdminStoredSubtitles"

const adminSubtitleListSort = "created_at:desc"
const adminSubtitleListTiebreaker = "id:desc"

func registerAdminSubtitleList(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/subtitles", opListAdminStoredSubtitles, "admin", "List stored subtitles with filtered counts, newest first. Each page is consistent; later pages read the live collection."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *AdminSubtitleListInput) (*AdminStoredSubtitleCollectionOutput, error) {
		if reg.deps.AdminSubtitleList == nil {
			return nil, NewProblem(TypeDependencyUnavailable, "Subtitle administration is unavailable.")
		}
		filter := handlers.AdminSubtitleListFilter{Provider: strings.TrimSpace(in.Provider), Language: strings.TrimSpace(in.Language), Search: strings.TrimSpace(in.Search)}
		for _, field := range []struct {
			raw  ID
			dest *int
		}{{in.UserID, &filter.UserID}, {in.MediaFileID, &filter.MediaFileID}} {
			raw, dest := field.raw, field.dest
			if raw == "" {
				continue
			}
			n, err := intOfID(raw)
			if err != nil || n <= 0 {
				return nil, NewProblem(TypeValidationFailed, "Invalid subtitle list filter ID.")
			}
			*dest = n
		}
		filterJSON, _ := json.Marshal(struct {
			Filter handlers.AdminSubtitleListFilter
			Limit  int
		}{filter, in.Limit})
		scope := CursorScope{OperationID: opListAdminStoredSubtitles, Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: string(filterJSON), Sort: adminSubtitleListSort, Tiebreaker: adminSubtitleListTiebreaker}
		var after *handlers.AdminSubtitlePageKey
		if in.Cursor != "" {
			after = new(handlers.AdminSubtitlePageKey)
			if p := cursors.Decode(scope, in.Cursor, after); p != nil {
				return nil, p
			}
			if after.ID <= 0 || after.CreatedAt.IsZero() {
				return nil, NewProblem(TypeInvalidCursor, "Invalid subtitle cursor.")
			}
		}
		result, err := reg.deps.AdminSubtitleList.ListAdminSubtitlesPage(ctx, filter, after, in.Limit)
		if err != nil {
			return nil, NewProblem(TypeInternalError, "Unable to list stored subtitles.")
		}
		items := make([]AdminStoredSubtitle, 0, len(result.Items))
		for _, row := range result.Items {
			language, ok := subtitles.CanonicalProviderLanguage(row.Provider, row.Language)
			if !ok {
				return nil, NewProblem(TypeInternalError, "Stored subtitle has an invalid language value.")
			}
			item := AdminStoredSubtitle{ID: IDFromInt(int64(row.ID)), MediaFileID: IDFromInt(int64(row.MediaFileID)), MediaContentID: row.MediaContentID, Provider: row.Provider, Language: language, Format: row.Format, ReleaseName: row.ReleaseName, Score: row.Score, HearingImpaired: row.HearingImpaired, CreatedAt: NewInstant(row.CreatedAt), UploaderUsername: row.UploaderUsername, MediaTitle: row.MediaTitle, MediaType: row.MediaType, FilePath: row.FilePath}
			if row.DownloadedBy != nil {
				item.DownloadedBy = new(IDFromInt(int64(*row.DownloadedBy)))
			}
			items = append(items, item)
		}
		next := ""
		if result.HasMore {
			if len(result.Items) == 0 {
				return nil, NewProblem(TypeInternalError, "Unable to page stored subtitles.")
			}
			last := result.Items[len(result.Items)-1]
			next, err = cursors.Encode(scope, handlers.AdminSubtitlePageKey{ID: last.ID, CreatedAt: last.CreatedAt})
			if err != nil {
				return nil, NewProblem(TypeInternalError, "Unable to encode subtitle cursor.")
			}
		}
		return &AdminStoredSubtitleCollectionOutput{Body: AdminStoredSubtitleCollection{Collection: Paginated(items, next), Total: result.Total, Uploads: result.Uploads, ProviderDownloads: result.ProviderDownloads}}, nil
	})
}
