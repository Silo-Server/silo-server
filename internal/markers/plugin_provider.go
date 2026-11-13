package markers

import (
	"context"
	"fmt"
	"math"
	"net/http"
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

func (a *PluginResolverAdapter) RefreshMarkerRuntime(installationID int) error {
	if refresher, ok := a.inner.(interface{ RefreshMarkerRuntime(int) error }); ok {
		return refresher.RefreshMarkerRuntime(installationID)
	}
	return nil
}

type PluginProviderOptions struct {
	InstallationID      int
	CapabilityID        string
	DisplayName         string
	PluginID            string
	CacheRevision       string
	RequiredExternalIDs []string
	SupportsAmend       bool
	SupportsAbsent      bool
	// DefaultContributeEnabled records the manifest's
	// default_contribute_enabled for the admin API.
	DefaultContributeEnabled bool
}

// PluginProvider adapts a marker_provider.v1 plugin capability to Silo's
// marker Provider/Submitter interfaces.
type PluginProvider struct {
	installationID      int
	capabilityID        string
	displayName         string
	pluginID            string
	cacheRevision       string
	requiredExternalIDs []string
	supportsAmend       bool
	supportsAbsent      bool
	defaultContribute   bool
	clientFactory       pluginMarkerClientFactory
}

var _ Submitter = (*PluginProvider)(nil)

// ReadOnlyPluginProvider exposes only the fetch side of a marker plugin whose
// capability declares supports_contribution=false. The plugin gRPC service has
// submit methods whether or not the provider accepts contributions, so leaving
// them off this wrapper makes the Submitter assertion used by contribution and
// the admin API follow the manifest instead of the service shape.
type ReadOnlyPluginProvider struct {
	inner *PluginProvider
}

var (
	_ Provider          = (*ReadOnlyPluginProvider)(nil)
	_ DescribedProvider = (*ReadOnlyPluginProvider)(nil)
)

func NewReadOnlyPluginProvider(inner *PluginProvider) *ReadOnlyPluginProvider {
	return &ReadOnlyPluginProvider{inner: inner}
}

func (p *ReadOnlyPluginProvider) ID() string {
	if p == nil || p.inner == nil {
		return ""
	}
	return p.inner.ID()
}

// CacheRevision keeps the plugin revision in the population cache identity and
// in the reload check that decides whether the runtime needs refreshing.
func (p *ReadOnlyPluginProvider) CacheRevision() string {
	if p == nil || p.inner == nil {
		return ""
	}
	return p.inner.CacheRevision()
}

func (p *ReadOnlyPluginProvider) ProviderDescription() ProviderDescriptor {
	if p == nil || p.inner == nil {
		return ProviderDescriptor{}
	}
	return p.inner.ProviderDescription()
}

func (p *ReadOnlyPluginProvider) FetchMarkers(ctx context.Context, req Request) (Result, error) {
	if p == nil || p.inner == nil {
		return Result{}, nil
	}
	return p.inner.FetchMarkers(ctx, req)
}

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
		cacheRevision:       opts.CacheRevision,
		requiredExternalIDs: requiredIDs,
		supportsAmend:       opts.SupportsAmend,
		supportsAbsent:      opts.SupportsAbsent,
		defaultContribute:   opts.DefaultContributeEnabled,
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

// CacheRevision identifies the plugin version and configuration behind cached
// lookups. It contains configuration timestamps, never configuration values.
func (p *PluginProvider) CacheRevision() string {
	if p == nil {
		return ""
	}
	return p.cacheRevision
}

func (p *PluginProvider) ProviderDescription() ProviderDescriptor {
	if p == nil {
		return ProviderDescriptor{}
	}
	return ProviderDescriptor{
		ID:                       p.ID(),
		DisplayName:              p.displayName,
		SourceType:               ProviderSourcePlugin,
		PluginID:                 p.pluginID,
		PluginInstallationID:     p.installationID,
		CapabilityID:             p.capabilityID,
		DefaultContributeEnabled: p.defaultContribute,
	}
}

func (p *PluginProvider) SubmissionRequirements() SubmissionRequirements {
	if p == nil {
		return SubmissionRequirements{}
	}
	reqs := SubmissionRequirements{SupportsAmend: p.supportsAmend, SupportsAbsent: p.supportsAbsent}
	if len(p.requiredExternalIDs) > 0 {
		reqs.RequiredExternalIDs = append([]string(nil), p.requiredExternalIDs...)
	}
	return reqs
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
		ItemType:           itemTypeName(req.Kind),
		ExternalIds:        externalIDsProto(req.ExternalIDs),
		SeasonNumber:       int32(req.SeasonNumber),  //nolint:gosec // integer columns in the catalog
		EpisodeNumber:      int32(req.EpisodeNumber), //nolint:gosec // integer columns in the catalog
		DurationSeconds:    req.Duration.Seconds(),
		EpisodeExternalIds: episodeExternalIDsProto(req.EpisodeExternalIDs),
		EpisodeNumbering:   req.EpisodeNumbering,
		EpisodeTitle:       req.EpisodeTitle,
		FileHash:           req.FileHash,
		FileSize:           req.FileSize,
		AcceptsAbsent:      true,
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
		ItemType:           itemTypeName(req.Kind),
		ExternalIds:        externalIDsProto(req.ExternalIDs),
		SeasonNumber:       int32(req.SeasonNumber),  //nolint:gosec // integer columns in the catalog
		EpisodeNumber:      int32(req.EpisodeNumber), //nolint:gosec // integer columns in the catalog
		Segment:            markerKindName(req.Segment),
		DurationSeconds:    req.Duration.Seconds(),
		EpisodeExternalIds: episodeExternalIDsProto(req.EpisodeExternalIDs),
		EpisodeNumbering:   req.EpisodeNumbering,
		EpisodeTitle:       req.EpisodeTitle,
		FileHash:           req.FileHash,
		FileSize:           req.FileSize,
		Source:             req.Source,
		Algorithm:          req.Algorithm,
		Confidence:         req.Confidence,
		Absent:             req.Absent,
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
	// Confidence decides between providers, so a value that is not a valid
	// 0-1 score (NaN, infinite, or out of range) counts as unrated, on an
	// absent answer too.
	confidence := segment.GetConfidence()
	if math.IsNaN(confidence) || confidence < 0 || confidence > 1 {
		confidence = 0
	}
	// An absent answer has no bounds. Read as a segment, the missing bounds
	// would default to the whole file.
	if segment.GetAbsent() {
		return Marker{
			Kind:            kind,
			Absent:          true,
			Confidence:      confidence,
			SubmissionCount: int(segment.GetSubmissionCount()),
			Algorithm:       strings.TrimSpace(segment.GetAlgorithm()),
		}, true
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
		Confidence:      confidence,
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

// episodeExternalIDsProto leaves the field unset for a movie or an episode
// without its own IDs, as a host that predates the field would.
func episodeExternalIDsProto(ids map[string]string) *pluginv1.MarkerExternalIDs {
	if len(ids) == 0 {
		return nil
	}
	return externalIDsProto(ids)
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
		return models.MarkerSegmentIntro
	case MarkerKindCredits:
		return models.MarkerSegmentCredits
	case MarkerKindRecap:
		return models.MarkerSegmentRecap
	case MarkerKindPreview:
		return models.MarkerSegmentPreview
	default:
		return ""
	}
}

func markerKindFromName(name string) (MarkerKind, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case models.MarkerSegmentIntro:
		return MarkerKindIntro, true
	case models.MarkerSegmentCredits:
		return MarkerKindCredits, true
	case models.MarkerSegmentRecap:
		return MarkerKindRecap, true
	case models.MarkerSegmentPreview:
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
	message := err.Error()
	if ok {
		message = st.Message()
	}
	upstreamStatus, legacyHTTPStatus := pluginSubmissionHTTPStatus(message)
	if (ok && st.Code() == codes.AlreadyExists) || (legacyHTTPStatus && upstreamStatus == http.StatusConflict) {
		return &SubmissionConflictError{
			Provider:   providerID,
			HTTPStatus: http.StatusConflict,
			Message:    err.Error(),
		}
	}
	// InvalidArgument describes the request itself. FailedPrecondition and
	// similar codes describe provider state, such as missing setup, and stay
	// retryable.
	if (ok && st.Code() == codes.InvalidArgument) ||
		(legacyHTTPStatus && permanentSubmissionHTTPStatus(upstreamStatus)) {
		invalid := &SubmissionInvalidError{Provider: providerID, Message: err.Error()}
		if legacyHTTPStatus {
			invalid.HTTPStatus = upstreamStatus
		}
		return invalid
	}
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

// pluginSubmissionHTTPStatus recognizes the legacy marker-provider error text
// used before plugins could expose an AlreadyExists gRPC status. Keep this
// narrow to submit errors so unrelated provider failures are not reclassified.
func pluginSubmissionHTTPStatus(message string) (int, bool) {
	const marker = "submit HTTP "
	start := strings.Index(message, marker)
	if start < 0 {
		return 0, false
	}
	tail := strings.TrimSpace(message[start+len(marker):])
	if end := strings.IndexAny(tail, ": "); end >= 0 {
		tail = tail[:end]
	}
	code, err := strconv.Atoi(tail)
	if err != nil || code < 100 || code > 599 {
		return 0, false
	}
	return code, true
}

// permanentSubmissionHTTPStatus reports upstream refusals of the submitted
// item that retrying cannot fix. Timeouts, conflicts, and rate limits are
// handled elsewhere or stay retryable.
func permanentSubmissionHTTPStatus(code int) bool {
	switch code {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
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

// PluginContributionDefaults is the contribution configuration a marker
// capability asks for when its provider config row is first created.
type PluginContributionDefaults struct {
	Enabled       bool
	AutoLocal     bool
	MinConfidence float64
}

// PluginContributionDefaultsFromMetadata reads default_contribute_enabled,
// default_contribute_auto_local and default_contribute_min_confidence. Missing
// or malformed keys keep contribution off with a 0.95 minimum, as before the
// keys existed.
func PluginContributionDefaultsFromMetadata(metadata map[string]any) PluginContributionDefaults {
	defaults := PluginContributionDefaults{
		Enabled:       pluginMetadataBool(metadata, "default_contribute_enabled", false),
		AutoLocal:     pluginMetadataBool(metadata, "default_contribute_auto_local", false),
		MinConfidence: 0.95,
	}
	if confidence, ok := pluginMetadataNumber(metadata, "default_contribute_min_confidence"); ok && confidence >= 0 && confidence <= 1 {
		defaults.MinConfidence = confidence
	}
	return defaults
}

// PluginSupportsContributionFromMetadata reports whether a marker capability
// accepts submissions. Existing manifests omit the key and keep the historical
// submitter behavior; only an explicit false makes the provider fetch-only.
func PluginSupportsContributionFromMetadata(metadata map[string]any) bool {
	return pluginMetadataBool(metadata, "supports_contribution", true)
}

// PluginSupportsAmendFromMetadata reports whether a marker capability replaces
// a server's earlier submission for the same file and kind, so the host may
// resubmit a changed marker.
func PluginSupportsAmendFromMetadata(metadata map[string]any) bool {
	return pluginMetadataBool(metadata, "supports_amend", false)
}

// PluginSupportsAbsentFromMetadata reports whether a marker capability accepts
// submissions saying an item has no segment of a kind.
func PluginSupportsAbsentFromMetadata(metadata map[string]any) bool {
	return pluginMetadataBool(metadata, "supports_absent", false)
}

func pluginMetadataNumber(metadata map[string]any, key string) (float64, bool) {
	switch value := metadata[key].(type) {
	case int:
		return float64(value), true
	case int32:
		return float64(value), true
	case int64:
		return float64(value), true
	case float64:
		return value, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func pluginMetadataBool(metadata map[string]any, key string, fallback bool) bool {
	switch value := metadata[key].(type) {
	case bool:
		return value
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fallback
		}
		return parsed
	default:
		return fallback
	}
}
