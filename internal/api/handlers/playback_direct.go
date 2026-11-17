package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/directconn"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
)

// directPolicyDirectOnlyV3 is the connection path of a device that takes media
// over the direct path or its local route and never over a regular origin.
const directPolicyDirectOnlyV3 = "direct_only"

// mediaHostV3 is the host that serves a plan's media bytes: the API server or
// a media proxy.
type mediaHostV3 uint8

const (
	mediaHostAPI mediaHostV3 = iota
	mediaHostProxy
)

type directRequestKeyV3 struct{}

// directRequestV3 is one native playback request's direct-transport state: the
// transport the request asked for and the device's enrollment, read once before
// planning. Everything the planner does with it afterwards is a field read, so
// it is safe under the planner lock.
type directRequestV3 struct {
	requested string
	enrolled  bool
	device    directconn.DeviceDirect
	// unreadable is set when the device's state could not be read. Whether the
	// device is direct_only is then unknown, so the request leaves every proxy
	// out and the API host serves its media.
	unreadable bool
}

func directRequestFromContext(ctx context.Context) *directRequestV3 {
	d, _ := ctx.Value(directRequestKeyV3{}).(*directRequestV3)
	return d
}

// media is the state the proxy predicates and URL builders take. A nil request
// is a device with no direct enrollment. A device whose state is unreadable
// is treated as direct_only: it never gets a regular proxy URL.
func (d *directRequestV3) media() nodepool.DirectMedia {
	if d == nil {
		return nodepool.DirectMedia{}
	}
	return nodepool.DirectMedia{
		Requested:  d.requested == playback.MediaTransportDirectV3,
		DirectOnly: d.unreadable || (d.enrolled && d.device.Policy == directPolicyDirectOnlyV3),
	}
}

// apiDirect reports whether the API host serves this request's media over the
// direct path: the request asked for direct and the API host's path to the
// device is live.
func (d *directRequestV3) apiDirect() bool {
	return d != nil && d.requested == playback.MediaTransportDirectV3 && d.enrolled && d.device.APIReady
}

// directMediaFromContext is the proxy predicate and builder input for the
// request on ctx.
func directMediaFromContext(ctx context.Context) nodepool.DirectMedia {
	return directRequestFromContext(ctx).media()
}

// loadDirectDeviceV3 reads one device's direct state with one call. A device
// that is not enrolled, and every device while direct connections are off or no
// DirectState is wired, answers ok=false and plans as a regular one. A state
// that cannot be read answers ok=false with the error.
func (h *PlaybackHandler) loadDirectDeviceV3(ctx context.Context, userID int, device string) (directconn.DeviceDirect, bool, error) {
	if device == "" || userID <= 0 || h.DirectState == nil {
		return directconn.DeviceDirect{}, false, nil
	}
	state, ok, err := h.DirectState.Device(ctx, userID, device)
	if err != nil {
		slog.WarnContext(ctx, "direct connection state unavailable; the API host serves the media", logComponentKey, playbackLogValueV3, "error", err)
		return directconn.DeviceDirect{}, false, err //nolint:wrapcheck // Callers only test it.
	}
	return state, ok, nil
}

// newDirectRequestV3 reads the device's direct state once for a request that
// asked for the given transport. With read false it reads nothing: the request
// reuses a transport whose attribution the session already holds.
func (h *PlaybackHandler) newDirectRequestV3(ctx context.Context, userID int, device, requested string, read bool) *directRequestV3 {
	if requested != playback.MediaTransportDirectV3 {
		requested = playback.MediaTransportRegularV3
	}
	d := &directRequestV3{requested: requested}
	if read {
		var err error
		d.device, d.enrolled, err = h.loadDirectDeviceV3(ctx, userID, connectionDeviceV3(device))
		d.unreadable = err != nil
	}
	return d
}

// withDirectRequestV3 records the request's direct state on ctx. The device is
// the one the caller named in X-Silo-Device-Id. With read false the state is
// not read, for a replan that reuses its transport.
func (h *PlaybackHandler) withDirectRequestV3(ctx context.Context, userID int, requested string, read bool) context.Context {
	device, _ := ctx.Value(playbackConnectionDeviceContextKey{}).(string)
	return context.WithValue(ctx, directRequestKeyV3{}, h.newDirectRequestV3(ctx, userID, device, requested, read))
}

// withDirectProbeV3 is withDirectRequestV3 for the bandwidth probe, which
// names its device and transport itself.
func (h *PlaybackHandler) withDirectProbeV3(ctx context.Context, userID int, device, requested string) context.Context {
	return context.WithValue(ctx, directRequestKeyV3{}, h.newDirectRequestV3(ctx, userID, device, requested, true))
}

// connectionKeyV3 is key(host) for the request on ctx: the device's direct key
// when the host is the API host and the request is assigned direct, and the
// regular key for every other host. The regular key never reads the direct
// path.
func (h *PlaybackHandler) connectionKeyV3(ctx context.Context, host mediaHostV3) string {
	if d := directRequestFromContext(ctx); host == mediaHostAPI && d.apiDirect() {
		key, _ := connectionstore.Key(ctx, h.JWTSecret, connectionstore.ClassDirect, d.device.ConnectionKey)
		return key
	}
	key, _ := connectionstore.Key(ctx, h.JWTSecret, connectionstore.ClassRegular, "")
	return key
}

// attributedConnectionKeyV3 is the key a session's readings belong to. A plan
// on the direct path reads under the API path record's key, and only while the
// record's generation is the one the plan stored: a reading from an earlier
// path belongs to a connection that no longer exists, so it has no key. Every
// other plan reads under the regular key of this request.
func (h *PlaybackHandler) attributedConnectionKeyV3(ctx context.Context, d *directRequestV3, transport string, generation uint64) string {
	if transport != playback.MediaTransportDirectV3 {
		key, _ := connectionstore.Key(ctx, h.JWTSecret, connectionstore.ClassRegular, "")
		return key
	}
	if d == nil || !d.enrolled || d.device.ConnectionKey == "" || d.device.PathGeneration == 0 || d.device.PathGeneration != generation {
		return ""
	}
	key, _ := connectionstore.Key(ctx, h.JWTSecret, connectionstore.ClassDirect, d.device.ConnectionKey)
	return key
}

// sessionConnectionKeyV3 is the key the readings of the live session belong to,
// for the request on ctx. A session this replica does not hold has no key.
func (h *PlaybackHandler) sessionConnectionKeyV3(ctx context.Context, sessionID string) string {
	session, err := h.sessionMgr.GetSession(sessionID)
	if err != nil {
		return ""
	}
	return h.attributedConnectionKeyV3(ctx, directRequestFromContext(ctx), session.MediaTransport, session.PathGeneration)
}

// sampleConnectionKeyV3 is the key a plan summary's throughput sample belongs
// to. Without a direct state every plan is regular, so the request's regular
// key stands. Otherwise the session's attribution decides: the live session
// when this replica holds it, and else the stored plan, which says only
// whether the plan was direct; a direct plan with no live session to compare
// generations against has no key.
func (h *PlaybackHandler) sampleConnectionKeyV3(ctx context.Context, userID int, device, sessionID string) string {
	regular := func() string {
		key, _ := connectionstore.Key(ctx, h.JWTSecret, connectionstore.ClassRegular, "")
		return key
	}
	if h.DirectState == nil || sessionID == "" {
		return regular()
	}
	var transport string
	var generation uint64
	if session, err := h.sessionMgr.GetSession(sessionID); err == nil {
		transport, generation = session.MediaTransport, session.PathGeneration
	} else if record, err := h.PlanStoreV3.GetAttempt(ctx, sessionID); err == nil {
		transport = record.CurrentPlan.MediaTransport
	} else {
		return ""
	}
	if transport != playback.MediaTransportDirectV3 {
		return regular()
	}
	d := &directRequestV3{}
	d.device, d.enrolled, _ = h.loadDirectDeviceV3(ctx, userID, device)
	return h.attributedConnectionKeyV3(ctx, d, transport, generation)
}

// attributeTransportV3 records on the plan and the prepared transport which
// transport the committed route carries the media over: direct when the API
// host serves a request assigned direct, regular otherwise. A reused
// transport keeps the attribution of the session it belongs to. A request with
// no direct state publishes nothing.
func attributeTransportV3(ctx context.Context, plan *playback.PlanV3, transport *preparedTransportV3, reused *playback.Session) {
	d := directRequestFromContext(ctx)
	if d == nil || plan == nil {
		return
	}
	switch {
	case reused != nil:
		transport.mediaTransport, transport.pathGeneration = reused.MediaTransport, reused.PathGeneration
	case transport.routingEgress != noderouting.EgressProxy && d.apiDirect():
		transport.mediaTransport, transport.pathGeneration = playback.MediaTransportDirectV3, d.device.PathGeneration
	}
	if transport.mediaTransport == "" {
		transport.mediaTransport = playback.MediaTransportRegularV3
	}
	plan.MediaTransport = transport.mediaTransport
}

// keyPassV3 is one planning pass under one connection key.
type keyPassV3 struct {
	// read is the key whose reading and learned limit the pass plans under;
	// empty plans under none.
	read string
	// record is the key a clear or an observed rebuffer is written under, and
	// writes says whether this pass writes at all. Only the first pass writes:
	// the request's own observations are recorded once.
	record string
	writes bool
}

// pinProxyEgressV3 returns r with the egress of every workload pinned to a
// proxy, so the committed route cannot leave the host class the pass planned
// for.
func pinProxyEgressV3(h *PlaybackHandler, r *http.Request) *http.Request {
	policy := h.playbackRoutingPolicyForContextV3(r.Context())
	policy.DirectPlayEgress = config.PlaybackEgressProxyOnly
	policy.RemuxEgress = config.PlaybackEgressProxyOnly
	policy.VideoTranscodeEgress = config.PlaybackEgressProxyOnly
	return r.WithContext(withPlaybackRoutingPolicySnapshotV3(r.Context(), policy))
}

// selectMediaHostV3 names the host the route resolver would serve result's
// media from, without keeping the reservation it takes. False when no route
// resolves.
func (h *PlaybackHandler) selectMediaHostV3(r *http.Request, mode mediaAuthModeV3, result playback.PlannerResultV3) (mediaHostV3, bool) {
	if result.Plan == nil || result.Terminal != nil {
		return mediaHostAPI, false
	}
	probeID := "route-probe-" + uuid.NewString()
	if releaser, ok := h.NodePlanner.(sessionReservationReleaserV3); ok {
		defer releaser.ReleaseSession(probeID)
	}
	policy := h.playbackRoutingPolicyForContextV3(r.Context())
	var decision noderouting.Decision
	switch result.Plan.Delivery {
	case playback.DeliveryTranscodeHLSV3, playback.DeliveryRemuxHLSV3:
		proxyAllowed := mode.proxyEgress || (!mode.headerAuth && h.JWTSecret != "")
		decision = h.resolveHLSRouteWithPolicyV3(r.Context(), &playback.Session{ID: probeID}, result, policy, proxyAllowed, nil, nil)
	default:
		var routeErr *transportErrorV3
		decision, routeErr = h.resolveIdentityRouteV3(r, probeID, result, mode, policy, nil)
		if routeErr != nil {
			return mediaHostAPI, false
		}
	}
	if !decision.Selected() {
		return mediaHostAPI, false
	}
	if decision.Shape.Egress == noderouting.EgressProxy {
		return mediaHostProxy, true
	}
	return mediaHostAPI, true
}

// planUnderKeysV3 runs plan under the connection keys of the start policy: at
// most two plans beyond the first, only for a request assigned direct on the API
// host.
//
// Pass 1 plans under key(API host) and resolves the route. When the host that
// route selects has the same key, the plan stands; every regular request ends
// here, because every host then has the regular key. Otherwise pass 2 plans
// again from the original request under the selected host's key, with the
// route's egress pinned to that host class. A pass that fails ends the policy
// with that pass, so the caller fails the request. When the pinned route does
// not resolve, a last plan runs under no key at all and the route resolves as
// it would for any request. The returned request carries the pin of the plan
// that stands.
func planUnderKeysV3[T any](h *PlaybackHandler, r *http.Request, record string, plan func(keyPassV3) T, resultOf func(T) playback.PlannerResultV3, modeOf func(T) mediaAuthModeV3, failed func(T) bool) (T, *http.Request) {
	apiKey := h.connectionKeyV3(r.Context(), mediaHostAPI)
	first := plan(keyPassV3{read: apiKey, record: record, writes: true})
	proxyKey := h.connectionKeyV3(r.Context(), mediaHostProxy)
	if proxyKey == apiKey {
		return first, r
	}
	host, ok := h.selectMediaHostV3(r, modeOf(first), resultOf(first))
	if !ok || host == mediaHostAPI {
		return first, r
	}
	pinned := pinProxyEgressV3(h, r)
	second := plan(keyPassV3{read: proxyKey})
	if failed(second) {
		return second, r
	}
	if host, ok := h.selectMediaHostV3(pinned, modeOf(second), resultOf(second)); ok && host == mediaHostProxy {
		return second, pinned
	}
	return plan(keyPassV3{}), r
}
