// Package runtimehost provides a typed client for the RuntimeHost service
// exposed by the silo host. Plugins obtain a *Client via the runtime
// package's Host() accessor.
package runtimehost

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type Client struct {
	rpc pluginv1.RuntimeHostClient
}

type CallPluginHTTPRequest struct {
	InstallationID int
	Method         string
	Path           string
	Headers        map[string]string
	Body           []byte
	Query          map[string]any
}

type CallPluginHTTPResponse struct {
	StatusCode int
	Headers    map[string]string
	Body       []byte
}

func (c *Client) CallPluginHTTP(ctx context.Context, req CallPluginHTTPRequest) (*CallPluginHTTPResponse, error) {
	if req.InstallationID <= 0 {
		return nil, fmt.Errorf("runtimehost: installation id is required")
	}
	if req.Path == "" {
		return nil, fmt.Errorf("runtimehost: path is required")
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	var query *structpb.Struct
	if len(req.Query) > 0 {
		var err error
		query, err = structpb.NewStruct(req.Query)
		if err != nil {
			return nil, fmt.Errorf("runtimehost: encode query: %w", err)
		}
	}
	resp, err := c.rpc.CallPluginHTTP(ctx, &pluginv1.CallPluginHTTPRequest{
		InstallationId: int32(req.InstallationID),
		Method:         method,
		Path:           req.Path,
		Headers:        req.Headers,
		Body:           req.Body,
		Query:          query,
	})
	if err != nil {
		return nil, err
	}
	return &CallPluginHTTPResponse{
		StatusCode: int(resp.GetStatusCode()),
		Headers:    resp.GetHeaders(),
		Body:       resp.GetBody(),
	}, nil
}

type ScopedStreamRequest struct {
	MediaFileID         int
	PlayMethod          string
	ExpiresAt           time.Time
	MaxWatchMinutes     int
	MaxResolutionHeight int
	AllowDirectPlay     bool
	AllowDownloads      bool
	DisableSeeking      bool
	AuditSubject        string
	WatermarkMode       string
	WatermarkText       string
	WatermarkLogoURL    string
}

type ScopedStreamGrant struct {
	StreamURL  string
	PlayMethod string
	ExpiresAt  time.Time
}

func (c *Client) MintScopedStream(ctx context.Context, req ScopedStreamRequest) (*ScopedStreamGrant, error) {
	if req.MediaFileID <= 0 {
		return nil, fmt.Errorf("runtimehost: media file id is required")
	}
	resp, err := c.rpc.MintScopedStream(ctx, &pluginv1.MintScopedStreamRequest{
		MediaFileId:         int64(req.MediaFileID),
		PlayMethod:          req.PlayMethod,
		ExpiresAtUnix:       req.ExpiresAt.Unix(),
		MaxWatchMinutes:     int32(req.MaxWatchMinutes),
		MaxResolutionHeight: int32(req.MaxResolutionHeight),
		AllowDirectPlay:     req.AllowDirectPlay,
		AllowDownloads:      req.AllowDownloads,
		DisableSeeking:      req.DisableSeeking,
		AuditSubject:        req.AuditSubject,
		WatermarkMode:       req.WatermarkMode,
		WatermarkText:       req.WatermarkText,
		WatermarkLogoUrl:    req.WatermarkLogoURL,
	})
	if err != nil {
		return nil, err
	}
	return &ScopedStreamGrant{
		StreamURL:  resp.GetStreamUrl(),
		PlayMethod: resp.GetPlayMethod(),
		ExpiresAt:  time.Unix(resp.GetExpiresAtUnix(), 0),
	}, nil
}

func NewClient(conn *grpc.ClientConn) *Client {
	return &Client{rpc: pluginv1.NewRuntimeHostClient(conn)}
}

// PublishEvent publishes an event into the host's event bus. The host stamps
// the calling plugin's ID and prefixes the event name accordingly.
func (c *Client) PublishEvent(ctx context.Context, name string, payload map[string]any) error {
	if name == "" {
		return fmt.Errorf("runtimehost: event name is required")
	}
	pb, err := structFromMap("payload", payload)
	if err != nil {
		return err
	}
	_, err = c.rpc.PublishEvent(ctx, &pluginv1.PublishEventRequest{
		EventName: name,
		Payload:   pb,
	})
	return err
}

// PublishEventTo publishes an event addressed to one target plugin_id.
func (c *Client) PublishEventTo(ctx context.Context, targetPluginID, name string, payload map[string]any) error {
	if targetPluginID == "" {
		return fmt.Errorf("runtimehost: target plugin id is required")
	}
	if name == "" {
		return fmt.Errorf("runtimehost: event name is required")
	}
	pb, err := structFromMap("payload", payload)
	if err != nil {
		return err
	}
	_, err = c.rpc.PublishEventTo(ctx, &pluginv1.PublishEventToRequest{
		TargetPluginId: targetPluginID,
		EventName:      name,
		Payload:        pb,
	})
	return err
}

// ListLibraries returns libraries the host knows about, optionally filtered to
// those visible to the given user. Pass empty userID for an admin-scope call.
func (c *Client) ListLibraries(ctx context.Context, userID string) ([]*pluginv1.Library, error) {
	resp, err := c.rpc.ListLibraries(ctx, &pluginv1.ListLibrariesRequest{UserId: userID})
	if err != nil {
		return nil, err
	}
	return resp.GetLibraries(), nil
}

// MediaPresence describes a single host catalog match returned by
// CheckMediaPresence.
type MediaPresence struct {
	ExternalID string
	MediaID    string
	LibraryID  string
	Title      string
}

// CheckMediaPresence asks the host whether any of the supplied external IDs
// already exist in its catalog. Returns a map keyed by the external ID for
// O(1) lookup; absent IDs are simply not in the map. v1 supports provider
// "tmdb" only.
func (c *Client) CheckMediaPresence(ctx context.Context, provider, mediaType string, ids []string) (map[string]MediaPresence, error) {
	if len(ids) == 0 {
		return map[string]MediaPresence{}, nil
	}
	resp, err := c.rpc.CheckMediaPresence(ctx, &pluginv1.CheckMediaPresenceRequest{
		Provider:  provider,
		MediaType: mediaType,
		Ids:       ids,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]MediaPresence, len(resp.GetPresent()))
	for _, p := range resp.GetPresent() {
		out[p.GetExternalId()] = MediaPresence{
			ExternalID: p.GetExternalId(),
			MediaID:    p.GetMediaId(),
			LibraryID:  p.GetLibraryId(),
			Title:      p.GetTitle(),
		}
	}
	return out, nil
}

// ListInstalledPlugins returns installed plugins and their advertised
// capabilities so plugins can discover peers by capability.
func (c *Client) ListInstalledPlugins(ctx context.Context) ([]*pluginv1.InstalledPlugin, error) {
	resp, err := c.rpc.ListInstalledPlugins(ctx, &pluginv1.ListInstalledPluginsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetPlugins(), nil
}

// SetGlobalConfigEntry persists a plugin-owned global config entry for the
// calling plugin installation.
func (c *Client) SetGlobalConfigEntry(ctx context.Context, key string, value map[string]any) error {
	if key == "" {
		return fmt.Errorf("runtimehost: config key is required")
	}
	if value == nil {
		value = map[string]any{}
	}
	pb, err := structpb.NewStruct(value)
	if err != nil {
		return fmt.Errorf("runtimehost: encode value: %w", err)
	}
	_, err = c.rpc.SetGlobalConfigEntry(ctx, &pluginv1.SetGlobalConfigEntryRequest{
		Key:   key,
		Value: pb,
	})
	return err
}

type ListLibraryMediaRequest struct {
	LibraryIDs []string
	MediaTypes []string
	Query      string
	Genre      string
	YearMin    int
	YearMax    int
	Sort       string
	Descending bool
	PageSize   int
	PageToken  string
}

type CatalogMediaItem struct {
	MediaID          string
	LibraryID        string
	MediaType        string
	Title            string
	Year             int
	Overview         string
	PosterURL        string
	BackdropURL      string
	Genres           []string
	RuntimeMinutes   int
	Rating           float64
	ContentRating    string
	AddedAt          string
	ExternalProvider string
	ExternalID       string
}

type ListLibraryMediaResponse struct {
	Items         []CatalogMediaItem
	NextPageToken string
	TotalCount    int
}

func (c *Client) ListLibraryMedia(ctx context.Context, req ListLibraryMediaRequest) (*ListLibraryMediaResponse, error) {
	resp, err := c.rpc.ListLibraryMedia(ctx, &pluginv1.ListLibraryMediaRequest{
		LibraryIds: req.LibraryIDs,
		MediaTypes: req.MediaTypes,
		Query:      req.Query,
		Genre:      req.Genre,
		YearMin:    int32(req.YearMin),
		YearMax:    int32(req.YearMax),
		Sort:       req.Sort,
		Descending: req.Descending,
		PageSize:   int32(req.PageSize),
		PageToken:  req.PageToken,
	})
	if err != nil {
		return nil, err
	}
	out := &ListLibraryMediaResponse{
		Items:         make([]CatalogMediaItem, 0, len(resp.GetItems())),
		NextPageToken: resp.GetNextPageToken(),
		TotalCount:    int(resp.GetTotalCount()),
	}
	for _, it := range resp.GetItems() {
		out.Items = append(out.Items, CatalogMediaItem{
			MediaID:          it.GetMediaId(),
			LibraryID:        it.GetLibraryId(),
			MediaType:        it.GetMediaType(),
			Title:            it.GetTitle(),
			Year:             int(it.GetYear()),
			Overview:         it.GetOverview(),
			PosterURL:        it.GetPosterUrl(),
			BackdropURL:      it.GetBackdropUrl(),
			Genres:           append([]string(nil), it.GetGenres()...),
			RuntimeMinutes:   int(it.GetRuntimeMinutes()),
			Rating:           it.GetRating(),
			ContentRating:    it.GetContentRating(),
			AddedAt:          it.GetAddedAt(),
			ExternalProvider: it.GetExternalProvider(),
			ExternalID:       it.GetExternalId(),
		})
	}
	return out, nil
}

type CatalogStats struct {
	TotalItems      int
	MediaTypeCounts []CatalogTypeCount
	LibraryCounts   []CatalogLibraryCount
}

type CatalogTypeCount struct {
	MediaType string
	Count     int
}

type CatalogLibraryCount struct {
	LibraryID   string
	LibraryName string
	MediaType   string
	Count       int
}

func (c *Client) GetCatalogStats(ctx context.Context, libraryIDs []string) (*CatalogStats, error) {
	resp, err := c.rpc.GetCatalogStats(ctx, &pluginv1.GetCatalogStatsRequest{LibraryIds: libraryIDs})
	if err != nil {
		return nil, err
	}
	out := &CatalogStats{
		TotalItems:      int(resp.GetTotalItems()),
		MediaTypeCounts: make([]CatalogTypeCount, 0, len(resp.GetMediaTypeCounts())),
		LibraryCounts:   make([]CatalogLibraryCount, 0, len(resp.GetLibraryCounts())),
	}
	for _, c := range resp.GetMediaTypeCounts() {
		out.MediaTypeCounts = append(out.MediaTypeCounts, CatalogTypeCount{
			MediaType: c.GetMediaType(),
			Count:     int(c.GetCount()),
		})
	}
	for _, c := range resp.GetLibraryCounts() {
		out.LibraryCounts = append(out.LibraryCounts, CatalogLibraryCount{
			LibraryID:   c.GetLibraryId(),
			LibraryName: c.GetLibraryName(),
			MediaType:   c.GetMediaType(),
			Count:       int(c.GetCount()),
		})
	}
	return out, nil
}

// ResolveCatalogImageURLs resolves stored catalog image paths, such as poster
// or backdrop paths returned by ListLibraryMedia, into browser-usable URLs via
// the host's configured image resolver. The variant argument selects a
// host-supported size or quality; pass empty to use the host default.
func (c *Client) ResolveCatalogImageURLs(ctx context.Context, paths []string, variant string) (map[string]string, error) {
	if len(paths) == 0 {
		return map[string]string{}, nil
	}
	resp, err := c.rpc.ResolveCatalogImageURLs(ctx, &pluginv1.ResolveCatalogImageURLsRequest{
		Paths:   append([]string(nil), paths...),
		Variant: variant,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetUrls(), nil
}

func structFromMap(name string, value map[string]any) (*structpb.Struct, error) {
	if len(value) == 0 {
		return nil, nil
	}
	pb, err := structpb.NewStruct(value)
	if err != nil {
		return nil, fmt.Errorf("runtimehost: encode %s: %w", name, err)
	}
	return pb, nil
}
