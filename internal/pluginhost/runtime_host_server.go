package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"golang.org/x/time/rate"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/events"
)

// EventPublisher is the subset of *events.Hub required by RuntimeHostServer.
// Defined as an interface so tests can supply a fake.
type EventPublisher interface {
	Publish(ctx context.Context, env events.Envelope) error
}

// LibraryRecord is the wire shape for libraries returned to plugins. Mirrors
// the proto Library message.
type LibraryRecord struct {
	ID        string
	Name      string
	MediaType string // movie | tv | mixed
}

// LibraryLister returns libraries visible to a user (or all when userID is "").
type LibraryLister interface {
	ListLibraries(ctx context.Context, userID string) ([]LibraryRecord, error)
}

// LibraryPresenceRecord is a single host catalog match returned by a
// CatalogPresenceLookup. The plugin sees it as proto MediaPresence.
type LibraryPresenceRecord struct {
	ExternalID string
	MediaID    string
	LibraryID  string
	Title      string
}

// CatalogPresenceLookup answers "which of these external IDs do we already
// have?" for a batch of IDs. v1 supports provider "tmdb" only; other values
// should return the empty result without error.
type CatalogPresenceLookup interface {
	LookupByExternalIDs(ctx context.Context, provider, mediaType string, ids []string) ([]LibraryPresenceRecord, error)
}

// InstalledPluginRecord is the host-side shape returned to plugins for peer
// discovery.
type InstalledPluginRecord struct {
	InstallationID int
	PluginID       string
	Version        string
	Enabled        bool
	Capabilities   []*pluginv1.CapabilityDescriptor
}

// InstalledPluginLister returns installed plugins and their capability
// descriptors for RuntimeHost.ListInstalledPlugins.
type InstalledPluginLister interface {
	ListInstalledPlugins(ctx context.Context) ([]InstalledPluginRecord, error)
}

// InstalledPluginListerFunc adapts a plain function to InstalledPluginLister.
type InstalledPluginListerFunc func(ctx context.Context) ([]InstalledPluginRecord, error)

func (f InstalledPluginListerFunc) ListInstalledPlugins(ctx context.Context) ([]InstalledPluginRecord, error) {
	return f(ctx)
}

// GlobalConfigSetter persists a global config entry for a plugin installation.
type GlobalConfigSetter interface {
	SetGlobalConfigEntry(ctx context.Context, installationID int, key string, value map[string]any) error
}

// GlobalConfigSetterFunc adapts a plain function to GlobalConfigSetter.
type GlobalConfigSetterFunc func(ctx context.Context, installationID int, key string, value map[string]any) error

func (f GlobalConfigSetterFunc) SetGlobalConfigEntry(ctx context.Context, installationID int, key string, value map[string]any) error {
	return f(ctx, installationID, key, value)
}

// VirtualCatalogRegistrar performs host-owned transactional registration of
// virtual media submitted by an installed plugin.
type VirtualCatalogRegistrar interface {
	UpsertVirtualMedia(context.Context, int, catalog.VirtualMedia) (*catalog.VirtualMediaResult, error)
}

type VirtualCatalogRegistrarFunc func(context.Context, int, catalog.VirtualMedia) (*catalog.VirtualMediaResult, error)

func (f VirtualCatalogRegistrarFunc) UpsertVirtualMedia(ctx context.Context, installationID int, req catalog.VirtualMedia) (*catalog.VirtualMediaResult, error) {
	return f(ctx, installationID, req)
}

// DefaultPublishEventRatePerSec is the default maximum number of events a
// plugin may publish per second. It also serves as the burst size so a plugin
// can fire a short burst at a higher rate before being throttled.
const DefaultPublishEventRatePerSec = 100

// RuntimeHostServer is the gRPC server that plugins call back into via the
// go-plugin broker. Each plugin instance gets its own RuntimeHostServer so
// the pluginID is fixed at construction.
type RuntimeHostServer struct {
	pluginv1.UnimplementedRuntimeHostServer
	publisher EventPublisher
	libs      LibraryLister
	catalog   CatalogPresenceLookup
	pluginID  string
	limiter   *rate.Limiter

	installedPlugins InstalledPluginLister
	configSetter     GlobalConfigSetter
	virtualCatalog   VirtualCatalogRegistrar
	installationID   int
}

// NewRuntimeHostServer constructs a RuntimeHostServer bound to the given
// pluginID. The pluginID is server-stamped on all published events so plugins
// cannot forge core event names.
func NewRuntimeHostServer(publisher EventPublisher, libs LibraryLister, pluginID string) *RuntimeHostServer {
	return &RuntimeHostServer{
		publisher: publisher,
		libs:      libs,
		pluginID:  pluginID,
		limiter:   rate.NewLimiter(rate.Limit(DefaultPublishEventRatePerSec), DefaultPublishEventRatePerSec),
	}
}

// NewRuntimeHostServerWithRate is like NewRuntimeHostServer but installs a
// caller-specified rate limit (events/sec, also used as burst). Pass perSec<=0
// to fall back to DefaultPublishEventRatePerSec.
func NewRuntimeHostServerWithRate(publisher EventPublisher, libs LibraryLister, pluginID string, perSec int) *RuntimeHostServer {
	if perSec <= 0 {
		perSec = DefaultPublishEventRatePerSec
	}
	return &RuntimeHostServer{
		publisher: publisher,
		libs:      libs,
		pluginID:  pluginID,
		limiter:   rate.NewLimiter(rate.Limit(perSec), perSec),
	}
}

// NewRuntimeHostServerWithCatalog is like NewRuntimeHostServer but also
// accepts a CatalogPresenceLookup. Use this when the host can answer
// CheckMediaPresence; passing nil makes presence queries return empty.
func NewRuntimeHostServerWithCatalog(publisher EventPublisher, libs LibraryLister, catalog CatalogPresenceLookup, pluginID string) *RuntimeHostServer {
	s := NewRuntimeHostServer(publisher, libs, pluginID)
	s.catalog = catalog
	return s
}

// NewRuntimeHostServerWithServices is like NewRuntimeHostServerWithCatalog but
// also enables peer discovery and plugin-owned config persistence.
func NewRuntimeHostServerWithServices(
	publisher EventPublisher,
	libs LibraryLister,
	catalog CatalogPresenceLookup,
	installedPlugins InstalledPluginLister,
	configSetter GlobalConfigSetter,
	virtualCatalog VirtualCatalogRegistrar,
	pluginID string,
	installationID int,
) *RuntimeHostServer {
	s := NewRuntimeHostServerWithCatalog(publisher, libs, catalog, pluginID)
	s.installedPlugins = installedPlugins
	s.configSetter = configSetter
	s.virtualCatalog = virtualCatalog
	s.installationID = installationID
	return s
}

func (s *RuntimeHostServer) UpsertVirtualMedia(ctx context.Context, req *pluginv1.UpsertVirtualMediaRequest) (*pluginv1.UpsertVirtualMediaResponse, error) {
	if s.virtualCatalog == nil {
		return nil, fmt.Errorf("server: virtual catalog is not configured")
	}
	if s.installationID <= 0 {
		return nil, fmt.Errorf("server: plugin installation is not bound")
	}

	variants := make([]catalog.VirtualMediaVariant, 0, len(req.GetVariants()))
	for _, v := range req.GetVariants() {
		variants = append(variants, catalog.VirtualMediaVariant{
			VirtualURI:     v.GetVirtualUri(),
			Label:          v.GetLabel(),
			Resolution:     v.GetResolution(),
			CodecVideo:     v.GetCodecVideo(),
			CodecAudio:     v.GetCodecAudio(),
			HDR:            v.GetHdr(),
			Bitrate:        int(v.GetBitrate()),
			RuntimeMinutes: int(v.GetRuntimeMinutes()),
		})
	}

	episodes := make([]catalog.VirtualEpisode, 0, len(req.GetEpisodes()))
	for _, episode := range req.GetEpisodes() {
		var airDate time.Time
		if episode.GetAirDateUnix() > 0 {
			airDate = time.Unix(episode.GetAirDateUnix(), 0).UTC()
		}
		epVariants := make([]catalog.VirtualMediaVariant, 0, len(episode.GetVariants()))
		for _, v := range episode.GetVariants() {
			epVariants = append(epVariants, catalog.VirtualMediaVariant{
				VirtualURI:     v.GetVirtualUri(),
				Label:          v.GetLabel(),
				Resolution:     v.GetResolution(),
				CodecVideo:     v.GetCodecVideo(),
				CodecAudio:     v.GetCodecAudio(),
				HDR:            v.GetHdr(),
				Bitrate:        int(v.GetBitrate()),
				RuntimeMinutes: int(v.GetRuntimeMinutes()),
			})
		}
		episodes = append(episodes, catalog.VirtualEpisode{
			SeasonNumber: int(episode.GetSeasonNumber()), EpisodeNumber: int(episode.GetEpisodeNumber()),
			Title: episode.GetTitle(), Overview: episode.GetOverview(), AirDate: airDate,
			RuntimeMinutes: int(episode.GetRuntimeMinutes()), StillPath: episode.GetStillPath(), VirtualURI: episode.GetVirtualUri(),
			Variants: epVariants,
		})
	}

	vm := catalog.VirtualMedia{
		LibraryID: req.GetLibraryId(), MediaType: req.GetMediaType(), Title: req.GetTitle(), Year: int(req.GetYear()),
		IMDbID: req.GetImdbId(), TMDBID: req.GetTmdbId(), TVDBID: req.GetTvdbId(), Overview: req.GetOverview(),
		Genres: req.GetGenres(), PosterPath: req.GetPosterPath(), BackdropPath: req.GetBackdropPath(),
		VirtualURI: req.GetVirtualUri(), RuntimeMinutes: int(req.GetRuntimeMinutes()), Episodes: episodes,
		Variants: variants,
	}

	result, err := s.virtualCatalog.UpsertVirtualMedia(ctx, s.installationID, vm)
	if err != nil {
		return nil, err
	}
	return &pluginv1.UpsertVirtualMediaResponse{MediaId: result.MediaID, LibraryId: result.LibraryID, EpisodesUpserted: int32(result.EpisodesUpserted)}, nil
}

// PublishEvent auto-prefixes the plugin's event name with "plugin.<plugin_id>."
// and forwards to the EventPublisher on events.ChannelPlugins. The plugin ID
// is server-stamped (not taken from the request) so plugins cannot forge
// core event names by crafting a malicious event_name.
func (s *RuntimeHostServer) PublishEvent(ctx context.Context, req *pluginv1.PublishEventRequest) (*pluginv1.PublishEventResponse, error) {
	if s.limiter != nil && !s.limiter.Allow() {
		return nil, fmt.Errorf("rate limit exceeded for plugin %q", s.pluginID)
	}
	name := strings.TrimSpace(req.GetEventName())
	if name == "" {
		return nil, fmt.Errorf("event_name is required")
	}
	if s.pluginID == "" {
		return nil, fmt.Errorf("server: plugin id not bound")
	}
	if s.publisher == nil {
		return nil, fmt.Errorf("server: event publisher not configured")
	}

	var payload json.RawMessage
	if p := req.GetPayload(); p != nil {
		raw, err := p.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("encode payload: %w", err)
		}
		payload = raw
	}

	prefixed := "plugin." + s.pluginID + "." + name
	env := events.Envelope{
		Channel: events.ChannelPlugins,
		Event:   prefixed,
		Data:    payload,
	}
	if err := s.publisher.Publish(ctx, env); err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	return &pluginv1.PublishEventResponse{}, nil
}

// PublishEventTo is like PublishEvent but restricts delivery to subscribers
// belonging to the target plugin_id.
func (s *RuntimeHostServer) PublishEventTo(ctx context.Context, req *pluginv1.PublishEventToRequest) (*pluginv1.PublishEventToResponse, error) {
	if s.limiter != nil && !s.limiter.Allow() {
		return nil, fmt.Errorf("rate limit exceeded for plugin %q", s.pluginID)
	}
	name := strings.TrimSpace(req.GetEventName())
	if name == "" {
		return nil, fmt.Errorf("event_name is required")
	}
	targetPluginID := strings.TrimSpace(req.GetTargetPluginId())
	if targetPluginID == "" {
		return nil, fmt.Errorf("target_plugin_id is required")
	}
	if s.pluginID == "" {
		return nil, fmt.Errorf("server: plugin id not bound")
	}
	if s.publisher == nil {
		return nil, fmt.Errorf("server: event publisher not configured")
	}

	var payload json.RawMessage
	if p := req.GetPayload(); p != nil {
		raw, err := p.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("encode payload: %w", err)
		}
		payload = raw
	}

	env := events.Envelope{
		Channel:        events.ChannelPlugins,
		Event:          "plugin." + s.pluginID + "." + name,
		Data:           payload,
		TargetPluginID: targetPluginID,
	}
	if err := s.publisher.Publish(ctx, env); err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	return &pluginv1.PublishEventToResponse{}, nil
}

// ListLibraries delegates to the LibraryLister, mapping the userID from the
// request through to the underlying data source.
func (s *RuntimeHostServer) ListLibraries(ctx context.Context, req *pluginv1.ListLibrariesRequest) (*pluginv1.ListLibrariesResponse, error) {
	if s.libs == nil {
		return &pluginv1.ListLibrariesResponse{}, nil
	}
	rows, err := s.libs.ListLibraries(ctx, req.GetUserId())
	if err != nil {
		return nil, fmt.Errorf("list libraries: %w", err)
	}
	resp := &pluginv1.ListLibrariesResponse{Libraries: make([]*pluginv1.Library, 0, len(rows))}
	for _, r := range rows {
		resp.Libraries = append(resp.Libraries, &pluginv1.Library{
			Id:        r.ID,
			Name:      r.Name,
			MediaType: r.MediaType,
		})
	}
	return resp, nil
}

// ListInstalledPlugins returns installed plugins and their advertised
// capabilities for peer discovery.
func (s *RuntimeHostServer) ListInstalledPlugins(ctx context.Context, _ *pluginv1.ListInstalledPluginsRequest) (*pluginv1.ListInstalledPluginsResponse, error) {
	if s.installedPlugins == nil {
		return &pluginv1.ListInstalledPluginsResponse{}, nil
	}
	rows, err := s.installedPlugins.ListInstalledPlugins(ctx)
	if err != nil {
		return nil, fmt.Errorf("list installed plugins: %w", err)
	}
	resp := &pluginv1.ListInstalledPluginsResponse{Plugins: make([]*pluginv1.InstalledPlugin, 0, len(rows))}
	for _, row := range rows {
		resp.Plugins = append(resp.Plugins, &pluginv1.InstalledPlugin{
			InstallationId: int64(row.InstallationID),
			PluginId:       row.PluginID,
			Version:        row.Version,
			Enabled:        row.Enabled,
			Capabilities:   row.Capabilities,
		})
	}
	return resp, nil
}

// SetGlobalConfigEntry persists a plugin-owned global config entry for this
// plugin installation.
func (s *RuntimeHostServer) SetGlobalConfigEntry(ctx context.Context, req *pluginv1.SetGlobalConfigEntryRequest) (*pluginv1.SetGlobalConfigEntryResponse, error) {
	key := strings.TrimSpace(req.GetKey())
	if key == "" {
		return nil, fmt.Errorf("key is required")
	}
	if s.installationID == 0 {
		return nil, fmt.Errorf("server: installation id not bound")
	}
	if s.configSetter == nil {
		return nil, fmt.Errorf("server: config setter not configured")
	}
	value := map[string]any{}
	if req.GetValue() != nil {
		value = req.GetValue().AsMap()
	}
	if err := s.configSetter.SetGlobalConfigEntry(ctx, s.installationID, key, value); err != nil {
		return nil, fmt.Errorf("set global config entry: %w", err)
	}
	return &pluginv1.SetGlobalConfigEntryResponse{}, nil
}

// CheckMediaPresence delegates to the configured CatalogPresenceLookup.
// Returns the empty list when no catalog is configured.
func (s *RuntimeHostServer) CheckMediaPresence(ctx context.Context, req *pluginv1.CheckMediaPresenceRequest) (*pluginv1.CheckMediaPresenceResponse, error) {
	if len(req.GetIds()) > 100 {
		return nil, fmt.Errorf("ids: too many (%d), max 100", len(req.GetIds()))
	}
	if s.catalog == nil {
		return &pluginv1.CheckMediaPresenceResponse{}, nil
	}
	rows, err := s.catalog.LookupByExternalIDs(ctx, req.GetProvider(), req.GetMediaType(), req.GetIds())
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	resp := &pluginv1.CheckMediaPresenceResponse{
		Present: make([]*pluginv1.MediaPresence, 0, len(rows)),
	}
	for _, r := range rows {
		resp.Present = append(resp.Present, &pluginv1.MediaPresence{
			ExternalId: r.ExternalID,
			MediaId:    r.MediaID,
			LibraryId:  r.LibraryID,
			Title:      r.Title,
		})
	}
	return resp, nil
}
