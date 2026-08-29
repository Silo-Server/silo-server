package handlers

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/jellycompat"
)

const (
	// adminHealthProbeTimeout bounds each dependency probe. A wedged Postgres
	// or Redis must not hold the status route open: the dashboard would rather
	// be told "not ok" promptly than hang on its health strip.
	adminHealthProbeTimeout = 2 * time.Second

	adminLogLevelCountsCacheKey = "log-level-counts-24h"
	adminLogLevelCountsCacheTTL = 30 * time.Second
)

type adminServerStatusResponse struct {
	StartedAt             time.Time  `json:"started_at"`
	RestartRequired       bool       `json:"restart_required"`
	RestartRequiredAt     *time.Time `json:"restart_required_at,omitempty"`
	RestartRequiredReason string     `json:"restart_required_reason,omitempty"`
	// RestartRequiredReasons accumulates every distinct reason marked since
	// boot ("setting:<key>" entries for settings saves), so a client can scope
	// a pending restart to the subsystem it belongs to. The singular field
	// above only remembers the last save.
	RestartRequiredReasons []string `json:"restart_required_reasons,omitempty"`
	// RestartMarkCount increments on every restart-required save. The boolean
	// above latches for the process lifetime, so this is the client's only
	// signal that a NEW requirement arrived after one was dismissed.
	RestartMarkCount   int               `json:"restart_mark_count"`
	RestartRequested   bool              `json:"restart_requested"`
	RestartRequestedAt *time.Time        `json:"restart_requested_at,omitempty"`
	Health             adminServerHealth `json:"health"`
}

// adminServerHealth backs the dashboard health strip. Version, uptime and node
// counts are not repeated here: the client already has them from
// /admin/system/build, started_at above, and /admin/nodes.
type adminServerHealth struct {
	Postgres    adminHealthComponent `json:"postgres"`
	Redis       adminHealthComponent `json:"redis"`
	Errors24h   int64                `json:"errors_24h"`
	Warnings24h int64                `json:"warnings_24h"`
}

// adminHealthComponent reports one backing service. `configured` false means
// the deployment runs without it (a supported single-node shape for Redis), in
// which case `ok` is absent rather than false — "not present" and "present but
// broken" must not look the same on the strip.
type adminHealthComponent struct {
	Configured bool     `json:"configured"`
	OK         *bool    `json:"ok,omitempty"`
	LatencyMS  *float64 `json:"latency_ms,omitempty"`
}

// adminLogLevelCounts is the cached error/warning tally. It is an array rather
// than a struct so it satisfies cache.TTLCache's comparable constraint without
// a pointer indirection.
type adminLogLevelCounts [2]int64

// HandleGetServerStatus handles GET /admin/server/status.
func (h *AdminHandler) HandleGetServerStatus(w http.ResponseWriter, r *http.Request) {
	snapshot := h.RestartStatus.Snapshot()
	resp := adminServerStatusResponse{
		StartedAt:              snapshot.StartedAt,
		RestartRequired:        snapshot.RestartRequired,
		RestartRequiredAt:      snapshot.RestartRequiredAt,
		RestartRequiredReason:  snapshot.RestartRequiredReason,
		RestartRequiredReasons: snapshot.RestartReasons,
		RestartMarkCount:       snapshot.RestartMarkCount,
		RestartRequested:       snapshot.RestartRequested,
		RestartRequestedAt:     snapshot.RestartRequestedAt,
	}

	// Settings live in Postgres, so this lookup fails in exactly the outage the
	// health object below exists to report. A failure therefore skips the
	// jellycompat restart derivation instead of aborting the response — a 500
	// here would hide postgres.ok:false from the one page built to show it —
	// and the lookup is bounded like the probes: a wedged pool must not hold
	// this optional derivation, and with it the whole response, to the request
	// deadline.
	if h.SettingsRepo != nil {
		settingsCtx, cancel := context.WithTimeout(r.Context(), adminHealthProbeTimeout)
		settings, err := h.SettingsRepo.GetAll(settingsCtx)
		cancel()
		if err == nil && jellycompat.WebComponentStatusForConfig(h.Config, settings).RestartRequired {
			resp.RestartRequired = true
			if resp.RestartRequiredReason == "" {
				resp.RestartRequiredReason = "jellyfin_compat"
			}
			// This requirement is derived here rather than marked on the
			// tracker, so the accumulated list has to gain it too — a client
			// scoping restarts by reason would otherwise never see it.
			if !slices.Contains(resp.RestartRequiredReasons, "jellyfin_compat") {
				resp.RestartRequiredReasons = append(resp.RestartRequiredReasons, "jellyfin_compat")
			}
		}
	}

	resp.Health = h.collectHealth(r.Context())

	writeJSON(w, http.StatusOK, resp)
}

// collectHealth probes the backing services and reads the recent log tallies.
// Every failure here is reported in the body, never as a status code: an
// unreachable dependency is exactly what an admin opened this page to see.
func (h *AdminHandler) collectHealth(ctx context.Context) adminServerHealth {
	health := adminServerHealth{
		Postgres: h.probePostgres(ctx),
		Redis:    h.probeRedis(ctx),
	}
	counts := h.logLevelCounts24h(ctx)
	health.Errors24h = counts[0]
	health.Warnings24h = counts[1]
	return health
}

func (h *AdminHandler) probePostgres(ctx context.Context) adminHealthComponent {
	if h == nil || h.pool == nil {
		return adminHealthComponent{}
	}
	probeCtx, cancel := context.WithTimeout(ctx, adminHealthProbeTimeout)
	defer cancel()

	start := time.Now()
	err := h.pool.Ping(probeCtx)
	return newHealthComponent(err == nil, time.Since(start))
}

func (h *AdminHandler) probeRedis(ctx context.Context) adminHealthComponent {
	if h == nil || h.RedisClient == nil {
		return adminHealthComponent{}
	}
	probeCtx, cancel := context.WithTimeout(ctx, adminHealthProbeTimeout)
	defer cancel()

	start := time.Now()
	err := h.RedisClient.Ping(probeCtx).Err()
	return newHealthComponent(err == nil, time.Since(start))
}

func newHealthComponent(ok bool, latency time.Duration) adminHealthComponent {
	// Sub-millisecond pings are the normal case for a local Postgres, so the
	// latency keeps two decimals instead of rounding an entire healthy install
	// down to zero.
	ms := math.Round(float64(latency.Microseconds())/10) / 100
	return adminHealthComponent{Configured: true, OK: &ok, LatencyMS: &ms}
}

// logLevelCounts24h returns [errors, warnings] logged in the last 24 hours.
// A server with operational logging disabled, or one whose log partitions have
// not been created yet, reports zeros and a warn log rather than failing the
// whole status route over a secondary number.
func (h *AdminHandler) logLevelCounts24h(ctx context.Context) adminLogLevelCounts {
	if h == nil || h.pool == nil {
		return adminLogLevelCounts{}
	}
	if h.logLevelCounts != nil {
		if counts, ok := h.logLevelCounts.Get(adminLogLevelCountsCacheKey); ok {
			return counts
		}
	}

	queryCtx, cancel := context.WithTimeout(ctx, adminHealthProbeTimeout)
	defer cancel()

	var counts adminLogLevelCounts
	err := h.pool.QueryRow(queryCtx, `
		SELECT
			COUNT(*) FILTER (WHERE level = 'error')::bigint,
			COUNT(*) FILTER (WHERE level = 'warn')::bigint
		FROM operational_logs
		WHERE timestamp >= now() - interval '24 hours'
	`).Scan(&counts[0], &counts[1])
	if err != nil {
		slog.WarnContext(ctx, "failed to count recent operational logs for server status",
			"component", "api", "error", err)
		return adminLogLevelCounts{}
	}

	if h.logLevelCounts != nil {
		h.logLevelCounts.Set(adminLogLevelCountsCacheKey, counts, adminLogLevelCountsCacheTTL)
	}
	return counts
}

func (h *AdminHandler) markServerRestartRequired(reason string) {
	if h == nil {
		return
	}
	h.RestartStatus.MarkRequired(reason)
}
