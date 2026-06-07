package markers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

const pluginMarkerAlgorithm = "plugin:marker_provider.v1"

type pluginMarkerClient interface {
	FetchMarkers(ctx context.Context, req *pluginv1.FetchMarkersRequest) (*pluginv1.FetchMarkersResponse, error)
	SubmitMarker(ctx context.Context, req *pluginv1.SubmitMarkerRequest) (*pluginv1.SubmitMarkerResponse, error)
	GetMarkerProviderStats(ctx context.Context, req *pluginv1.GetMarkerProviderStatsRequest) (*pluginv1.MarkerProviderStatsResponse, error)
}

type pluginMarkerResolver interface {
	MarkerProviderClient(ctx context.Context, installationID int, capabilityID string) (pluginMarkerClient, error)
}

type pluginMarkerClientFactory func(ctx context.Context, installationID int, capabilityID string) (pluginMarkerClient, error)

type PluginResolverAdapter struct {
	inner interface {
		MarkerProviderClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.MarkerProviderClient, error)
	}
}

func NewPluginResolverAdapter(svc interface {
	MarkerProviderClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.MarkerProviderClient, error)
}) *PluginResolverAdapter {
	if svc == nil {
		return nil
	}
	return &PluginResolverAdapter{inner: svc}
}

func (a *PluginResolverAdapter) MarkerProviderClient(ctx context.Context, installationID int, capabilityID string) (pluginMarkerClient, error) {
	return a.inner.MarkerProviderClient(ctx, installationID, capabilityID)
}

type PluginProviderOptions struct {
	InstallationID      int
	CapabilityID        string
	DisplayName         string
	PluginID            string
	RequiredExternalIDs []string
}

// PluginProvider adapts a marker_provider.v1 plugin capability to Silo's
// marker Provider/Submitter interfaces.
type PluginProvider struct {
	installationID      int
	capabilityID        string
	displayName         string
	pluginID            string
	requiredExternalIDs []string
	clientFactory       pluginMarkerClientFactory
}

var _ Submitter = (*PluginProvider)(nil)

func NewPluginProvider(opts PluginProviderOptions, resolver pluginMarkerResolver) (*PluginProvider, error) {
	if resolver == nil {
		return nil, fmt.Errorf("plugin marker resolver is required")
	}
	return NewPluginProviderWithClientFactory(opts, resolver.MarkerProviderClient)
}

func NewPluginProviderWithClientFactory(opts PluginProviderOptions, clientFactory pluginMarkerClientFactory) (*PluginProvider, error) {
	if opts.InstallationID <= 0 {
		return nil, fmt.Errorf("plugin marker installation id is required")
	}
	if strings.TrimSpace(opts.CapabilityID) == "" {
		return nil, fmt.Errorf("plugin marker capability id is required")
	}
	if clientFactory == nil {
		return nil, fmt.Errorf("plugin marker client factory is required")
	}
	displayName := strings.TrimSpace(opts.DisplayName)
	if displayName == "" {
		displayName = opts.CapabilityID
	}
	requiredIDs := normalizeExternalIDKeys(opts.RequiredExternalIDs)
	return &PluginProvider{
		installationID:      opts.InstallationID,
		capabilityID:        strings.TrimSpace(opts.CapabilityID),
		displayName:         displayName,
		pluginID:            strings.TrimSpace(opts.PluginID),
		requiredExternalIDs: requiredIDs,
		clientFactory:       clientFactory,
	}, nil
}

func PluginProviderID(installationID int, capabilityID string) string {
	return fmt.Sprintf("plugin:%d:%s", installationID, strings.TrimSpace(capabilityID))
}

func (p *PluginProvider) ID() string {
	if p == nil {
		return ""
	}
	return PluginProviderID(p.installationID, p.capabilityID)
}

func (p *PluginProvider) ProviderDescription() ProviderDescriptor {
	if p == nil {
		return ProviderDescriptor{}
	}
	return ProviderDescriptor{
		ID:                   p.ID(),
		DisplayName:          p.displayName,
		SourceType:           ProviderSourcePlugin,
		PluginID:             p.pluginID,
		PluginInstallationID: p.installationID,
		CapabilityID:         p.capabilityID,
	}
}

func (p *PluginProvider) SubmissionRequirements() SubmissionRequirements {
	if p == nil || len(p.requiredExternalIDs) == 0 {
		return SubmissionRequirements{}
	}
	return SubmissionRequirements{RequiredExternalIDs: append([]string(nil), p.requiredExternalIDs...)}
}

func (p *PluginProvider) FetchMarkers(ctx context.Context, req Request) (Result, error) {
	if p == nil || p.clientFactory == nil {
		return Result{}, nil
	}
	client, err := p.clientFactory(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return Result{}, err
	}
	response, err := client.FetchMarkers(ctx, &pluginv1.FetchMarkersRequest{
		ItemType:        itemTypeName(req.Kind),
		ExternalIds:     externalIDsProto(req.ExternalIDs),
		SeasonNumber:    int32(req.SeasonNumber),
		EpisodeNumber:   int32(req.EpisodeNumber),
		DurationSeconds: req.Duration.Seconds(),
	})
	if err != nil {
		return Result{}, pluginProviderError(p.ID(), err)
	}
	if response == nil {
		return Result{}, nil
	}

	result := Result{
		SourceClass: models.MarkerSourcePlugin,
		ProviderID:  p.ID(),
		Algorithm:   pluginMarkerAlgorithm,
	}
	for _, segment := range response.GetMarkers() {
		marker, ok := markerFromPluginSegment(segment, req.Duration)
		if !ok {
			continue
		}
		marker.SourceClass = models.MarkerSourcePlugin
		marker.ProviderID = p.ID()
		if marker.Algorithm == "" {
			marker.Algorithm = pluginMarkerAlgorithm
		}
		result.Markers = append(result.Markers, marker)
	}
	return result, nil
}

func (p *PluginProvider) SubmitMarker(ctx context.Context, req SubmissionRequest) (SubmissionResult, error) {
	if p == nil || p.clientFactory == nil {
		return SubmissionResult{}, fmt.Errorf("plugin marker provider not configured")
	}
	client, err := p.clientFactory(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return SubmissionResult{}, err
	}
	body := &pluginv1.SubmitMarkerRequest{
		ItemType:        itemTypeName(req.Kind),
		ExternalIds:     externalIDsProto(req.ExternalIDs),
		SeasonNumber:    int32(req.SeasonNumber),
		EpisodeNumber:   int32(req.EpisodeNumber),
		Segment:         markerKindName(req.Segment),
		DurationSeconds: req.Duration.Seconds(),
	}
	if req.Start != nil {
		start := req.Start.Seconds()
		body.StartSeconds = &start
	}
	if req.End != nil {
		end := req.End.Seconds()
		body.EndSeconds = &end
	}
	response, err := client.SubmitMarker(ctx, body)
	if err != nil {
		return SubmissionResult{}, pluginProviderError(p.ID(), err)
	}
	if response == nil {
		return SubmissionResult{Status: SubmissionStatusPending}, nil
	}
	status := strings.TrimSpace(response.GetStatus())
	if status == "" {
		status = SubmissionStatusPending
	}
	return SubmissionResult{ID: response.GetSubmissionId(), Status: status, Weight: response.GetWeight()}, nil
}

func (p *PluginProvider) FetchUserStats(ctx context.Context) (UserStats, error) {
	if p == nil || p.clientFactory == nil {
		return UserStats{}, fmt.Errorf("plugin marker provider not configured")
	}
	client, err := p.clientFactory(ctx, p.installationID, p.capabilityID)
	if err != nil {
		return UserStats{}, err
	}
	response, err := client.GetMarkerProviderStats(ctx, &pluginv1.GetMarkerProviderStatsRequest{})
	if err != nil {
		return UserStats{}, pluginProviderError(p.ID(), err)
	}
	if response == nil {
		return UserStats{}, nil
	}
	return UserStats{
		Total:          int(response.GetTotal()),
		Accepted:       int(response.GetAccepted()),
		Pending:        int(response.GetPending()),
		Rejected:       int(response.GetRejected()),
		AcceptanceRate: response.GetAcceptanceRate(),
		CurrentStreak:  int(response.GetCurrentStreak()),
		BestStreak:     int(response.GetBestStreak()),
	}, nil
}

func markerFromPluginSegment(segment *pluginv1.MarkerSegment, duration time.Duration) (Marker, bool) {
	if segment == nil {
		return Marker{}, false
	}
	kind, ok := markerKindFromName(segment.GetSegment())
	if !ok {
		return Marker{}, false
	}
	start := time.Duration(0)
	end := duration
	if segment.StartSeconds != nil {
		start = secondsDuration(segment.GetStartSeconds())
	}
	if segment.EndSeconds != nil {
		end = secondsDuration(segment.GetEndSeconds())
	}
	if start < 0 {
		return Marker{}, false
	}
	if duration > 0 && (start >= duration || end > duration) {
		return Marker{}, false
	}
	if end <= start {
		return Marker{}, false
	}
	return Marker{
		Kind:            kind,
		Start:           start,
		End:             end,
		Confidence:      segment.GetConfidence(),
		SubmissionCount: int(segment.GetSubmissionCount()),
		Algorithm:       strings.TrimSpace(segment.GetAlgorithm()),
	}, true
}

func externalIDsProto(ids map[string]string) *pluginv1.MarkerExternalIDs {
	if len(ids) == 0 {
		return &pluginv1.MarkerExternalIDs{}
	}
	providerIDs := make(map[string]any, len(ids))
	for key, value := range ids {
		if strings.TrimSpace(value) != "" {
			providerIDs[key] = value
		}
	}
	structIDs, _ := structpb.NewStruct(providerIDs)
	return &pluginv1.MarkerExternalIDs{
		TmdbId:      strings.TrimSpace(ids[ExternalIDKeyTMDB]),
		ImdbId:      strings.TrimSpace(ids[ExternalIDKeyIMDB]),
		TvdbId:      strings.TrimSpace(ids[ExternalIDKeyTVDB]),
		ProviderIds: structIDs,
	}
}

func itemTypeName(kind ItemKind) string {
	switch kind {
	case ItemKindEpisode:
		return "episode"
	case ItemKindMovie:
		return "movie"
	default:
		return ""
	}
}

func markerKindName(kind MarkerKind) string {
	switch kind {
	case MarkerKindIntro:
		return "intro"
	case MarkerKindCredits:
		return "credits"
	case MarkerKindRecap:
		return "recap"
	case MarkerKindPreview:
		return "preview"
	default:
		return ""
	}
}

func markerKindFromName(name string) (MarkerKind, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "intro":
		return MarkerKindIntro, true
	case "credits":
		return MarkerKindCredits, true
	case "recap":
		return MarkerKindRecap, true
	case "preview":
		return MarkerKindPreview, true
	default:
		return 0, false
	}
}

func secondsDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}

func normalizeExternalIDKeys(keys []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		switch key {
		case ExternalIDKeyTMDB, ExternalIDKeyIMDB, ExternalIDKeyTVDB:
		default:
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func pluginProviderError(providerID string, err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.ResourceExhausted {
		return err
	}
	retryAfter := time.Duration(0)
	for _, detail := range st.Details() {
		if retry, ok := detail.(*errdetails.RetryInfo); ok && retry.GetRetryDelay() != nil {
			retryAfter = retry.GetRetryDelay().AsDuration()
			break
		}
	}
	return &RetryAfterError{Provider: providerID, RetryAfter: retryAfter, Message: err.Error()}
}

func PluginRequiredExternalIDsFromMetadata(metadata map[string]any) []string {
	raw, ok := metadata["required_external_ids"]
	if !ok {
		return nil
	}
	switch typed := raw.(type) {
	case []string:
		return normalizeExternalIDKeys(typed)
	case []any:
		keys := make([]string, 0, len(typed))
		for _, value := range typed {
			if s, ok := value.(string); ok {
				keys = append(keys, s)
			}
		}
		return normalizeExternalIDKeys(keys)
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return normalizeExternalIDKeys(strings.Split(typed, ","))
	default:
		return nil
	}
}

func PluginDefaultFetchPriorityFromMetadata(metadata map[string]any) (int, bool) {
	raw, ok := metadata["default_fetch_priority"]
	if !ok {
		return 0, false
	}
	switch typed := raw.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}
