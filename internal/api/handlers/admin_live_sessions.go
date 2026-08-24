package handlers

import (
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// Evidence values for a live session row. Every session in the merged view has
// at least one, and which ones it has IS the diagnosis:
//
//	reported -> a client claims to be watching. Nothing was measured leaving.
//	measured -> bytes went out. No session manager claims them.
//	both     -> an ordinary, corroborated viewer.
const (
	evidenceReported = "reported"
	evidenceMeasured = "measured"
	evidenceBoth     = "both"
)

// sessionTelemetry is the per-row block from the merged telemetry view: what was
// actually delivered, how fast, to which addresses, and which side of the picture
// each fact came from.
type sessionTelemetry struct {
	// Evidence is the first field to read. See the constants above.
	Evidence string `json:"evidence"`
	// NoDelivery marks a session reported as PLAYING for which telemetry measured
	// no bytes at all — the #666 shape, where a dead session keeps posting
	// progress while nothing leaves the building.
	//
	// A session reported as PAUSED is deliberately not flagged: a paused client
	// stops pulling bytes, so silence is the expected shape rather than an
	// anomaly (issue #243). That is now read off two fields of one row instead of
	// reconciling two stores.
	NoDelivery bool `json:"no_delivery,omitempty"`
	// ViewerBytes is delivery at the outermost viewer edge. RelayBytes is
	// internal proxy-to-node traffic and is never cap-relevant.
	ViewerBytes int64 `json:"viewer_bytes"`
	RelayBytes  int64 `json:"relay_bytes,omitempty"`
	// BytesDegraded marks a total known to be short because a publisher dropped
	// records. Render it as a floor, not a measurement.
	BytesDegraded bool `json:"bytes_degraded,omitempty"`
	// DeliveryRateKbps is measured between two consecutive view builds and is
	// absent until a session has been seen twice. Absent means "not yet known"
	// and must not render as zero, which would read as a stalled stream.
	DeliveryRateKbps *float64   `json:"delivery_rate_kbps,omitempty"`
	LastByteAt       *time.Time `json:"last_byte_at,omitempty"`
	OpenObservations int64      `json:"open_observations"`
	RequestCount     int64      `json:"request_count,omitempty"`
	// ViewerIPs is every address that pulled bytes. More than one is not
	// automatically abuse — carrier NAT and network handoff both produce it — but
	// it is the field to look at when the count is implausible.
	ViewerIPs     []string `json:"viewer_ips,omitempty"`
	RealtimeAlive bool     `json:"realtime_alive,omitempty"`
	// IdentityConflict marks publishers disagreeing about who is watching.
	// Surfaced rather than resolved: the disagreement is the abuse signal.
	IdentityConflict bool `json:"identity_conflict,omitempty"`
	// Publishers names everyone who contributed. ViewerEdgePublishers is strictly
	// who served bytes, which is what answers "from which node?".
	Publishers           []string `json:"publishers,omitempty"`
	ViewerEdgePublishers []string `json:"viewer_edge_publishers,omitempty"`
}

// liveSessionsResponse is the envelope for GET /api/v1/admin/sessions/live.
//
// GET /admin/sessions keeps its bare-array shape for the clients already reading
// it; this is a separate endpoint because the view's completeness has to travel
// with the list. A consumer that cannot tell a complete view from a degraded one
// will read "fewer sessions" as "fewer viewers".
type liveSessionsResponse struct {
	// TelemetryEnabled false means this process has telemetry switched off, and
	// Sessions is the legacy projection with no telemetry block on any row. That
	// is the ONLY fallback path: everything else is one merged view.
	TelemetryEnabled bool `json:"telemetry_enabled"`
	// ViewAvailable is false before the first successful build. Distinguishing it
	// from "no sessions" matters: an empty list from an unavailable view means
	// "not known yet", never "nothing is streaming".
	ViewAvailable     bool     `json:"view_available"`
	ViewComplete      bool     `json:"view_complete"`
	ViewStale         bool     `json:"view_stale"`
	ViewAgeMS         int64    `json:"view_age_ms"`
	IncompleteReasons []string `json:"incomplete_reasons,omitempty"`
	// NoDeliveryCount is how many rows are reported-as-playing with no measured
	// bytes. Always reported, whether or not those rows are included, so the UI
	// can offer to reveal them.
	NoDeliveryCount int                  `json:"no_delivery_count"`
	NoDeliveryShown bool                 `json:"no_delivery_shown"`
	Sessions        []playbackSessionRow `json:"sessions"`
}

// HandleListLiveSessions handles GET /api/v1/admin/sessions/live.
//
// The MERGED TELEMETRY VIEW IS THE SPINE. Every process publishes into it — the
// five measuring route families, and each API process's session manager as a
// reporting publisher — so the view already holds every session anybody knows
// about, corroborated or not. This handler walks that view and decorates each
// session with the display fields Postgres owns.
//
// It is deliberately not the other way round. Reading the legacy projection and
// filtering it against telemetry means reconciling two sets at read time, and
// that is where the ghosts, the route-family coverage gaps and the paused-session
// special cases all came from. Telemetry is not canonical for titles, posters,
// positions or codecs and never will be, so those are looked up per session —
// but which sessions exist is one question with one answer.
//
// Query parameters:
//
//	include_idle=true  keep rows reported as playing that have delivered nothing.
//	                   Default false.
func (h *AdminHandler) HandleListLiveSessions(w http.ResponseWriter, r *http.Request) {
	includeIdle := parseBoolFormValue(r.URL.Query().Get("include_idle"))
	response := liveSessionsResponse{Sessions: []playbackSessionRow{}, NoDeliveryShown: includeIdle}

	if h.StreamTelemetryViewCache == nil || h.StreamTelemetry == nil || !h.StreamTelemetry.Enabled() {
		// Telemetry is switched off, so there is no merged view to be the spine
		// and the legacy projection is all there is. Rows carry no telemetry
		// block, which is how a client tells this apart from a measured answer.
		rows, err := h.loadPlaybackSessions(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list sessions")
			return
		}
		response.Sessions = rows
		sortPlaybackSessionRows(response.Sessions)
		writeJSON(w, http.StatusOK, response)
		return
	}
	response.TelemetryEnabled = true

	snapshot, view, status := h.StreamTelemetryViewCache.Live(r.Context())
	response.ViewAvailable = status.Available
	response.ViewComplete = view.Complete
	response.ViewStale = status.Stale
	response.ViewAgeMS = status.Age.Milliseconds()
	response.IncompleteReasons = view.IncompleteReasons

	if !status.Available {
		// Before the first build there is no view to walk. Serving the legacy set
		// is right; serving an empty list would read as "nothing is streaming".
		rows, err := h.loadPlaybackSessions(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list sessions")
			return
		}
		response.Sessions = rows
		sortPlaybackSessionRows(response.Sessions)
		writeJSON(w, http.StatusOK, response)
		return
	}

	// Look the display fields up BY the view's session ids rather than taking the
	// newest page and hoping it covers them: the view is the spine and can hold
	// far more sessions than that page, which would have returned rows with no
	// title, poster or position for everything past the cut.
	//
	// Postgres is decoration here, not the answer. If it is unavailable the view
	// still knows who is streaming, so the list degrades to identity-only rather
	// than failing - hiding every live viewer because a display join was down
	// would defeat the point of measuring them.
	sessionIDs := make([]string, 0, len(snapshot.Facts))
	for sessionID := range snapshot.Facts {
		sessionIDs = append(sessionIDs, sessionID)
	}
	rows, err := h.loadPlaybackSessionsByID(r.Context(), r, sessionIDs)
	if err != nil {
		slog.WarnContext(r.Context(), "live sessions: display lookup failed; serving telemetry identity only",
			"error", err)
		rows = nil
	}

	// An incomplete view is blindness, not disagreement. The publisher holding a
	// session's bytes may be exactly the one that is missing, so "reported with no
	// measured bytes" stops being evidence of anything - classifying on it would
	// flag healthy viewers, and hiding on it would remove them from the operator's
	// screen during the rolling deploy that caused it.
	response.Sessions, response.NoDeliveryCount = decorateLiveSessions(
		snapshot, rows, includeIdle, view.Complete, time.Now())
	writeJSON(w, http.StatusOK, response)
}

// noDeliveryGrace is how long a reported session may go unmeasured before that
// counts as evidence of anything.
//
// A session exists the moment /playback/start returns, and is reported on the
// next publisher tick — but nothing has ASKED for a byte yet. Measuring it then
// takes a client request, a sweep and a merge, so with default settings a
// perfectly healthy start reads as reported-with-no-bytes for on the order of ten
// seconds. Without this window every play would be classified no_delivery and
// hidden from the default list for its first moments, and no_delivery_count —
// the number an operator reads to size the ghost problem — would count ordinary
// starts.
//
// The ghosts this endpoint exists to surface ran for hours (the 48h soak's worst
// case was 24.1h of wall clock against 0.16h played), so a window this short
// costs nothing in detection and removes the whole false-positive class.
const noDeliveryGrace = 30 * time.Second

// decorateLiveSessions walks the merged view and attaches the display fields
// Postgres owns, returning the rows to serve and how many were held back. at is
// the reading instant, against which a session's age is measured.
func decorateLiveSessions(
	snapshot streamtelemetry.LiveSnapshot, rows []playbackSessionRow, includeIdle, viewComplete bool,
	at time.Time,
) ([]playbackSessionRow, int) {
	display := make(map[string]playbackSessionRow, len(rows))
	for _, row := range rows {
		display[row.SessionID] = row
	}

	sessions := make([]playbackSessionRow, 0, len(snapshot.Facts))
	noDelivery := 0
	for _, facts := range snapshot.Facts {
		row, known := display[facts.SessionID]
		if !known {
			// In the view but not in Postgres. Usually a session that ended
			// between the two reads; when it persists it is delivery nobody has
			// claimed, which is exactly the case worth seeing rather than
			// dropping. Carry what the view knows.
			row = rowFromTelemetry(facts)
		}
		row.Telemetry = newSessionTelemetry(facts)
		if !viewComplete {
			// Cannot be told apart from a publisher we simply cannot see.
			row.Telemetry.NoDelivery = false
		}
		// Too young to have been measured yet. A zero StartedAt is left flagged:
		// its age is unknown, and suppressing on an unknown age would give any
		// session whose start instant never reached the view permanent immunity
		// from the classification.
		if !facts.StartedAt.IsZero() && at.Sub(facts.StartedAt) < noDeliveryGrace {
			row.Telemetry.NoDelivery = false
		}
		if row.Telemetry.NoDelivery {
			noDelivery++
			if !includeIdle {
				continue
			}
		}
		sessions = append(sessions, row)
	}
	sortPlaybackSessionRows(sessions)
	return sessions, noDelivery
}

// rowFromTelemetry builds the display row for a session Postgres has no record
// of, from what the merged view knows. Every field telemetry is not canonical
// for is left empty rather than guessed.
func rowFromTelemetry(facts streamtelemetry.LiveByteFacts) playbackSessionRow {
	row := playbackSessionRow{
		SessionID:   facts.SessionID,
		ProfileID:   facts.ProfileID,
		MediaFileID: facts.MediaFileID,
		PlayMethod:  facts.PlayMethod,
		StartedAt:   facts.StartedAt,
	}
	if facts.Subject.Kind == streamtelemetry.SubjectUser {
		if userID, convErr := strconv.Atoi(facts.Subject.ID); convErr == nil {
			row.UserID = userID
		}
	}
	return row
}

func newSessionTelemetry(facts streamtelemetry.LiveByteFacts) *sessionTelemetry {
	block := &sessionTelemetry{
		Evidence: evidenceMeasured, ViewerBytes: facts.ViewerBytes, RelayBytes: facts.RelayBytes,
		BytesDegraded: facts.BytesDegraded, OpenObservations: facts.OpenObservations,
		RequestCount: facts.RequestCount, ViewerIPs: facts.ViewerIPs,
		RealtimeAlive: facts.RealtimeAlive, IdentityConflict: facts.IdentityConflict,
		Publishers: facts.Publishers, ViewerEdgePublishers: facts.ViewerEdgePublishers,
	}
	switch {
	case facts.Reported && facts.ViewerBytes > 0:
		block.Evidence = evidenceBoth
	case facts.Reported:
		block.Evidence = evidenceReported
		// Reported as playing, nothing measured. A paused client is expected to
		// go quiet, so only an unpaused one is an anomaly.
		block.NoDelivery = !facts.ReportedPaused
	}
	if facts.RateAvailable {
		rate := facts.DeliveryRateKbps
		block.DeliveryRateKbps = &rate
	}
	if !facts.LastByteAt.IsZero() {
		at := facts.LastByteAt.UTC()
		block.LastByteAt = &at
	}
	return block
}

// sortPlaybackSessionRows orders newest first, matching the dashboard's existing
// reading order, with the session id breaking ties so the order is stable across
// reads rather than following map iteration.
func sortPlaybackSessionRows(rows []playbackSessionRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].StartedAt.Equal(rows[j].StartedAt) {
			return rows[i].StartedAt.After(rows[j].StartedAt)
		}
		return rows[i].SessionID < rows[j].SessionID
	})
}
