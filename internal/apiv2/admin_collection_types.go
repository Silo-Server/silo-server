package apiv2

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	adminCollectionGroupField = "group_id"
	adminCollectionNull       = "null"
	adminCollectionSyncError  = "error"
	adminCollectionSyncFailed = "failed"
)

type AdminCollection struct {
	ID                ID              `json:"id"`
	LibraryID         ID              `json:"library_id"`
	LibraryIDs        []ID            `json:"library_ids" maxItems:"1000"`
	Slug              string          `json:"slug"`
	Title             string          `json:"title"`
	Description       string          `json:"description"`
	CollectionType    string          `json:"collection_type"`
	Visibility        string          `json:"visibility"`
	SortOrder         int             `json:"sort_order"`
	GroupID           *ID             `json:"group_id" nullable:"true"`
	Featured          bool            `json:"featured"`
	PosterURL         string          `json:"poster_url"`
	BackdropURL       string          `json:"backdrop_url"`
	PosterThumbhash   string          `json:"poster_thumbhash,omitempty"`
	BackdropThumbhash string          `json:"backdrop_thumbhash,omitempty"`
	SourceURL         string          `json:"source_url"`
	QueryDefinition   json.RawMessage `json:"query_definition"`
	SortConfig        json.RawMessage `json:"sort_config"`
	SourceConfig      json.RawMessage `json:"source_config"`
	ManagementMode    string          `json:"management_mode"`
	ManagementSource  string          `json:"management_source"`
	ManagementKey     string          `json:"management_key"`
	LastSyncStatus    string          `json:"last_sync_status"`
	LastSyncMessage   string          `json:"last_sync_message"`
	LastSyncAt        *Instant        `json:"last_sync_at,omitempty" nullable:"false"`
	SyncSchedule      string          `json:"sync_schedule,omitempty"`
	NextSyncAt        *Instant        `json:"next_sync_at,omitempty" nullable:"false"`
	ItemCount         int             `json:"item_count"`
	CreatedAt         Instant         `json:"created_at"`
	UpdatedAt         Instant         `json:"updated_at"`
}

type AdminCollectionCreate struct {
	LibraryID        ID              `json:"library_id,omitempty"`
	LibraryIDs       []ID            `json:"library_ids,omitempty" maxItems:"1000"`
	Slug             string          `json:"slug,omitempty"`
	Title            string          `json:"title" minLength:"1"`
	Description      string          `json:"description,omitempty"`
	CollectionType   string          `json:"collection_type,omitempty"`
	Visibility       string          `json:"visibility,omitempty"`
	SortOrder        int             `json:"sort_order,omitempty"`
	GroupID          *ID             `json:"group_id,omitempty" nullable:"true"`
	Featured         bool            `json:"featured,omitempty"`
	PosterURL        string          `json:"poster_url,omitempty"`
	BackdropURL      string          `json:"backdrop_url,omitempty"`
	SourceURL        string          `json:"source_url,omitempty"`
	QueryDefinition  json.RawMessage `json:"query_definition,omitempty"`
	SortConfig       json.RawMessage `json:"sort_config,omitempty"`
	SourceConfig     json.RawMessage `json:"source_config,omitempty"`
	ManagementMode   string          `json:"management_mode,omitempty"`
	ManagementSource string          `json:"management_source,omitempty"`
	ManagementKey    string          `json:"management_key,omitempty"`
	SyncSchedule     string          `json:"sync_schedule,omitempty"`
}

type AdminCollectionUpdate struct {
	LibraryIDs       *[]ID           `json:"library_ids,omitempty" nullable:"false" maxItems:"1000"`
	Slug             *string         `json:"slug,omitempty" nullable:"false"`
	Title            *string         `json:"title,omitempty" minLength:"1" nullable:"false"`
	Description      *string         `json:"description,omitempty" nullable:"false"`
	CollectionType   *string         `json:"collection_type,omitempty" nullable:"false"`
	Visibility       *string         `json:"visibility,omitempty" nullable:"false"`
	SortOrder        *int            `json:"sort_order,omitempty" nullable:"false"`
	GroupID          *ID             `json:"group_id,omitempty" nullable:"true"`
	Featured         *bool           `json:"featured,omitempty" nullable:"false"`
	PosterURL        *string         `json:"poster_url,omitempty" nullable:"false"`
	BackdropURL      *string         `json:"backdrop_url,omitempty" nullable:"false"`
	SourceURL        *string         `json:"source_url,omitempty" nullable:"false"`
	QueryDefinition  json.RawMessage `json:"query_definition,omitempty"`
	SortConfig       json.RawMessage `json:"sort_config,omitempty"`
	SourceConfig     json.RawMessage `json:"source_config,omitempty"`
	ManagementMode   *string         `json:"management_mode,omitempty" nullable:"false"`
	ManagementSource *string         `json:"management_source,omitempty" nullable:"false"`
	ManagementKey    *string         `json:"management_key,omitempty" nullable:"false"`
	SyncSchedule     *string         `json:"sync_schedule,omitempty" nullable:"false"`
}

type AdminMDBListImport struct {
	SortConfig       json.RawMessage `json:"sort_config,omitempty"`
	LibraryID        ID              `json:"library_id,omitempty"`
	LibraryIDs       []ID            `json:"library_ids,omitempty" maxItems:"1000"`
	Title            string          `json:"title" minLength:"1"`
	Description      string          `json:"description,omitempty"`
	URL              string          `json:"url"`
	Limit            *int            `json:"limit,omitempty" nullable:"false"`
	Featured         bool            `json:"featured,omitempty"`
	SortOrder        int             `json:"sort_order,omitempty"`
	PosterURL        string          `json:"poster_url,omitempty"`
	SyncSchedule     string          `json:"sync_schedule,omitempty"`
	ManagementMode   string          `json:"management_mode,omitempty"`
	ManagementSource string          `json:"management_source,omitempty"`
	ManagementKey    string          `json:"management_key,omitempty"`
}

type AdminTMDBImport struct {
	SortConfig       json.RawMessage `json:"sort_config,omitempty"`
	LibraryID        ID              `json:"library_id,omitempty"`
	LibraryIDs       []ID            `json:"library_ids,omitempty" maxItems:"1000"`
	Title            string          `json:"title" minLength:"1"`
	Description      string          `json:"description,omitempty"`
	Preset           string          `json:"preset,omitempty"`
	TimeWindow       string          `json:"time_window,omitempty"`
	MediaType        string          `json:"media_type,omitempty"`
	Limit            *int            `json:"limit,omitempty" nullable:"false"`
	Featured         bool            `json:"featured,omitempty"`
	SortOrder        int             `json:"sort_order,omitempty"`
	PosterURL        string          `json:"poster_url,omitempty"`
	SyncSchedule     string          `json:"sync_schedule,omitempty"`
	ManagementMode   string          `json:"management_mode,omitempty"`
	ManagementSource string          `json:"management_source,omitempty"`
	ManagementKey    string          `json:"management_key,omitempty"`
}

type AdminTraktImport struct {
	SortConfig       json.RawMessage `json:"sort_config,omitempty"`
	LibraryID        ID              `json:"library_id,omitempty"`
	LibraryIDs       []ID            `json:"library_ids,omitempty" maxItems:"1000"`
	Title            string          `json:"title" minLength:"1"`
	Description      string          `json:"description,omitempty"`
	Preset           string          `json:"preset,omitempty"`
	MediaType        string          `json:"media_type,omitempty"`
	ProfileID        ID              `json:"profile_id,omitempty"`
	ListURL          string          `json:"list_url,omitempty"`
	Limit            *int            `json:"limit,omitempty" nullable:"false"`
	Featured         bool            `json:"featured,omitempty"`
	PosterURL        string          `json:"poster_url,omitempty"`
	SyncSchedule     string          `json:"sync_schedule,omitempty"`
	ManagementMode   string          `json:"management_mode,omitempty"`
	ManagementSource string          `json:"management_source,omitempty"`
	ManagementKey    string          `json:"management_key,omitempty"`
}

type AdminTemplateApply struct {
	LibraryIDs     []ID                   `json:"library_ids" minItems:"1" maxItems:"1000"`
	DryRun         bool                   `json:"dry_run,omitempty"`
	DeleteExisting bool                   `json:"delete_existing,omitempty"`
	Featured       *AdminTemplateFeatured `json:"featured,omitempty" nullable:"false"`
}

type AdminTemplateFeatured struct {
	Home      *AdminTemplateFeaturedHome `json:"home,omitempty" nullable:"false"`
	Libraries map[ID]string              `json:"libraries,omitempty"`
}

type AdminTemplateFeaturedHome struct {
	LibraryID  ID     `json:"library_id"`
	TemplateID string `json:"template_id"`
}

type AdminTemplateEntry struct {
	TemplateID    string `json:"template_id"`
	TemplateTitle string `json:"template_title"`
	LibraryID     ID     `json:"library_id"`
	LibraryName   string `json:"library_name"`
	CollectionID  ID     `json:"collection_id,omitempty"`
}

type AdminTemplateCollectionEntry struct {
	LibraryID       ID     `json:"library_id"`
	LibraryName     string `json:"library_name"`
	CollectionID    ID     `json:"collection_id"`
	CollectionTitle string `json:"collection_title"`
}

type AdminTemplateFeaturedEntry struct {
	Surface       string `json:"surface"`
	LibraryID     ID     `json:"library_id,omitempty"`
	LibraryName   string `json:"library_name,omitempty"`
	TemplateID    string `json:"template_id"`
	TemplateTitle string `json:"template_title"`
	CollectionID  ID     `json:"collection_id,omitempty"`
	SectionID     ID     `json:"section_id,omitempty"`
}

type AdminTemplateResult struct {
	BundleID       string                         `json:"bundle_id"`
	DryRun         bool                           `json:"dry_run"`
	DeleteExisting bool                           `json:"delete_existing"`
	Deleted        []AdminTemplateCollectionEntry `json:"deleted"`
	DeleteSkipped  []AdminTemplateCollectionEntry `json:"delete_skipped"`
	DeleteFailed   []AdminTemplateCollectionEntry `json:"delete_failed"`
	Created        []AdminTemplateEntry           `json:"created"`
	Skipped        []AdminTemplateEntry           `json:"skipped"`
	Failed         []AdminTemplateEntry           `json:"failed"`
	SyncQueued     []AdminTemplateEntry           `json:"sync_queued"`
	Featured       []AdminTemplateFeaturedEntry   `json:"featured"`
	FeaturedFailed []AdminTemplateFeaturedEntry   `json:"featured_failed"`
}

func (v AdminCollectionCreate) command() (handlers.AdminCollectionCreate, *Problem) {
	var c handlers.AdminCollectionCreate
	if v.LibraryID != "" {
		ids, p := intsOfIDs([]ID{v.LibraryID}, "library_id")
		if p != nil {
			return c, p
		}
		c.LibraryID = ids[0]
	}
	ids, p := intsOfIDs(v.LibraryIDs, "library_ids")
	if p != nil {
		return c, p
	}
	c.LibraryIDs, c.Slug, c.Title, c.Description = ids, v.Slug, v.Title, v.Description
	c.CollectionType, c.Visibility, c.SortOrder = v.CollectionType, v.Visibility, v.SortOrder
	c.Featured, c.PosterURL, c.BackdropURL, c.SourceURL = v.Featured, v.PosterURL, v.BackdropURL, v.SourceURL
	c.QueryDefinition, c.SortConfig, c.SourceConfig = v.QueryDefinition, v.SortConfig, v.SourceConfig
	c.ManagementMode, c.ManagementSource, c.ManagementKey, c.SyncSchedule = v.ManagementMode, v.ManagementSource, v.ManagementKey, v.SyncSchedule
	if v.GroupID != nil {
		value := string(*v.GroupID)
		c.GroupID = &value
	}
	return c, nil
}
func (v AdminCollectionUpdate) command(raw []byte) (handlers.AdminCollectionUpdate, *Problem) {
	var c handlers.AdminCollectionUpdate
	if p := rejectNonNullableNulls(raw, map[string]bool{adminCollectionGroupField: true}); p != nil {
		return c, p
	}
	if v.LibraryIDs != nil {
		ids, p := intsOfIDs(*v.LibraryIDs, "library_ids")
		if p != nil {
			return c, p
		}
		c.LibraryIDs = &ids
	}
	c.Slug, c.Title, c.Description, c.CollectionType = v.Slug, v.Title, v.Description, v.CollectionType
	c.Visibility, c.SortOrder, c.Featured = v.Visibility, v.SortOrder, v.Featured
	c.PosterURL, c.BackdropURL, c.SourceURL = v.PosterURL, v.BackdropURL, v.SourceURL
	c.QueryDefinition, c.SortConfig, c.SourceConfig = v.QueryDefinition, v.SortConfig, v.SourceConfig
	c.ManagementMode, c.ManagementSource, c.ManagementKey, c.SyncSchedule = v.ManagementMode, v.ManagementSource, v.ManagementKey, v.SyncSchedule
	if v.GroupID != nil {
		value := string(*v.GroupID)
		c.GroupID.SetValue(&value, true)
	}
	var original map[string]json.RawMessage
	if err := json.Unmarshal(raw, &original); err != nil {
		return c, NewProblem(TypeValidationFailed, "Invalid collection definition.")
	}
	if value, ok := original[adminCollectionGroupField]; ok && strings.TrimSpace(string(value)) == adminCollectionNull {
		c.GroupID.SetValue(nil, true)
	}
	return c, nil
}
func (v AdminMDBListImport) command() (handlers.AdminCollectionImportMDBList, *Problem) {
	var c handlers.AdminCollectionImportMDBList
	ids, p := intsOfIDs(v.LibraryIDs, "library_ids")
	if p != nil {
		return c, p
	}
	var libraryID int
	if v.LibraryID != "" {
		lowered, p := intsOfIDs([]ID{v.LibraryID}, "library_id")
		if p != nil {
			return c, p
		}
		libraryID = lowered[0]
	}
	c.LibraryID, c.LibraryIDs, c.Title, c.Description, c.URL = libraryID, ids, v.Title, v.Description, v.URL
	c.SortConfig, c.Limit, c.Featured, c.SortOrder, c.PosterURL, c.SyncSchedule = v.SortConfig, v.Limit, v.Featured, v.SortOrder, v.PosterURL, v.SyncSchedule
	c.ManagementMode, c.ManagementSource, c.ManagementKey = v.ManagementMode, v.ManagementSource, v.ManagementKey
	return c, nil
}
func (v AdminTMDBImport) command() (handlers.AdminCollectionImportTMDB, *Problem) {
	var c handlers.AdminCollectionImportTMDB
	ids, p := intsOfIDs(v.LibraryIDs, "library_ids")
	if p != nil {
		return c, p
	}
	var libraryID int
	if v.LibraryID != "" {
		lowered, p := intsOfIDs([]ID{v.LibraryID}, "library_id")
		if p != nil {
			return c, p
		}
		libraryID = lowered[0]
	}
	c.LibraryID, c.LibraryIDs, c.Title, c.Description = libraryID, ids, v.Title, v.Description
	c.Preset, c.TimeWindow, c.MediaType, c.Limit = v.Preset, v.TimeWindow, v.MediaType, v.Limit
	c.SortConfig, c.Featured, c.SortOrder, c.PosterURL, c.SyncSchedule = v.SortConfig, v.Featured, v.SortOrder, v.PosterURL, v.SyncSchedule
	c.ManagementMode, c.ManagementSource, c.ManagementKey = v.ManagementMode, v.ManagementSource, v.ManagementKey
	return c, nil
}
func (v AdminTraktImport) command() (handlers.AdminCollectionImportTrakt, *Problem) {
	var c handlers.AdminCollectionImportTrakt
	ids, p := intsOfIDs(v.LibraryIDs, "library_ids")
	if p != nil {
		return c, p
	}
	var libraryID int
	if v.LibraryID != "" {
		lowered, p := intsOfIDs([]ID{v.LibraryID}, "library_id")
		if p != nil {
			return c, p
		}
		libraryID = lowered[0]
	}
	c.LibraryID, c.LibraryIDs, c.Title, c.Description = libraryID, ids, v.Title, v.Description
	c.Preset, c.MediaType, c.ProfileID, c.ListURL, c.Limit = v.Preset, v.MediaType, string(v.ProfileID), v.ListURL, v.Limit
	c.SortConfig, c.Featured, c.PosterURL, c.SyncSchedule = v.SortConfig, v.Featured, v.PosterURL, v.SyncSchedule
	c.ManagementMode, c.ManagementSource, c.ManagementKey = v.ManagementMode, v.ManagementSource, v.ManagementKey
	return c, nil
}
func (v AdminTemplateApply) command() (handlers.AdminCollectionTemplateApply, *Problem) {
	var c handlers.AdminCollectionTemplateApply
	ids, p := intsOfIDs(v.LibraryIDs, "library_ids")
	if p != nil {
		return c, p
	}
	if len(ids) == 0 {
		return c, NewProblem(TypeValidationFailed, "At least one library is required.")
	}
	// Featured library map keys remain strings on the wire; the service uses integer keys.
	type featuredHome struct {
		LibraryID  int    `json:"library_id"`
		TemplateID string `json:"template_id"`
	}
	type featured struct {
		Home      *featuredHome  `json:"home,omitempty" nullable:"false"`
		Libraries map[int]string `json:"libraries,omitempty"`
	}
	var lowered *featured
	if v.Featured != nil {
		lowered = &featured{Libraries: map[int]string{}}
		if v.Featured.Home != nil {
			values, p := intsOfIDs([]ID{v.Featured.Home.LibraryID}, "featured.home.library_id")
			if p != nil {
				return c, p
			}
			lowered.Home = &featuredHome{LibraryID: values[0], TemplateID: v.Featured.Home.TemplateID}
		}
		for id, template := range v.Featured.Libraries {
			values, p := intsOfIDs([]ID{id}, "featured.libraries")
			if p != nil {
				return c, p
			}
			lowered.Libraries[values[0]] = template
		}
	}
	data, err := json.Marshal(struct {
		LibraryIDs     []int     `json:"library_ids" maxItems:"1000"`
		DryRun         bool      `json:"dry_run"`
		DeleteExisting bool      `json:"delete_existing"`
		Featured       *featured `json:"featured,omitempty" nullable:"false"`
	}{ids, v.DryRun, v.DeleteExisting, lowered})
	if err != nil {
		return c, NewProblem(TypeValidationFailed, "Invalid template application.")
	}
	if err = json.Unmarshal(data, &c); err != nil {
		return c, NewProblem(TypeValidationFailed, "Invalid template application.")
	}
	return c, nil
}

type AdminCollectionSyncRun struct {
	ID             ID       `json:"id"`
	CollectionID   ID       `json:"collection_id"`
	Status         string   `json:"status"`
	Message        string   `json:"message"`
	ItemsAdded     int      `json:"items_added"`
	ItemsRemoved   int      `json:"items_removed"`
	ItemsMatched   int      `json:"items_matched"`
	ItemsUnmatched int      `json:"items_unmatched"`
	StartedAt      *Instant `json:"started_at,omitempty" nullable:"false"`
	CompletedAt    *Instant `json:"completed_at,omitempty" nullable:"false"`
	CreatedAt      Instant  `json:"created_at"`
}
type AdminCollectionImportResult struct {
	Collection AdminCollection         `json:"collection"`
	SyncRun    *AdminCollectionSyncRun `json:"sync_run,omitempty" nullable:"false"`
}

func adminSyncInstant(t *time.Time) *Instant {
	if t == nil || t.IsZero() {
		return nil
	}
	return new(NewInstant(*t))
}
func adminCollectionSyncRunOf(v *models.LibraryCollectionSyncRun) *AdminCollectionSyncRun {
	if v == nil {
		return nil
	}
	message := ""
	if v.Status == adminCollectionSyncFailed || v.Status == adminCollectionSyncError {
		message = "Collection sync failed."
	}
	return &AdminCollectionSyncRun{ID: ID(v.ID), CollectionID: ID(v.CollectionID), Status: v.Status, Message: message, ItemsAdded: v.ItemsAdded, ItemsRemoved: v.ItemsRemoved, ItemsMatched: v.ItemsMatched, ItemsUnmatched: v.ItemsUnmatched, StartedAt: adminSyncInstant(v.StartedAt), CompletedAt: adminSyncInstant(v.CompletedAt), CreatedAt: NewInstant(v.CreatedAt)}
}
func adminCollectionImportResultOf(v handlers.AdminCollectionImportResult) AdminCollectionImportResult {
	return AdminCollectionImportResult{Collection: adminCollectionOf(v.Collection), SyncRun: adminCollectionSyncRunOf(v.SyncRun)}
}

func adminCollectionOf(v handlers.AdminCollection) AdminCollection {
	out := AdminCollection{
		ID:                ID(v.ID),
		LibraryID:         IDFromInt(int64(v.LibraryID)),
		LibraryIDs:        idsOfInts(v.LibraryIDs),
		Slug:              v.Slug,
		Title:             v.Title,
		Description:       v.Description,
		CollectionType:    v.CollectionType,
		Visibility:        v.Visibility,
		SortOrder:         v.SortOrder,
		Featured:          v.Featured,
		PosterURL:         v.PosterURL,
		BackdropURL:       v.BackdropURL,
		PosterThumbhash:   v.PosterThumbhash,
		BackdropThumbhash: v.BackdropThumbhash,
		SourceURL:         v.SourceURL,
		QueryDefinition:   v.QueryDefinition,
		SortConfig:        v.SortConfig,
		SourceConfig:      v.SourceConfig,
		ManagementMode:    v.ManagementMode,
		ManagementSource:  v.ManagementSource,
		ManagementKey:     v.ManagementKey,
		LastSyncStatus:    v.LastSyncStatus,
		LastSyncMessage:   "",
		LastSyncAt:        instantOfStamp(v.LastSyncAt),
		SyncSchedule:      v.SyncSchedule,
		NextSyncAt:        instantOfStamp(v.NextSyncAt),
		ItemCount:         v.ItemCount,
	}
	if v.GroupID != nil {
		out.GroupID = new(ID(*v.GroupID))
	}
	if t := instantOfStamp(v.CreatedAt); t != nil {
		out.CreatedAt = *t
	}
	if t := instantOfStamp(v.UpdatedAt); t != nil {
		out.UpdatedAt = *t
	}
	if v.LastSyncStatus == adminCollectionSyncFailed || v.LastSyncStatus == adminCollectionSyncError {
		out.LastSyncMessage = "Collection sync failed."
	}
	return out
}

func adminTemplateResultOf(v handlers.AdminCollectionTemplateResult) AdminTemplateResult {
	out := AdminTemplateResult{BundleID: v.BundleID, DryRun: v.DryRun, DeleteExisting: v.DeleteExisting}
	out.Deleted = make([]AdminTemplateCollectionEntry, 0, len(v.Deleted))
	for _, e := range v.Deleted {
		out.Deleted = append(out.Deleted, AdminTemplateCollectionEntry{LibraryID: IDFromInt(int64(e.LibraryID)), LibraryName: e.LibraryName, CollectionID: ID(e.CollectionID), CollectionTitle: e.CollectionTitle})
	}
	out.DeleteSkipped = make([]AdminTemplateCollectionEntry, 0, len(v.DeleteSkipped))
	for _, e := range v.DeleteSkipped {
		out.DeleteSkipped = append(out.DeleteSkipped, AdminTemplateCollectionEntry{LibraryID: IDFromInt(int64(e.LibraryID)), LibraryName: e.LibraryName, CollectionID: ID(e.CollectionID), CollectionTitle: e.CollectionTitle})
	}
	out.DeleteFailed = make([]AdminTemplateCollectionEntry, 0, len(v.DeleteFailed))
	for _, e := range v.DeleteFailed {
		out.DeleteFailed = append(out.DeleteFailed, AdminTemplateCollectionEntry{LibraryID: IDFromInt(int64(e.LibraryID)), LibraryName: e.LibraryName, CollectionID: ID(e.CollectionID), CollectionTitle: e.CollectionTitle})
	}
	out.Created = make([]AdminTemplateEntry, 0, len(v.Created))
	for _, e := range v.Created {
		out.Created = append(out.Created, AdminTemplateEntry{TemplateID: e.TemplateID, TemplateTitle: e.TemplateTitle, LibraryID: IDFromInt(int64(e.LibraryID)), LibraryName: e.LibraryName, CollectionID: ID(e.CollectionID)})
	}
	out.Skipped = make([]AdminTemplateEntry, 0, len(v.Skipped))
	for _, e := range v.Skipped {
		out.Skipped = append(out.Skipped, AdminTemplateEntry{TemplateID: e.TemplateID, TemplateTitle: e.TemplateTitle, LibraryID: IDFromInt(int64(e.LibraryID)), LibraryName: e.LibraryName, CollectionID: ID(e.CollectionID)})
	}
	out.Failed = make([]AdminTemplateEntry, 0, len(v.Failed))
	for _, e := range v.Failed {
		out.Failed = append(out.Failed, AdminTemplateEntry{TemplateID: e.TemplateID, TemplateTitle: e.TemplateTitle, LibraryID: IDFromInt(int64(e.LibraryID)), LibraryName: e.LibraryName, CollectionID: ID(e.CollectionID)})
	}
	out.SyncQueued = make([]AdminTemplateEntry, 0, len(v.SyncQueued))
	for _, e := range v.SyncQueued {
		out.SyncQueued = append(out.SyncQueued, AdminTemplateEntry{TemplateID: e.TemplateID, TemplateTitle: e.TemplateTitle, LibraryID: IDFromInt(int64(e.LibraryID)), LibraryName: e.LibraryName, CollectionID: ID(e.CollectionID)})
	}
	out.Featured = make([]AdminTemplateFeaturedEntry, 0, len(v.Featured))
	for _, e := range v.Featured {
		out.Featured = append(out.Featured, AdminTemplateFeaturedEntry{Surface: e.Surface, LibraryID: adminOptionalLibraryID(e.LibraryID), LibraryName: e.LibraryName, TemplateID: e.TemplateID, TemplateTitle: e.TemplateTitle, CollectionID: ID(e.CollectionID), SectionID: ID(e.SectionID)})
	}
	out.FeaturedFailed = make([]AdminTemplateFeaturedEntry, 0, len(v.FeaturedFailed))
	for _, e := range v.FeaturedFailed {
		out.FeaturedFailed = append(out.FeaturedFailed, AdminTemplateFeaturedEntry{Surface: e.Surface, LibraryID: adminOptionalLibraryID(e.LibraryID), LibraryName: e.LibraryName, TemplateID: e.TemplateID, TemplateTitle: e.TemplateTitle, CollectionID: ID(e.CollectionID), SectionID: ID(e.SectionID)})
	}
	return out
}

func adminOptionalLibraryID(id int) ID {
	if id <= 0 {
		return ""
	}
	return IDFromInt(int64(id))
}
