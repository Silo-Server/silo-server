package notifications

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// serverChannelFetchLimit bounds one sweep batch. Renderers cap how many
// groups one post shows; a larger backlog drains across consecutive passes.
const serverChannelFetchLimit = 200

// serverChannelWorker sweeps release_events into per-channel content digest
// posts. Unlike the per-profile channels it reads the event stream directly —
// there is no profile fan-out for broadcast destinations — using a
// per-channel (created_at, id) watermark that advances only on success.
// Multi-node safe: the channel row claim uses FOR UPDATE SKIP LOCKED.
type serverChannelWorker struct {
	pool     *pgxpool.Pool
	repo     *ServerChannelRepository
	releases *ReleaseRepository
	sender   *serverChannelSender
	settings *Settings
	logger   *slog.Logger
	nudge    chan struct{}
	// posterURL picks the artwork URL Discord embeds may carry. Wired by
	// NewSystem after construction; nil renders embeds without images.
	posterURL func(ctx context.Context, posterPath, posterSourcePath string) string
	// requesterDiscordID resolves a request's requester to their linked
	// Discord user id for @mentions (System.requesterDiscordID, which owns
	// the admin-setting gate). Wired by NewSystem after construction; nil or
	// empty results post without a mention.
	requesterDiscordID func(ctx context.Context, userID int) string

	// Short-lived cache behind requestEventChannels.
	requestChannelsMu        sync.Mutex
	requestChannels          []ServerChannel
	requestChannelsFetchedAt time.Time
}

const requestChannelCacheTTL = 15 * time.Second

func newServerChannelWorker(
	pool *pgxpool.Pool,
	repo *ServerChannelRepository,
	releases *ReleaseRepository,
	sender *serverChannelSender,
	settings *Settings,
) *serverChannelWorker {
	return &serverChannelWorker{
		pool:     pool,
		repo:     repo,
		releases: releases,
		sender:   sender,
		settings: settings,
		logger:   slog.Default().With("component", "notifications.server_channels"),
		nudge:    make(chan struct{}, 1),
	}
}

// Nudge schedules a near-term pass. Events younger than the batch window are
// invisible to the sweep regardless, so a nudge mostly helps after the window
// has already elapsed (e.g. a settings flip). Non-blocking.
func (w *serverChannelWorker) Nudge() {
	if w == nil {
		return
	}
	select {
	case w.nudge <- struct{}{}:
	default:
	}
}

// Run sweeps eligible channels until ctx is canceled.
func (w *serverChannelWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(channelPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.nudge:
			select {
			case <-ctx.Done():
				return
			case <-time.After(channelNudgeDelay):
			}
		}
		if !w.settings.ServerChannelsEnabled(ctx) {
			continue
		}
		w.runPass(ctx)
	}
}

// runPass attempts one sweep per eligible channel. Failures back off per
// channel via the shared exponential-backoff rule.
func (w *serverChannelWorker) runPass(ctx context.Context) {
	channels, err := w.repo.ListEnabledForContent(ctx)
	if err != nil {
		w.logger.Error("server channel pass: list channels failed", "error", err)
		return
	}
	if len(channels) == 0 {
		return
	}
	batchAge := w.settings.ServerChannelsBatchWindow(ctx)
	now := time.Now()

	failures := 0
	for _, ch := range channels {
		if ctx.Err() != nil || failures >= channelMaxFailuresPerPass {
			return
		}
		if !channelRetryEligible(now, ch.LastAttemptAt, ch.ConsecutiveFailures) {
			continue
		}
		// Cheap pre-check so idle channels don't open a claim transaction
		// every pass. A stale watermark only ever costs a harmless extra
		// claim.
		pending, err := w.releases.HasEventsSince(ctx,
			Cursor{CreatedAt: ch.WatermarkCreatedAt, ID: ch.WatermarkID}, batchAge)
		if err != nil {
			w.logger.Warn("server channel pass: pending check failed", "channel_id", ch.ID, "error", err)
			continue
		}
		if !pending {
			continue
		}
		sent, err := w.processChannel(ctx, ch.ID, batchAge)
		if err != nil {
			failures++
			w.logger.Warn("server channel sweep failed", "channel_id", ch.ID, "error", err)
			continue
		}
		if sent {
			// Drain a large backlog promptly instead of waiting a poll cycle.
			w.Nudge()
		}
	}
}

// processChannel sweeps one channel under its row lock: read events past the
// watermark, group, send, and advance the watermark — all in one transaction
// so the watermark commits only with the outcome it describes. Returns
// whether a post went out.
func (w *serverChannelWorker) processChannel(ctx context.Context, channelID string, batchAge time.Duration) (bool, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin server channel sweep tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ch, err := w.repo.ClaimForSweep(ctx, tx, channelID)
	if err != nil {
		return false, err
	}
	if ch == nil {
		return false, nil // another node holds the row, or the channel was just disabled
	}
	// Re-check the kill switch under the lock: disabling the feature must
	// stop in-flight sweeps, not just future passes.
	if !w.settings.ServerChannelsEnabled(ctx) {
		return false, nil
	}

	since := Cursor{CreatedAt: ch.WatermarkCreatedAt, ID: ch.WatermarkID}
	events, err := w.releases.ListEventsSince(ctx, tx, since, batchAge, serverChannelFetchLimit)
	if err != nil {
		return false, err
	}
	if len(events) == 0 {
		return false, nil
	}

	// The watermark passes everything fetched — including events filtered by
	// the channel's kind toggles and events past the staleness horizon — so
	// skipped events are never re-read. It only ever moves forward.
	last := events[len(events)-1]
	watermark := maxCursor(Cursor{CreatedAt: last.CreatedAt, ID: last.ID}, since)

	staleCutoff := time.Now().Add(-w.settings.MaxEventAge(ctx))
	fresh := make([]ReleaseEvent, 0, len(events))
	for _, event := range events {
		if event.CreatedAt.Before(staleCutoff) {
			continue // same staleness policy as fanout: old news is noise
		}
		if !ch.WantsContentKind(event.Kind) {
			continue
		}
		fresh = append(fresh, event)
	}
	if len(fresh) == 0 {
		if err := w.repo.MarkSwept(ctx, tx, ch.ID, watermark); err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}

	metas, err := loadContentMeta(ctx, tx, fresh)
	if err != nil {
		return false, err
	}
	groups := GroupContentEvents(fresh, metas)
	if ch.Type == WebhookTypeDiscord && w.posterURL != nil {
		for i := range groups {
			groups[i].Meta.PosterURL = w.posterURL(ctx,
				groups[i].Meta.PosterPath, groups[i].Meta.PosterSourcePath)
		}
	}

	result := w.sender.sendContent(ctx, ch, groups, false)
	if !result.OK {
		var status *int
		if result.HTTPStatus > 0 {
			status = &result.HTTPStatus
		}
		if err := w.repo.MarkSweepFailure(ctx, tx, ch.ID, status, result.Message); err != nil {
			return false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return false, fmt.Errorf("send failed: %s", result.Message)
	}

	if err := w.repo.MarkSwept(ctx, tx, ch.ID, watermark); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	w.logger.Info("server channel content posted",
		"channel_id", ch.ID, "url_host", ch.URLHost,
		"events", len(fresh), "groups", len(groups))
	return true, nil
}

// loadContentMeta batch-fetches display metadata for every series and movie
// in the batch, keyed by content id.
func loadContentMeta(ctx context.Context, tx pgx.Tx, events []ReleaseEvent) (map[string]ContentMeta, error) {
	idSet := make(map[string]struct{}, len(events))
	ids := make([]string, 0, len(events))
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := idSet[id]; !ok {
			idSet[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	for _, event := range events {
		if event.Kind == EventKindMovie {
			add(event.ItemID)
		} else {
			add(event.SeriesID)
		}
	}
	metas := make(map[string]ContentMeta, len(ids))
	if len(ids) == 0 {
		return metas, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT content_id, title, COALESCE(year, 0), COALESCE(type, ''),
		       COALESCE(overview, ''), COALESCE(poster_path, ''),
		       COALESCE(poster_source_path, ''),
		       COALESCE(genres, '{}'::text[]), COALESCE(content_rating, ''),
		       COALESCE(rating_imdb, 0), COALESCE(rating_tmdb, 0),
		       COALESCE(imdb_id, ''), COALESCE(tmdb_id, ''), COALESCE(tvdb_id, '')
		FROM media_items
		WHERE content_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("load content metadata: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var meta ContentMeta
		if err := rows.Scan(&id, &meta.Title, &meta.Year, &meta.Type,
			&meta.Overview, &meta.PosterPath, &meta.PosterSourcePath,
			&meta.Genres, &meta.ContentRating,
			&meta.RatingIMDB, &meta.RatingTMDB,
			&meta.IMDBID, &meta.TMDBID, &meta.TVDBID,
		); err != nil {
			return nil, fmt.Errorf("scan content metadata: %w", err)
		}
		metas[id] = meta
	}
	return metas, rows.Err()
}

// requestEventChannels returns the request-subscribed channel list through a
// short TTL cache: every request lifecycle event posts through here, and a
// reconcile pass can fulfill up to 100 requests back-to-back — the common
// no-subscriber case must be a memory check, not a query per event. Newly
// toggled channels start posting within the TTL (same staleness budget as
// the settings cache).
func (w *serverChannelWorker) requestEventChannels(ctx context.Context) ([]ServerChannel, error) {
	w.requestChannelsMu.Lock()
	defer w.requestChannelsMu.Unlock()
	if time.Since(w.requestChannelsFetchedAt) < requestChannelCacheTTL {
		return w.requestChannels, nil
	}
	channels, err := w.repo.ListEnabledForRequests(ctx)
	if err != nil {
		return nil, err
	}
	w.requestChannels = channels
	w.requestChannelsFetchedAt = time.Now()
	return channels, nil
}

// PostRequestEvent posts one request lifecycle event to every opted-in
// channel, best-effort: failures are recorded for backoff/auto-disable but
// never propagate to the request flow.
func (w *serverChannelWorker) PostRequestEvent(ctx context.Context, event string, info RequestEventInfo) {
	if w == nil || !w.settings.ServerChannelsEnabled(ctx) {
		return
	}
	channels, err := w.requestEventChannels(ctx)
	if err != nil {
		w.logger.Warn("server channel request post: list channels failed", "error", err)
		return
	}
	// Request posters are raw TMDB paths rendered by the Discord builder;
	// "off" is the only poster mode that changes them.
	if w.settings.DiscordPosterMode(ctx) == DiscordPostersOff {
		info.PosterPath = ""
	}
	now := time.Now()
	mentionResolved := false
	for _, ch := range channels {
		if ctx.Err() != nil {
			return
		}
		if !ch.WantsRequestEvent(event) {
			continue
		}
		if !channelRetryEligible(now, ch.LastAttemptAt, ch.ConsecutiveFailures) {
			continue
		}
		// Resolve the requester's Discord identity at most once per event,
		// and only when a Discord channel is actually about to receive it —
		// the common no-subscriber case must stay query-free.
		if ch.Type == WebhookTypeDiscord && !mentionResolved {
			mentionResolved = true
			if w.requesterDiscordID != nil {
				info.RequesterDiscordID = w.requesterDiscordID(ctx, info.RequesterUserID)
			}
		}
		result := w.sender.sendRequest(ctx, &ch, event, info)
		if result.OK {
			if err := w.repo.RecordSendSuccess(ctx, ch.ID); err != nil {
				w.logger.Warn("server channel success bookkeeping failed", "channel_id", ch.ID, "error", err)
			}
			continue
		}
		var status *int
		if result.HTTPStatus > 0 {
			status = &result.HTTPStatus
		}
		if err := w.repo.RecordSendFailure(ctx, ch.ID, status, result.Message); err != nil {
			w.logger.Warn("server channel failure bookkeeping failed", "channel_id", ch.ID, "error", err)
		}
		w.logger.Warn("server channel request post failed",
			"channel_id", ch.ID, "event", event, "status", result.HTTPStatus, "message", result.Message)
	}
}
