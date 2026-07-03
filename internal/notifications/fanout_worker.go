package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

const (
	fanoutPollInterval = 15 * time.Second
	fanoutClaimLimit   = 100
)

// FanoutWorker consumes unprocessed release_events and materializes
// notification_deliveries plus realtime dispatch. It is safe to run on
// multiple nodes: claiming uses FOR UPDATE SKIP LOCKED, delivery inserts are
// idempotent, and the last-notified cursor update is guarded.
type FanoutWorker struct {
	pool        *pgxpool.Pool
	releases    *ReleaseRepository
	interests   *InterestRepository
	deliveries  *DeliveryRepository
	preferences *PreferencesRepository
	settings    *Settings
	dispatcher  Dispatcher
	logger      *slog.Logger
	nudge       chan struct{}

	// Per-target outboxes: when set, the fanout transaction also enqueues
	// `pending` attempt rows for each newly inserted delivery — one per
	// enabled, reason-matching webhook and one per enabled web push
	// subscription.
	webhooks    *WebhookRepository
	rateLimiter *profileRateLimiter
	webPush     *WebPushRepository
	pushDevices *PushDeviceRepository
}

// SetWebhookOutbox wires durable webhook attempt enqueueing into the fanout
// transaction.
func (w *FanoutWorker) SetWebhookOutbox(webhooks *WebhookRepository, limiter *profileRateLimiter) {
	w.webhooks = webhooks
	w.rateLimiter = limiter
}

// SetWebPushOutbox wires durable web push attempt enqueueing into the fanout
// transaction.
func (w *FanoutWorker) SetWebPushOutbox(webPush *WebPushRepository) {
	w.webPush = webPush
}

// SetPushOutbox wires durable Apple push attempt enqueueing into the fanout
// transaction.
func (w *FanoutWorker) SetPushOutbox(pushDevices *PushDeviceRepository) {
	w.pushDevices = pushDevices
}

// NewFanoutWorker creates a FanoutWorker.
func NewFanoutWorker(
	pool *pgxpool.Pool,
	releases *ReleaseRepository,
	interests *InterestRepository,
	deliveries *DeliveryRepository,
	preferences *PreferencesRepository,
	settings *Settings,
	dispatcher Dispatcher,
) *FanoutWorker {
	return &FanoutWorker{
		pool:        pool,
		releases:    releases,
		interests:   interests,
		deliveries:  deliveries,
		preferences: preferences,
		settings:    settings,
		dispatcher:  dispatcher,
		logger:      slog.Default().With("component", "notifications.fanout"),
		nudge:       make(chan struct{}, 1),
	}
}

// Nudge schedules a near-term claim pass (after the settle delay) so
// notifications feel realtime without tight polling. Non-blocking.
func (w *FanoutWorker) Nudge() {
	if w == nil {
		return
	}
	select {
	case w.nudge <- struct{}{}:
	default:
	}
}

// Run processes release events until ctx is canceled.
func (w *FanoutWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(fanoutPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.nudge:
			// Wait out the settle delay so one scan's burst for a series
			// lands in a single claim batch instead of trickling through.
			settle := w.settings.SettleDelay(ctx)
			select {
			case <-ctx.Done():
				return
			case <-time.After(settle + time.Second):
			}
		}
		if !w.settings.FanoutEnabled(ctx) {
			continue
		}
		for {
			processed, err := w.processBatch(ctx)
			if err != nil {
				if ctx.Err() == nil {
					w.logger.Error("fanout batch failed", "error", err)
				}
				break
			}
			if processed == 0 {
				break
			}
		}
	}
}

// processBatch claims one batch of release events and fans them out. The
// claim, delivery inserts, cursor updates, and processed marks share one
// transaction so an event is never marked processed without durable
// deliveries; reprocessing after a crash is harmless because delivery inserts
// dedupe. Returns the number of events handled (fanned out + suppressed).
func (w *FanoutWorker) processBatch(ctx context.Context) (int, error) {
	started := time.Now()
	settle := w.settings.SettleDelay(ctx)
	maxBurst := w.settings.MaxSeriesBurst(ctx)

	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin fanout tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	claimed, err := w.releases.ClaimUnprocessed(ctx, tx, settle, fanoutClaimLimit)
	if err != nil {
		return 0, err
	}
	if len(claimed) == 0 {
		return 0, nil
	}

	// Non-episode kinds (movies, audiobooks, ebooks) have no per-profile
	// interest and never fan out; mark them processed immediately so retention
	// reclaims them. This must happen before the burst cap: flat item events
	// have no series_id, and ApplyBurstCap groups by (library_id, series_id). The server-channel
	// sweep reads events by cursor regardless of processed state.
	events, others := PartitionEventsByKind(claimed)
	if err := w.releases.MarkProcessed(ctx, tx, eventIDs(others), nil); err != nil {
		return 0, err
	}

	// Suppress events that aged past the staleness horizon before fanout
	// could run (fanout disabled for a stretch, extended downtime): users
	// should not receive a backlog of long-stale "new episode" notifications
	// when the worker comes back.
	staleCutoff := time.Now().Add(-w.settings.MaxEventAge(ctx))
	fresh := make([]ReleaseEvent, 0, len(events))
	stale := make([]ReleaseEvent, 0)
	for _, event := range events {
		if event.CreatedAt.Before(staleCutoff) {
			stale = append(stale, event)
		} else {
			fresh = append(fresh, event)
		}
	}
	if len(stale) > 0 {
		reason := SuppressedReasonStale
		if err := w.releases.MarkProcessed(ctx, tx, eventIDs(stale), &reason); err != nil {
			return 0, err
		}
	}

	fanout, suppressed := ApplyBurstCap(fresh, maxBurst)
	if len(suppressed) > 0 {
		reason := SuppressedReasonSeriesBurst
		if err := w.releases.MarkProcessed(ctx, tx, eventIDs(suppressed), &reason); err != nil {
			return 0, err
		}
	}

	totalRecipients := 0
	totalInserted := 0
	dispatchRows := make([]DeliveryRow, 0, 32)
	for _, event := range fanout {
		rows, recipients, err := w.fanOutEvent(ctx, tx, event)
		if err != nil {
			return 0, fmt.Errorf("fan out event %s: %w", event.ID, err)
		}
		totalRecipients += recipients
		totalInserted += len(rows)
		dispatchRows = append(dispatchRows, rows...)
	}
	if err := w.releases.MarkProcessed(ctx, tx, eventIDs(fanout), nil); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit fanout tx: %w", err)
	}

	// Publish realtime events only after the delivery transaction commits.
	// Failures here are tolerable: the durable rows appear on reconnect.
	for _, row := range dispatchRows {
		if err := w.dispatcher.Dispatch(ctx, row); err != nil {
			w.logger.Warn("notification dispatch failed",
				"delivery_id", row.ID, "profile_id", row.ProfileID, "error", err)
		}
	}

	w.logger.Info("fanout batch processed",
		"claimed", len(claimed),
		"non_episode", len(others),
		"fanned_out", len(fanout),
		"suppressed", len(suppressed),
		"stale", len(stale),
		"recipient_count", totalRecipients,
		"inserted_count", totalInserted,
		"deduped_count", totalRecipients-totalInserted,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	return len(claimed), nil
}

// fanOutEvent resolves recipients for one release event and inserts
// deliveries. Returns dispatch payloads for the rows actually inserted and
// the candidate recipient count.
func (w *FanoutWorker) fanOutEvent(ctx context.Context, tx pgx.Tx, event ReleaseEvent) ([]DeliveryRow, int, error) {
	candidates, err := w.interests.ListActiveBySeries(ctx, tx, event.LibraryID, event.SeriesID)
	if err != nil {
		return nil, 0, err
	}
	if len(candidates) == 0 {
		return nil, 0, nil
	}

	profileIDs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		profileIDs = append(profileIDs, candidate.ProfileID)
	}
	prefs, err := w.preferences.GetMany(ctx, tx, profileIDs)
	if err != nil {
		return nil, 0, err
	}

	pending := make(map[string]pendingDelivery, len(candidates))
	toInsert := make([]Delivery, 0, len(candidates))
	for _, candidate := range candidates {
		flags, eligible := EvaluateRecipient(candidate, prefs[candidate.ProfileID], event.EpisodeKey)
		if !eligible {
			continue
		}
		flagsJSON, err := json.Marshal(flags)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal reason flags: %w", err)
		}
		eventID := event.ID
		libraryID := event.LibraryID
		seriesID := event.SeriesID
		episodeID := event.EpisodeID
		delivery := Delivery{
			ID:             ulid.Make().String(),
			ReleaseEventID: &eventID,
			UserID:         candidate.UserID,
			ProfileID:      candidate.ProfileID,
			LibraryID:      &libraryID,
			SeriesID:       &seriesID,
			EpisodeID:      &episodeID,
			Type:           DeliveryTypeEpisodeAvailable,
			ReasonFlags:    flagsJSON,
		}
		pending[candidate.ProfileID] = pendingDelivery{delivery: delivery, flags: flags}
		toInsert = append(toInsert, delivery)
	}
	if len(toInsert) == 0 {
		return nil, len(candidates), nil
	}

	inserted, err := w.deliveries.BulkInsert(ctx, tx, toInsert)
	if err != nil {
		return nil, 0, err
	}
	// Durable dispatch outbox: enqueue per-target attempt rows in the same
	// transaction so a crash after commit delays webhooks instead of silently
	// dropping them. Push channels (specs 02-03) plug in here the same way.
	if err := w.enqueueWebhookOutbox(ctx, tx, inserted, pending); err != nil {
		return nil, 0, err
	}
	if err := w.enqueueWebPushOutbox(ctx, tx, inserted); err != nil {
		return nil, 0, err
	}
	if err := w.enqueuePushOutbox(ctx, tx, inserted); err != nil {
		return nil, 0, err
	}

	notifiedProfiles := make([]string, 0, len(inserted))
	for _, row := range inserted {
		notifiedProfiles = append(notifiedProfiles, row.ProfileID)
	}
	if err := w.interests.GuardedSetLastNotified(ctx, tx, event.LibraryID, event.SeriesID, event.EpisodeKey, notifiedProfiles); err != nil {
		return nil, 0, err
	}

	display, err := loadEventDisplay(ctx, tx, event.EpisodeID)
	if err != nil {
		return nil, 0, err
	}
	dispatchRows := make([]DeliveryRow, 0, len(inserted))
	for _, row := range inserted {
		entry, ok := pending[row.ProfileID]
		if !ok {
			continue
		}
		delivery := entry.delivery
		delivery.ID = row.ID
		delivery.CreatedAt = row.CreatedAt
		seasonNumber := event.SeasonNumber
		episodeNumber := event.EpisodeNumber
		dispatchRows = append(dispatchRows, DeliveryRow{
			Delivery:         delivery,
			SeriesTitle:      display.seriesTitle,
			EpisodeTitle:     display.episodeTitle,
			SeasonNumber:     &seasonNumber,
			EpisodeNumber:    &episodeNumber,
			PosterPath:       display.posterPath,
			PosterThumbhash:  display.posterThumbhash,
			PosterSourcePath: display.posterSourcePath,
		})
	}
	return dispatchRows, len(candidates), nil
}

// pendingDelivery pairs a candidate delivery with its matched reason flags so
// the outbox enqueue can apply per-webhook reason filters.
type pendingDelivery struct {
	delivery Delivery
	flags    ReasonFlags
}

// enqueuePushOutbox inserts pending Apple push attempt rows for each newly
// inserted delivery and enabled private-push device.
func (w *FanoutWorker) enqueuePushOutbox(ctx context.Context, tx pgx.Tx, inserted []InsertedDelivery) error {
	if w.pushDevices == nil || len(inserted) == 0 || !w.settings.ApplePushDeliveryEnabled(ctx) {
		return nil
	}
	profileSet := make(map[string]struct{}, len(inserted))
	profileIDs := make([]string, 0, len(inserted))
	for _, row := range inserted {
		if _, ok := profileSet[row.ProfileID]; !ok {
			profileSet[row.ProfileID] = struct{}{}
			profileIDs = append(profileIDs, row.ProfileID)
		}
	}
	devicesByProfile, err := w.pushDevices.ListEnabledAppleByProfiles(ctx, tx, profileIDs)
	if err != nil {
		return err
	}
	attempts := make([]PushDeliveryAttempt, 0, len(inserted))
	for _, row := range inserted {
		attempts = append(attempts, newPushDeliveryAttempts(row.ID, devicesByProfile[row.ProfileID])...)
	}
	return w.pushDevices.EnqueuePushAttempts(ctx, tx, attempts)
}

// enqueueWebPushOutbox inserts `pending` web push attempt rows for each newly
// inserted delivery, inside the fanout transaction. Subscriptions have no
// per-reason filters: profile preferences already gated delivery creation,
// and push volume is bounded by the per-series burst cap.
func (w *FanoutWorker) enqueueWebPushOutbox(ctx context.Context, tx pgx.Tx, inserted []InsertedDelivery) error {
	if w.webPush == nil || len(inserted) == 0 || !w.settings.WebPushEnabled(ctx) {
		return nil
	}
	profileSet := make(map[string]struct{}, len(inserted))
	profileIDs := make([]string, 0, len(inserted))
	for _, row := range inserted {
		if _, ok := profileSet[row.ProfileID]; !ok {
			profileSet[row.ProfileID] = struct{}{}
			profileIDs = append(profileIDs, row.ProfileID)
		}
	}
	subsByProfile, err := w.webPush.ListEnabledByProfiles(ctx, tx, profileIDs)
	if err != nil {
		return err
	}
	attempts := make([]DeliveryAttempt, 0, len(inserted))
	for _, row := range inserted {
		for _, sub := range subsByProfile[row.ProfileID] {
			attempts = append(attempts, DeliveryAttempt{
				ID:                     ulid.Make().String(),
				NotificationDeliveryID: row.ID,
				TargetID:               sub.ID,
			})
		}
	}
	return w.webPush.EnqueueAttempts(ctx, tx, attempts)
}

// enqueueWebhookOutbox inserts `pending` webhook attempt rows for each newly
// inserted delivery, inside the fanout transaction. Two filters apply:
// per-webhook reason flags, and the per-profile delivery rate limit
// (over-limit notifications stay in the inbox; webhooks just don't fire).
func (w *FanoutWorker) enqueueWebhookOutbox(ctx context.Context, tx pgx.Tx, inserted []InsertedDelivery, pending map[string]pendingDelivery) error {
	if w.webhooks == nil || len(inserted) == 0 || !w.settings.WebhooksEnabled(ctx) {
		return nil
	}
	profileSet := make(map[string]struct{}, len(inserted))
	profileIDs := make([]string, 0, len(inserted))
	for _, row := range inserted {
		if _, ok := profileSet[row.ProfileID]; !ok {
			profileSet[row.ProfileID] = struct{}{}
			profileIDs = append(profileIDs, row.ProfileID)
		}
	}
	hooksByProfile, err := w.webhooks.ListEnabledByProfiles(ctx, tx, profileIDs)
	if err != nil {
		return err
	}
	rateLimit := w.settings.WebhooksDeliveriesPerMinute(ctx)

	attempts := make([]DeliveryAttempt, 0, len(inserted))
	rateLimited := 0
	for _, row := range inserted {
		entry, ok := pending[row.ProfileID]
		if !ok {
			continue
		}
		for _, hook := range hooksByProfile[row.ProfileID] {
			if !hook.MatchesReasons(entry.flags) {
				continue
			}
			if w.rateLimiter != nil && !w.rateLimiter.Allow(row.ProfileID, rateLimit) {
				rateLimited++
				continue
			}
			attempts = append(attempts, DeliveryAttempt{
				ID:                     ulid.Make().String(),
				NotificationDeliveryID: row.ID,
				TargetID:               hook.ID,
			})
		}
	}
	if rateLimited > 0 {
		w.logger.Warn("webhook deliveries rate limited", "skipped", rateLimited)
	}
	return w.webhooks.EnqueueAttempts(ctx, tx, attempts)
}

type eventDisplay struct {
	seriesTitle      string
	episodeTitle     string
	posterPath       string
	posterThumbhash  string
	posterSourcePath string
}

// loadEventDisplay fetches per-event display metadata once and reuses it for
// every recipient's dispatch payload.
func loadEventDisplay(ctx context.Context, tx pgx.Tx, episodeID string) (eventDisplay, error) {
	var display eventDisplay
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(e.title, ''), COALESCE(s.title, ''),
		       COALESCE(s.poster_path, ''), COALESCE(s.poster_thumbhash, ''),
		       COALESCE(s.poster_source_path, '')
		FROM episodes e
		LEFT JOIN media_items s ON s.content_id = e.series_id
		WHERE e.content_id = $1`,
		episodeID,
	).Scan(&display.episodeTitle, &display.seriesTitle, &display.posterPath,
		&display.posterThumbhash, &display.posterSourcePath)
	if errors.Is(err, pgx.ErrNoRows) {
		return eventDisplay{}, nil
	}
	if err != nil {
		return display, fmt.Errorf("load event display metadata: %w", err)
	}
	return display, nil
}

func eventIDs(events []ReleaseEvent) []string {
	ids := make([]string, 0, len(events))
	for _, event := range events {
		ids = append(ids, event.ID)
	}
	return ids
}
