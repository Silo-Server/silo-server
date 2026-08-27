package streamtelemetry

import (
	"sort"
	"strings"
	"time"
)

// LiveByteFacts is the per-session truth telemetry is canonical for: what was
// actually delivered, how fast, and to which addresses.
//
// It carries no display metadata on purpose. Telemetry never learns a title, a
// poster or a playback position, and §6/P0d records the admin session payload as
// a join rather than a swap — these facts decorate a legacy row, they do not
// replace one.
type LiveByteFacts struct {
	SessionID string
	// Identity, carried so a consumer can render a session Postgres has no row
	// for — delivery nobody has claimed is exactly the case worth seeing.
	Subject     Subject
	ProfileID   string
	MediaFileID int
	PlayMethod  string
	StartedAt   time.Time
	// Reported is true when a playback session manager published this session.
	// With ViewerBytes it is the whole diagnosis: reported with no bytes is a
	// client claiming to watch something unsent, bytes without reported is
	// delivery nobody claimed, and both together is an ordinary viewer.
	Reported                bool
	ReportedPaused          bool
	ReportedPositionSeconds float64
	// ViewerBytes is bytes delivered to the viewer at the outermost edge.
	// RelayBytes is internal proxy→node traffic and is never cap-relevant.
	ViewerBytes   int64
	RelayBytes    int64
	BytesDegraded bool
	// DeliveryRateKbps is measured across two consecutive view builds, so it is
	// absent (RateAvailable false) on the first sighting of a session and
	// whenever the builds were too close together to divide by. An absent rate
	// must render as "not yet known", never as zero.
	DeliveryRateKbps float64
	RateAvailable    bool
	LastByteAt       time.Time
	OpenObservations int64
	RequestCount     int64
	ViewerIPs        []string
	RealtimeAlive    bool
	// IdentityConflict marks a session whose publishers disagreed about who is
	// watching. Per the identity-disagreement rule this is an abuse signal, so it
	// is surfaced rather than resolved.
	IdentityConflict bool
	// Publishers is everyone who contributed. ViewerEdgePublishers is strictly
	// who served viewer bytes, which is what answers "from which node?".
	Publishers           []string
	ViewerEdgePublishers []string
}

// LiveByteFactsFromGlobalView projects the merged view into per-session byte
// facts, keyed by session id.
func LiveByteFactsFromGlobalView(view GlobalMonitoringView) LiveSnapshot {
	facts := make(LiveSnapshot, len(view.Sessions))
	for _, session := range view.Sessions {
		ips := append([]string(nil), session.ViewerIPs...)
		sort.Strings(ips)
		playMethod := ""
		if len(session.PlayMethods) == 1 {
			// A merged scalar play method is deliberately absent when publishers
			// disagree (§2.5); a single unioned value is the only unambiguous one.
			playMethod = session.PlayMethods[0]
		}
		facts[session.SessionID] = LiveByteFacts{
			SessionID:               session.SessionID,
			Subject:                 session.Subject,
			ProfileID:               session.ProfileID,
			MediaFileID:             session.MediaFileID,
			PlayMethod:              playMethod,
			StartedAt:               session.StartedAt,
			Reported:                session.Reported,
			ReportedPaused:          session.ReportedPaused,
			ReportedPositionSeconds: session.ReportedPositionSeconds,
			ViewerBytes:             session.ViewerBytesAccepted,
			RelayBytes:              session.RelayBytesAccepted,
			BytesDegraded:           session.BytesDegraded,
			LastByteAt:              session.LastByteAccepted,
			OpenObservations:        session.OpenObservations,
			RequestCount:            session.RequestCount,
			ViewerIPs:               ips,
			RealtimeAlive:           session.RealtimeConnectionAlive,
			IdentityConflict:        session.HasIdentityConflict,
			Publishers:              publisherIDs(session.Publishers),
			ViewerEdgePublishers:    publisherIDs(session.ViewerEdgePublishers),
		}
	}
	return facts
}

// LiveSnapshot is a view build projected into per-session byte facts, keyed by
// session id.
//
// There is no "is this authoritative?" flag any more, and its absence is the
// point. It existed when telemetry saw only measured families, so a session
// missing from the view could mean either "no bytes flowed" or "nobody was
// looking" — and a consumer had to be told which. With every API process
// publishing its session manager as a reporting publisher, the view holds every
// session anyone knows about, so an absent session means absent. Completeness is
// still reported, but it now qualifies the FACTS on a row rather than deciding
// whether the list may be trusted at all.
//
// That is also why this is the map itself rather than a struct wrapping it: the
// second field it existed to carry is gone, and every caller immediately
// unwrapped the one that was left.
type LiveSnapshot map[string]LiveByteFacts

// publisherIDs flattens publisher refs for a consumer that only needs the names.
func publisherIDs(refs []PublisherRef) []string {
	if len(refs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.PublisherID)
	}
	sort.Strings(ids)
	return ids
}

// minimumRateInterval is the shortest gap between two view builds that yields a
// usable rate. Two builds a few milliseconds apart divide a small byte delta by a
// near-zero interval and produce a meaningless spike, so the previous sample is
// carried instead.
const minimumRateInterval = time.Second

// rateSample is the per-session bookkeeping behind DeliveryRateKbps.
type rateSample struct {
	bytes int64
	at    time.Time
	kbps  float64
	known bool
	// contributors fingerprints which publishers the byte total was summed over.
	// A rate is a delta, so it is only meaningful between two totals summed over
	// the SAME set: a publisher rejoining after a restart brings its historical
	// bytes with it, and the resulting level jump would otherwise be reported as
	// traffic delivered inside one cache interval.
	contributors string
}

// updateRatesLocked folds a freshly built view into the rate table and returns
// nothing; c.rates is the state DeliveryRateKbps reads. It must be called with
// c.mu held.
//
// The table is rebuilt from the new view on every build rather than pruned, so it
// cannot outgrow the merged-session cap or retain a session that has ended.
func (c *ViewCache) updateRatesLocked(view GlobalMonitoringView, at time.Time) {
	next := make(map[string]rateSample, len(view.Sessions))
	for _, session := range view.Sessions {
		bytes := session.ViewerBytesAccepted
		// Fingerprint the byte SOURCES, not every contributor. ViewerBytesAccepted
		// sums viewer-egress routes only, so a reporting publisher contributes
		// nothing to the total while still changing a Publishers-derived key —
		// which would reset the rate every time the session manager starts or
		// stops reporting a session that is streaming normally.
		contributors := contributorKey(publisherIDs(session.ViewerEdgePublishers))
		sample := rateSample{bytes: bytes, at: at, contributors: contributors}
		previous, seen := c.rates[session.SessionID]
		switch {
		case !seen:
			// First sighting: no interval to divide by yet.
		case previous.contributors != contributors:
			// A publisher joined or left, so the two totals are not comparable.
			// Restart rather than attribute the level change to this interval.
		case at.Sub(previous.at) < minimumRateInterval:
			// Too close together to divide; carry the last usable answer.
			sample.at = previous.at
			sample.bytes = previous.bytes
			sample.kbps = previous.kbps
			sample.known = previous.known
			sample.contributors = previous.contributors
		default:
			delta := bytes - previous.bytes
			if delta < 0 {
				// Bytes are monotonic per session; a decrease means the session
				// was re-keyed or a publisher dropped out. Restart rather than
				// report a negative rate.
				break
			}
			sample.kbps = float64(delta) * 8 / 1000 / at.Sub(previous.at).Seconds()
			sample.known = true
		}
		next[session.SessionID] = sample
	}
	c.rates = next
}

// contributorKey fingerprints a session's publisher set. Publishers arrive
// sorted from the merge, so joining them is stable.
func contributorKey(publishers []string) string {
	return strings.Join(publishers, ",")
}
