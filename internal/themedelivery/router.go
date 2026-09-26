// Package themedelivery routes detail-page theme audio through the same
// playback routing policy as video. Original theme bytes follow the direct-play
// egress policy; a conversion to AAC follows the remux execution and egress
// policy, including execution on a transcode node relayed through a proxy.
//
// A theme is not a playback session. Each authorization reserves capacity
// under a fresh "theme-" identity, signs a short-lived worker token bound to
// the chosen nodes, and lets the reservation and any stored recipe expire on
// their own.
package themedelivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

// ProxyPath is the worker route that serves a routed theme.
const ProxyPath = "/stream/theme/"

// IDPrefix marks theme reservations, transports and tracked worker transfers.
const IDPrefix = "theme-"

// recipeGrace keeps a transcode recipe readable slightly past its token so a
// request admitted at the token's last moment still finds its authority.
const recipeGrace = 30 * time.Second

var (
	// ErrPolicyUnsatisfied means the routing policy admits no route for this
	// theme, such as proxy_only with no proxy able to serve themes.
	ErrPolicyUnsatisfied = errors.New("theme audio route policy unsatisfied")
	// ErrCapacityUnavailable means legal routes exist but none could be reserved.
	ErrCapacityUnavailable = errors.New("theme audio route capacity unavailable")
)

// RecipeStore stores the transport authority a transcode node re-reads before
// it starts a conversion. *noderecipe.Store implements it.
type RecipeStore interface {
	Enabled() bool
	PutTTL(ctx context.Context, sessionID string, card playback.RecipeCard, ttl time.Duration) error
}

type reservationReleaser interface {
	ReleaseSession(sessionID string)
}

// Router resolves where a theme is served. The zero value serves everything
// from the calling API node, subject to the routing policy.
type Router struct {
	// Planner reserves proxy and transcode nodes; nil means no worker pool.
	Planner nodepool.RoutePlanner
	// Secret returns the stream-token secret workers verify with. Proxy routes
	// are illegal without one, as they are for video.
	Secret func() string
	// Recipes stores transcode-node conversion authority. Without an enabled
	// store the transcode-executed route is never chosen.
	Recipes RecipeStore
	// Policy returns the current playback routing policy.
	Policy func() config.PlaybackRoutingPolicy
	// LocalConversion reports whether this API node can run the AAC recipe.
	// Without it the API-executed conversion route is never chosen.
	LocalConversion func(context.Context) bool
	Now             func() time.Time
}

// Request describes one theme authorization. File is already selected under
// the viewer's current access filter.
type Request struct {
	File       themesongs.File
	Delivery   themesongs.Delivery
	Conversion themesongs.Conversion
	UserID     int
	ProfileID  string
	AccessPath netaccess.Path
	// ExpiresAt bounds the worker token; see themesongs.Expiry.
	ExpiresAt time.Time
	// SeekSeconds starts a conversion part way in. Original bytes seek with
	// ranges instead.
	SeekSeconds float64
}

// Result is the chosen route. URL is empty when this API node serves the
// theme itself; otherwise it is the worker URL to hand or redirect the client
// to. Release returns an unused reservation early, for example after a HEAD
// probe; an unreleased reservation expires with the next node health report.
type Result struct {
	Shape   noderouting.Shape
	URL     string
	Release func()
}

// Local reports whether the calling API node serves the theme.
func (r Result) Local() bool { return r.URL == "" }

// Resolve chooses a route for req under the current policy and, for a worker
// route, publishes the worker URL. A route that cannot be published, such as
// a proxy with no origin on the client's access path, releases its
// reservation and yields to the next legal route.
func (r *Router) Resolve(ctx context.Context, req Request) (Result, error) {
	if req.File.ID == "" || req.File.Path == "" {
		return Result{}, themesongs.ErrNotFound
	}
	converted := req.Delivery == themesongs.DeliveryConverted
	id := IDPrefix + uuid.NewString()
	secret := ""
	if r.Secret != nil {
		secret = strings.TrimSpace(r.Secret())
	}
	policy := config.DefaultPlaybackRoutingPolicy()
	if r.Policy != nil {
		policy = config.EffectivePlaybackRoutingPolicy(r.Policy())
	}
	request := noderouting.Request{Workload: noderouting.WorkloadDirectPlay, Delivery: noderouting.DeliveryDirect, Policy: policy, ProxyAllowed: secret != ""}
	bitrate := req.File.BitrateKbps
	excluded := map[string]struct{}{}
	if converted {
		request.Workload, request.Delivery = noderouting.WorkloadRemux, noderouting.DeliveryProgressiveRemux
		bitrate = req.Conversion.BitrateKbps
		if r.LocalConversion == nil || !r.LocalConversion(ctx) {
			excluded[noderouting.ShapeProgressiveRemuxAPI] = struct{}{}
		}
		if r.Recipes == nil || !r.Recipes.Enabled() {
			excluded[noderouting.ShapeProgressiveRemuxTranscodeProxy] = struct{}{}
		}
	}
	caps := &capabilityCache{infos: map[*nodepool.Node]playback.HWAccelInfo{}}
	// Predicates run under the planner lock, which the planner reserves for
	// cheap lookups: parse every stored report before resolving.
	caps.warm(r.Planner)
	proxyEligible := nodepool.ClientReachableVia(req.AccessPath, func(n *nodepool.Node) bool {
		return caps.has(n, playback.TransportFeatureThemeAudioEgressV1) &&
			(!converted || caps.has(n, playback.TransportFeatureProgressiveRemuxRelayV1))
	})
	proxyExecutionEligible := nodepool.ClientReachableVia(req.AccessPath, func(n *nodepool.Node) bool {
		return caps.has(n, playback.TransportFeatureThemeAudioEgressV1) && caps.convertsAAC(n)
	})
	transcodeEligible := func(n *nodepool.Node) bool {
		return caps.has(n, playback.TransportFeatureThemeAudioExecutionV1) &&
			caps.has(n, playback.TransportFeatureProgressiveRemuxExecutionV1) && caps.convertsAAC(n)
	}
	for {
		decision, err := noderouting.Resolve(r.Planner, noderouting.ResolveRequest{
			Request:                request,
			SessionID:              id,
			EstimatedBitrateKbps:   bitrate,
			TranscodeEligible:      transcodeEligible,
			ProxyEligible:          proxyEligible,
			ProxyExecutionEligible: proxyExecutionEligible,
			ExcludedShapeIDs:       excluded,
		})
		if err != nil {
			return Result{}, fmt.Errorf("%w: %w", ErrPolicyUnsatisfied, err)
		}
		switch decision.Outcome {
		case noderouting.OutcomeSelected:
		case noderouting.OutcomePolicyUnsatisfied:
			return Result{}, ErrPolicyUnsatisfied
		default:
			return Result{}, ErrCapacityUnavailable
		}
		if !decision.Shape.NeedsProxyNode() {
			return Result{Shape: decision.Shape, Release: func() {}}, nil
		}
		release := func() { r.release(id) }
		location, err := r.publish(ctx, id, secret, req, decision)
		if err == nil {
			return Result{Shape: decision.Shape, URL: location, Release: release}, nil
		}
		release()
		slog.WarnContext(ctx, "theme audio route could not be published; trying the next route", "component", "themedelivery",
			"shape", decision.Shape.ID, "error", err)
		excluded[decision.Shape.ID] = struct{}{}
	}
}

func (r *Router) release(id string) {
	if releaser, ok := r.Planner.(reservationReleaser); ok {
		releaser.ReleaseSession(id)
	}
}

func (r *Router) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// publish signs the worker token for a proxy-egress route. The card is the
// single source of the routing tuple: its claims are what the transcode node
// compares against the stored authority, so the token cannot drift from it.
func (r *Router) publish(ctx context.Context, id, secret string, req Request, decision noderouting.Decision) (string, error) {
	proxy := decision.Plan.ProxyNode
	if proxy == nil || secret == "" {
		return "", errors.New("proxy transport unavailable")
	}
	base := proxy.ClientURLFor(req.AccessPath)
	if base == "" {
		return "", fmt.Errorf("proxy %d has no client origin on access path %q", proxy.ID, req.AccessPath.Provider)
	}
	now := r.now()
	ttl := req.ExpiresAt.Sub(now)
	if ttl <= 0 {
		return "", themesongs.ErrGrant
	}
	themeID, ok := themesongs.NumericID(req.File.ID)
	if !ok {
		return "", themesongs.ErrNotFound
	}
	provider := req.AccessPath.Provider
	card := playback.RecipeCard{
		SessionID:              id,
		UserID:                 req.UserID,
		ProfileID:              req.ProfileID,
		OriginalStartedAt:      now,
		InputPath:              req.File.Path,
		AudioOnly:              true,
		PlayMethod:             playback.PlayMethod(streamtoken.PlayMethodThemeDirect),
		RoutingNetworkProvider: &provider,
		RoutingWorkload:        string(decision.Shape.Workload),
		RoutingExecution:       string(decision.Shape.Execution),
		RoutingEgress:          string(decision.Shape.Egress),
		RoutingEgressNodeID:    proxy.ID,
	}
	if req.Delivery == themesongs.DeliveryConverted {
		card.PlayMethod = playback.PlayMethod(streamtoken.PlayMethodThemeAAC)
		card.TranscodeAudio = true
		card.TargetCodecAudio = themesongs.CodecAAC
		card.TargetAudioChannels = req.Conversion.Channels
		card.TargetAudioBitrateKbps = req.Conversion.BitrateKbps
		card.SourceAudioChannels = req.Conversion.SourceChannels
		switch decision.Shape.Execution {
		case noderouting.ExecutionProxy:
			card.RoutingExecutionNodeID = proxy.ID
		case noderouting.ExecutionTranscode:
			node := decision.Plan.TranscodeNode
			if node == nil {
				return "", errors.New("transcode node missing from plan")
			}
			card.TranscodeNodeURL = node.URL
			card.TranscodeTransportID = id
			card.RoutingExecutionNodeID = node.ID
		}
	}
	claims := card.ToClaims()
	// ToClaims keeps the source channel count only for the versioned surround
	// downmix recipe; the other audio targets are spelled out for every
	// conversion so every executor encodes the same output.
	claims.TargetCodecAudio = card.TargetCodecAudio
	claims.TargetAudioBitrateKbps = card.TargetAudioBitrateKbps
	if claims.TargetAudioChannels == 0 {
		claims.TargetAudioChannels = card.TargetAudioChannels
	}
	claims.ThemeID = themeID
	claims.ThemeSize = req.File.Size
	claims.ThemeModifiedUnixNano = req.File.Modified.UnixNano()
	if card.TranscodeTransportID != "" {
		if err := r.Recipes.PutTTL(ctx, id, card, ttl+recipeGrace); err != nil {
			return "", fmt.Errorf("store theme conversion authority: %w", err)
		}
	}
	token, err := streamtoken.Sign(claims, secret, ttl)
	if err != nil {
		return "", fmt.Errorf("sign theme token: %w", err)
	}
	location := nodepool.NodeEndpoint(base, ProxyPath+url.PathEscape(token))
	if req.Delivery == themesongs.DeliveryConverted && req.SeekSeconds > 0 {
		location += "?seek=" + strconv.FormatFloat(req.SeekSeconds, 'f', 3, 64)
	}
	return location, nil
}

// capabilityCache decodes each node's stored capability report once per
// resolution. Eligibility predicates run under the planner lock, so they must
// not fetch; the stored report is refreshed whenever a worker's capability hash
// changes, which includes its transport features.
type capabilityCache struct {
	mu    sync.Mutex
	infos map[*nodepool.Node]playback.HWAccelInfo
}

func (c *capabilityCache) info(n *nodepool.Node) playback.HWAccelInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if info, ok := c.infos[n]; ok {
		return info
	}
	var info playback.HWAccelInfo
	if raw := n.StoredCapabilities(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &info); err != nil {
			info = playback.HWAccelInfo{}
		}
	}
	c.infos[n] = info
	return info
}

// warm decodes every pooled node's report ahead of route planning. A planner
// that cannot list its nodes leaves the cache to fill lazily.
func (c *capabilityCache) warm(planner nodepool.RoutePlanner) {
	lister, ok := planner.(nodeLister)
	if !ok {
		return
	}
	for _, nodeURL := range lister.ProxyNodeURLs() {
		if n, found := lister.ProxyNodeByURL(nodeURL); found {
			c.info(n)
		}
	}
	for _, nodeURL := range lister.TranscodeNodeURLs() {
		if n, found := lister.TranscodeNodeByURL(nodeURL); found {
			c.info(n)
		}
	}
}

func (c *capabilityCache) has(n *nodepool.Node, feature string) bool {
	if n == nil {
		return false
	}
	for _, advertised := range c.info(n).TransportFeatures {
		if advertised == feature {
			return true
		}
	}
	return false
}

// convertsAAC matches the audio-only AAC recipe at the version this server
// encodes, as the compatibility path checks proxies for the same recipe.
func (c *capabilityCache) convertsAAC(n *nodepool.Node) bool {
	if n == nil {
		return false
	}
	for _, transformation := range c.info(n).Transformations {
		if strings.EqualFold(strings.TrimSpace(transformation.Name), playback.TransformationAudioToAACV3) &&
			strings.EqualFold(strings.TrimSpace(transformation.Executor), playback.ExecutorServerV3) &&
			strings.TrimSpace(transformation.RecipeVersion) == playback.TransformationAudioToAACRecipeVersionV3 {
			return true
		}
	}
	return false
}

// ClusterRouting reports whether themes can leave through worker nodes.
func (r *Router) ClusterRouting() bool { return r != nil && r.Planner != nil }

type nodeLister interface {
	ProxyNodeURLs() []string
	ProxyNodeByURL(string) (*nodepool.Node, bool)
	TranscodeNodeURLs() []string
	TranscodeNodeByURL(string) (*nodepool.Node, bool)
}

// CanConvert reports whether a conversion route could exist now: this node
// runs the AAC recipe, or a worker advertises theme conversion. It ignores
// policy and capacity, which only a resolution can settle.
func (r *Router) CanConvert(ctx context.Context) bool {
	if r == nil {
		return false
	}
	if r.LocalConversion != nil && r.LocalConversion(ctx) {
		return true
	}
	lister, ok := r.Planner.(nodeLister)
	if !ok {
		return false
	}
	caps := &capabilityCache{infos: map[*nodepool.Node]playback.HWAccelInfo{}}
	relay := false
	for _, nodeURL := range lister.ProxyNodeURLs() {
		n, found := lister.ProxyNodeByURL(nodeURL)
		if !found || !caps.has(n, playback.TransportFeatureThemeAudioEgressV1) {
			continue
		}
		if caps.convertsAAC(n) {
			return true
		}
		relay = relay || caps.has(n, playback.TransportFeatureProgressiveRemuxRelayV1)
	}
	// A transcode node's conversion reaches the client only through a proxy
	// that serves themes and relays progressive remux, as Resolve requires.
	if !relay || r.Recipes == nil || !r.Recipes.Enabled() {
		return false
	}
	for _, nodeURL := range lister.TranscodeNodeURLs() {
		if n, found := lister.TranscodeNodeByURL(nodeURL); found && caps.has(n, playback.TransportFeatureThemeAudioExecutionV1) &&
			caps.has(n, playback.TransportFeatureProgressiveRemuxExecutionV1) && caps.convertsAAC(n) {
			return true
		}
	}
	return false
}
