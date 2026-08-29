package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// parityScanLimit bounds each legacy read. It sits above the merged-session cap
// so a normal fleet is never truncated, while a runaway table still cannot blow
// the handler up.
const parityScanLimit = 60_000

// StreamTelemetryParityHandler serves P0d's admin parity projection: the merged
// stream-telemetry view beside both legacy live-session projections, and the
// diff between them.
//
// It compares; it does not cut over. The design puts the repoint after parity is
// demonstrated, and legacy retirement is its own project — see the design note
// for why the admin session payload is a join rather than a swap.
type StreamTelemetryParityHandler struct {
	Registry  *streamtelemetry.Registry
	ViewCache *streamtelemetry.ViewCache
	Pool      *pgxpool.Pool
	Redis     *redis.Client
}

type parityViewResponse struct {
	Available          bool     `json:"available"`
	BuiltAt            string   `json:"built_at,omitempty"`
	AgeMS              int64    `json:"age_ms"`
	Stale              bool     `json:"stale"`
	BuildTookMS        int64    `json:"build_took_ms"`
	Refreshes          int64    `json:"refreshes"`
	Failures           int64    `json:"failures"`
	LastError          string   `json:"last_error,omitempty"`
	Complete           bool     `json:"complete"`
	IncompleteReasons  []string `json:"incomplete_reasons"`
	MissingPublishers  []string `json:"missing_publishers"`
	ClockSkewSuspected bool     `json:"clock_skew_suspected"`
	Publishers         []string `json:"publishers"`
	SessionCount       int      `json:"session_count"`
	TransferCount      int      `json:"transfer_count"`
}

type paritySourceResponse struct {
	Source    string                        `json:"source"`
	Available bool                          `json:"available"`
	Error     string                        `json:"error,omitempty"`
	Notes     []string                      `json:"notes,omitempty"`
	Report    *streamtelemetry.ParityReport `json:"report,omitempty"`
}

type parityResponse struct {
	Enabled bool                   `json:"enabled"`
	Reason  string                 `json:"reason,omitempty"`
	View    parityViewResponse     `json:"view"`
	Sources []paritySourceResponse `json:"sources"`
}

// HandleGetStreamTelemetryParity handles
// GET /api/v1/admin/stream-telemetry/parity.
func (h *StreamTelemetryParityHandler) HandleGetStreamTelemetryParity(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Registry == nil || !h.Registry.Enabled() {
		// The honest answer is "there is nothing to compare", not an empty
		// report that reads as agreement.
		writeJSON(w, http.StatusOK, parityResponse{
			Reason:  "stream telemetry is disabled on this process",
			Sources: []paritySourceResponse{},
		})
		return
	}

	ctx := r.Context()
	view, status := h.ViewCache.View(ctx)
	response := parityResponse{Enabled: true, View: describeView(view, status), Sources: []paritySourceResponse{}}
	if !status.Available {
		response.Reason = "the global view has not been built yet"
		writeJSON(w, http.StatusOK, response)
		return
	}

	telemetry := streamtelemetry.LiveSessionsFromGlobalView(view)
	response.Sources = append(response.Sources,
		h.comparePostgres(ctx, telemetry),
		h.compareNodeSessions(ctx, telemetry),
	)
	writeJSON(w, http.StatusOK, response)
}

// describeView surfaces the completeness flag alongside the diff on purpose. A
// degraded view is missing sessions by construction, so a parity report built on
// one is not evidence of disagreement — it is evidence of blindness, and P0c
// built the flag precisely so the two cannot be confused.
func describeView(view streamtelemetry.GlobalMonitoringView, status streamtelemetry.ViewCacheStatus) parityViewResponse {
	response := parityViewResponse{
		Available: status.Available, AgeMS: status.Age.Milliseconds(), Stale: status.Stale,
		BuildTookMS: status.BuildTook.Milliseconds(), Refreshes: status.Refreshes,
		Failures: status.Failures, LastError: status.LastError,
		Complete: view.Complete, ClockSkewSuspected: view.ClockSkewSuspected,
		IncompleteReasons: view.IncompleteReasons, MissingPublishers: []string{},
		Publishers: []string{}, SessionCount: len(view.Sessions), TransferCount: len(view.Transfers),
	}
	if response.IncompleteReasons == nil {
		response.IncompleteReasons = []string{}
	}
	if !view.BuiltAt.IsZero() {
		response.BuiltAt = view.BuiltAt.UTC().Format(time.RFC3339Nano)
	}
	for _, publisher := range view.MissingPublishers {
		response.MissingPublishers = append(response.MissingPublishers, publisher.PublisherID)
	}
	for _, publisher := range view.Publishers {
		response.Publishers = append(response.Publishers, publisher.PublisherID+"="+string(publisher.State))
	}
	return response
}

func (h *StreamTelemetryParityHandler) comparePostgres(ctx context.Context, telemetry []streamtelemetry.LiveSession) paritySourceResponse {
	const source = "playback_sessions_sync"
	if h.Pool == nil {
		return paritySourceResponse{Source: source, Error: "database not configured"}
	}
	// Only the parity columns: the enriched admin query joins media, series and
	// episode metadata telemetry cannot express, so reading it here would cost
	// far more and compare nothing extra.
	rows, err := h.Pool.Query(ctx, `
		SELECT session_id, user_id, COALESCE(profile_id, ''), COALESCE(media_file_id, 0),
		       COALESCE(play_method, ''), COALESCE(reporting_node, ''), started_at
		FROM playback_sessions_sync
		LIMIT $1`, parityScanLimit)
	if err != nil {
		return paritySourceResponse{Source: source, Error: err.Error()}
	}
	defer rows.Close()

	legacy := make([]streamtelemetry.LiveSession, 0)
	for rows.Next() {
		var (
			sessionID, profileID, playMethod, node string
			userID, mediaFileID                    int
			startedAt                              time.Time
		)
		if err := rows.Scan(&sessionID, &userID, &profileID, &mediaFileID, &playMethod, &node, &startedAt); err != nil {
			return paritySourceResponse{Source: source, Error: err.Error()}
		}
		session := streamtelemetry.LiveSession{
			SessionID: sessionID, ProfileID: profileID, MediaFileID: mediaFileID,
			PlayMethod: playMethod, Node: node, StartedAt: startedAt,
		}
		if userID > 0 {
			session.Subject = streamtelemetry.UserSubject(userID)
		}
		legacy = append(legacy, session)
	}
	if err := rows.Err(); err != nil {
		return paritySourceResponse{Source: source, Error: err.Error()}
	}

	report := streamtelemetry.CompareLiveSessions(source, telemetry, legacy, streamtelemetry.DefaultParityLimit)
	return paritySourceResponse{Source: source, Available: true, Report: &report}
}

func (h *StreamTelemetryParityHandler) compareNodeSessions(ctx context.Context, telemetry []streamtelemetry.LiveSession) paritySourceResponse {
	const source = "node_sessions_redis"
	if h.Redis == nil {
		return paritySourceResponse{Source: source, Error: "redis not configured"}
	}
	result, err := nodesessions.ListAll(ctx, h.Redis, parityScanLimit)
	if err != nil {
		return paritySourceResponse{Source: source, Error: err.Error()}
	}

	legacy := make([]streamtelemetry.LiveSession, 0, len(result.Sessions))
	for _, info := range result.Sessions {
		session := streamtelemetry.LiveSession{
			SessionID: info.SessionID, ProfileID: info.ProfileID,
			MediaFileID: info.MediaFileID, Node: info.NodeName,
		}
		if info.AuthUserID > 0 {
			session.Subject = streamtelemetry.UserSubject(info.AuthUserID)
		}
		// The node record carries both a formatted timestamp and the immutable
		// nanosecond stamp P0a added. Prefer the nanos: the formatted value is
		// second-resolution and is what a mixed-version node may not have.
		if info.StartedAtUnixNano > 0 {
			session.StartedAt = time.Unix(0, info.StartedAtUnixNano)
		} else if parsed, parseErr := time.Parse(time.RFC3339, info.StartedAt); parseErr == nil {
			session.StartedAt = parsed
		}
		legacy = append(legacy, session)
	}

	response := paritySourceResponse{Source: source, Available: true}
	if result.Undecodable > 0 {
		response.Notes = append(response.Notes, "undecodable records skipped")
	}
	if result.Truncated {
		// Never let a capped read pass as a complete one.
		response.Notes = append(response.Notes, "scan truncated at the record limit; the report is partial")
	}
	report := streamtelemetry.CompareLiveSessions(source, telemetry, legacy, streamtelemetry.DefaultParityLimit)
	response.Report = &report
	return response
}
