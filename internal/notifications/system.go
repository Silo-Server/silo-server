package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/discord"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// UserLister enumerates login accounts for the interest backfill. Satisfied
// by *auth.UserRepository.
type UserLister interface {
	List(ctx context.Context) ([]*models.User, error)
}

// ImageURLResolver presigns stored image paths into client-fetchable URLs.
// Satisfied by *catalog.DetailService.
type ImageURLResolver interface {
	PresignImageURL(ctx context.Context, path, imageType, size string) string
}

// System bundles the user-facing release-notification services: availability
// detection, interest maintenance, fanout, inbox repositories, and websocket
// tickets. It is distinct from the operational Hub in hub.go.
type System struct {
	Settings    *Settings
	Releases    *ReleaseRepository
	Interests   *InterestRepository
	Deliveries  *DeliveryRepository
	Preferences *PreferencesRepository
	Detector    *AvailabilityDetector
	Interest    *InterestUpdater
	Fanout      *FanoutWorker
	Tickets     TicketStore
	// Webhooks is nil when no at-rest cipher is configured (webhook URLs are
	// credentials and must not be stored in plaintext).
	Webhooks *WebhookService
	// ServerChannels is nil when no at-rest cipher is configured (channel
	// URLs are credentials, same rule as webhooks).
	ServerChannels *ServerChannelService
	// WebPush is nil when the settings store is not writable (VAPID keys
	// could not be provisioned).
	WebPush *WebPushService
	// PushDevices is nil when no at-rest cipher is configured; APNs tokens are
	// credentials and must not be stored in plaintext.
	PushDevices *PushDeviceService
	// EmailPrefs is nil when no mail sender was provided.
	EmailPrefs *EmailPrefsRepository
	// DiscordPrefs holds Discord DM link + mode state; the channel only
	// delivers once an admin configures bot credentials in settings.
	DiscordPrefs *DiscordPrefsRepository

	mailSender    mail.Sender
	emailWorker   *accountChannelWorker[string]
	discordWorker *accountChannelWorker[int]
	discordClient *discord.Client
	// publicURL is the server's externally reachable base URL, used as the
	// fallback for tokenized email links (see SetPublicURL).
	publicURL string

	webhookRepo       *WebhookRepository
	webhookDispatcher *WebhookDispatcher
	webhookRetry      *WebhookRetryWorker
	webPushRepo       *WebPushRepository
	webPushDispatcher *WebPushDispatcher
	pushDeviceRepo    *PushDeviceRepository
	pushDispatcher    *PushDispatcher
	pushSender        *pushSender
	// serverChannelWorker sweeps release_events into admin broadcast posts;
	// nil without the at-rest cipher.
	serverChannelWorker *serverChannelWorker
	// dispatcher is the same MultiDispatcher the fanout worker uses; the
	// operational dispatch path shares it so every delivery reaches every
	// configured channel the same way.
	dispatcher Dispatcher

	pool   *pgxpool.Pool
	stores userstore.UserStoreProvider
	users  UserLister
	images ImageURLResolver
	logger *slog.Logger
	wg     sync.WaitGroup
}

// NewSystem wires the notification system. hub may be nil (no realtime
// publishing); redisClient may be nil (in-memory websocket tickets);
// mailSender may be nil (no email channel).
func NewSystem(
	pool *pgxpool.Pool,
	settingsReader SettingReader,
	stores userstore.UserStoreProvider,
	scopes ScopeResolver,
	users UserLister,
	hub *evt.Hub,
	redisClient *redis.Client,
	cipher *secret.Cipher,
	mailSender mail.Sender,
) *System {
	settings := NewSettings(settingsReader)
	releases := NewReleaseRepository(pool)
	interests := NewInterestRepository(pool)
	deliveries := NewDeliveryRepository(pool)
	preferences := NewPreferencesRepository(pool)

	wsDispatcher := NewWebsocketDispatcher(hub)
	dispatchers := []Dispatcher{wsDispatcher}

	// Outbound webhooks require the at-rest cipher: destination URLs are
	// bearer credentials (Discord) and must never be stored in plaintext.
	var webhookRepo *WebhookRepository
	var webhookService *WebhookService
	var webhookDispatcher *WebhookDispatcher
	var webhookRetry *WebhookRetryWorker
	var sender *webhookSender
	var pushDeviceService *PushDeviceService
	var pushDeviceRepo *PushDeviceRepository
	var pushSenderInst *pushSender
	var pushDispatcher *PushDispatcher
	if cipher != nil {
		webhookRepo = NewWebhookRepository(pool)
		sender = newWebhookSender(webhookRepo, deliveries, cipher, settings)
		webhookService = newWebhookService(webhookRepo, cipher, settings, sender)
		webhookDispatcher = newWebhookDispatcher(sender)
		webhookRetry = newWebhookRetryWorker(sender)
		dispatchers = append(dispatchers, webhookDispatcher)
		pushDeviceRepo = NewPushDeviceRepository(pool)
		pushDeviceService = NewPushDeviceService(pushDeviceRepo, cipher)
		pushSenderInst = newPushSender(pushDeviceRepo, deliveries, cipher, settings)
		pushDispatcher = newPushDispatcher(pushSenderInst)
		dispatchers = append(dispatchers, pushDispatcher)
	}

	// Admin server channels (broadcast destinations) share the cipher
	// requirement: their URLs are credentials too.
	var serverChannelService *ServerChannelService
	var serverChannelSweep *serverChannelWorker
	if cipher != nil {
		serverChannelRepo := NewServerChannelRepository(pool)
		serverChannelSenderInst := newServerChannelSender(cipher, settings)
		serverChannelService = newServerChannelService(serverChannelRepo, cipher, settings, serverChannelSenderInst)
		serverChannelSweep = newServerChannelWorker(pool, serverChannelRepo, releases, serverChannelSenderInst, settings)
	}

	// Web push needs a writable settings store to self-provision its VAPID
	// keypair. The reader main passes is the encrypted settings repo, which
	// also writes; tests may pass a read-only stub.
	var webPushRepo *WebPushRepository
	var webPushService *WebPushService
	var webPushDispatcher *WebPushDispatcher
	var webPushSenderInst *webPushSender
	if writer, ok := settingsReader.(SettingWriter); ok && writer != nil {
		webPushRepo = NewWebPushRepository(pool)
		webPushService = newWebPushService(webPushRepo, settings, writer)
		webPushSenderInst = newWebPushSender(webPushRepo, deliveries, webPushService, settings)
		webPushDispatcher = newWebPushDispatcher(webPushSenderInst)
		dispatchers = append(dispatchers, webPushDispatcher)
	}

	// Email rides the shared SMTP core. Unlike the per-target channels it
	// keeps no outbox: its dispatcher only nudges the watermark sweep.
	var emailPrefs *EmailPrefsRepository
	var emailChannelInst *emailChannel
	var emailWorker *accountChannelWorker[string]
	if mailSender != nil {
		emailPrefs = NewEmailPrefsRepository(pool)
		emailChannelInst = &emailChannel{
			prefs:      emailPrefs,
			deliveries: deliveries,
			settings:   settings,
			sender:     mailSender,
		}
		emailWorker = newAccountChannelWorker(pool, emailChannelInst)
		dispatchers = append(dispatchers, newNudgeDispatcher(emailWorker))
	}

	// Discord DMs ride the same account-watermark engine as email. The
	// channel is always wired (its credentials live in settings and may be
	// configured at runtime); enabled() gates each pass on the bot token.
	discordPrefs := NewDiscordPrefsRepository(pool)
	discordClient := discord.NewClient()
	discordWorker, discordChannelInst := newDiscordWorker(pool, deliveries, discordPrefs, settings, discordClient)
	dispatchers = append(dispatchers, newNudgeDispatcher(discordWorker))

	multiDispatcher := NewMultiDispatcher(dispatchers...)
	fanout := NewFanoutWorker(pool, releases, interests, deliveries, preferences, settings, multiDispatcher)
	if webhookRepo != nil {
		fanout.SetWebhookOutbox(webhookRepo, newProfileRateLimiter())
	}
	if webPushRepo != nil {
		fanout.SetWebPushOutbox(webPushRepo)
	}
	if pushDeviceRepo != nil {
		fanout.SetPushOutbox(pushDeviceRepo)
	}
	detector := NewAvailabilityDetector(releases, settings)
	detector.SetFanoutNudge(func() {
		fanout.Nudge()
		serverChannelSweep.Nudge() // nil-safe
	})
	interest := NewInterestUpdater(pool, interests, stores, scopes)

	system := &System{
		Settings:            settings,
		Releases:            releases,
		Interests:           interests,
		Deliveries:          deliveries,
		Preferences:         preferences,
		Detector:            detector,
		Interest:            interest,
		Fanout:              fanout,
		Tickets:             NewTicketStore(redisClient),
		Webhooks:            webhookService,
		ServerChannels:      serverChannelService,
		WebPush:             webPushService,
		PushDevices:         pushDeviceService,
		EmailPrefs:          emailPrefs,
		DiscordPrefs:        discordPrefs,
		mailSender:          mailSender,
		emailWorker:         emailWorker,
		discordWorker:       discordWorker,
		discordClient:       discordClient,
		webhookRepo:         webhookRepo,
		webhookDispatcher:   webhookDispatcher,
		webhookRetry:        webhookRetry,
		webPushRepo:         webPushRepo,
		webPushDispatcher:   webPushDispatcher,
		pushDeviceRepo:      pushDeviceRepo,
		pushDispatcher:      pushDispatcher,
		pushSender:          pushSenderInst,
		serverChannelWorker: serverChannelSweep,
		dispatcher:          multiDispatcher,
		pool:                pool,
		stores:              stores,
		users:               users,
		logger:              slog.Default().With("component", "notifications.system"),
	}
	wsDispatcher.payload = system.PayloadForRow
	if emailChannelInst != nil {
		emailChannelInst.profileName = system.lookupProfileName
	}
	if sender != nil {
		sender.operational = system.DispatchOperational
		sender.posterURL = system.discordPosterURL
	}
	discordChannelInst.posterURL = system.discordPosterURL
	if serverChannelSweep != nil {
		serverChannelSweep.posterURL = system.discordPosterURL
		serverChannelSweep.requesterDiscordID = system.requesterDiscordID
	}
	if webPushSenderInst != nil {
		webPushSenderInst.payload = system.PayloadForRow
	}
	return system
}

// SetImageResolver wires presigned poster URLs into notification payloads.
// Optional; without it clients fall back to thumbhash placeholders.
func (s *System) SetImageResolver(resolver ImageURLResolver) {
	if s != nil {
		s.images = resolver
	}
}

// discordPosterURL resolves a delivery's poster to a URL a Discord embed may
// carry, honoring the admin's poster mode: nothing when posters are off,
// public provider CDN URLs when derivable, and — only on the explicit
// "server" opt-in — a presigned URL from this server's own image storage.
// Wired into the Discord send paths as their posterURL hook.
func (s *System) discordPosterURL(ctx context.Context, posterPath, posterSourcePath string) string {
	mode := s.Settings.DiscordPosterMode(ctx)
	if mode == DiscordPostersOff {
		return ""
	}
	if url := embedPosterURL(posterPath, posterSourcePath); url != "" {
		return url
	}
	if mode != DiscordPostersServer || s.images == nil || posterPath == "" {
		return ""
	}
	return s.images.PresignImageURL(ctx, posterPath, "poster", "")
}

// PayloadForRow converts a row to its wire shape, attaching a presigned
// poster URL when an image resolver is configured.
func (s *System) PayloadForRow(ctx context.Context, row DeliveryRow) DeliveryRowPayload {
	payload := PayloadForRow(row)
	if s != nil && s.images != nil && row.PosterPath != "" {
		payload.PosterURL = s.images.PresignImageURL(ctx, row.PosterPath, "poster", "")
	}
	return payload
}

// PayloadsForRows converts rows to their wire shape with poster URLs.
func (s *System) PayloadsForRows(ctx context.Context, rows []DeliveryRow) []DeliveryRowPayload {
	payloads := make([]DeliveryRowPayload, 0, len(rows))
	for _, row := range rows {
		payloads = append(payloads, s.PayloadForRow(ctx, row))
	}
	return payloads
}

// Start launches the fanout worker, interest updater, and (when configured)
// the webhook dispatch pool and retry worker under ctx.
func (s *System) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.Fanout.Run(ctx)
	}()
	go func() {
		defer s.wg.Done()
		s.Interest.Run(ctx)
	}()
	if s.webhookDispatcher != nil {
		s.wg.Add(2)
		go func() {
			defer s.wg.Done()
			s.webhookDispatcher.Run(ctx)
		}()
		go func() {
			defer s.wg.Done()
			s.webhookRetry.Run(ctx)
		}()
	}
	if s.serverChannelWorker != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serverChannelWorker.Run(ctx)
		}()
	}
	if s.emailWorker != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.emailWorker.Run(ctx)
		}()
	}
	if s.discordWorker != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.discordWorker.Run(ctx)
		}()
	}
	if s.webPushDispatcher != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.webPushDispatcher.Run(ctx)
		}()
		// Provision the VAPID keypair eagerly so a broken settings store
		// surfaces at startup instead of on the first subscribe, and the
		// capability endpoint never pays the generation latency.
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if _, err := s.WebPush.PublicKey(ctx); err != nil && ctx.Err() == nil {
				s.logger.Error("web push VAPID provisioning failed", "error", err)
			}
		}()
	}
	if s.pushDispatcher != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.pushDispatcher.Run(ctx)
		}()
	}
}

// Wait blocks until the background loops exit (after their context is
// canceled), so shutdown can drain in-flight work.
func (s *System) Wait() {
	if s != nil {
		s.wg.Wait()
	}
}

// PurgeProfile removes all notification state for a deleted profile.
// Profiles may live in per-user SQLite stores, so Postgres cascades cannot
// cover this.
func (s *System) PurgeProfile(ctx context.Context, profileID string) error {
	if s == nil || profileID == "" {
		return nil
	}
	if err := s.Interests.DeleteAllForProfile(ctx, profileID); err != nil {
		return fmt.Errorf("purge interest rows: %w", err)
	}
	if err := s.Deliveries.DeleteAllForProfile(ctx, profileID); err != nil {
		return fmt.Errorf("purge deliveries: %w", err)
	}
	if err := s.Preferences.DeleteForProfile(ctx, profileID); err != nil {
		return fmt.Errorf("purge preferences: %w", err)
	}
	if s.webhookRepo != nil {
		if err := s.webhookRepo.DeleteAllForProfile(ctx, profileID); err != nil {
			return fmt.Errorf("purge webhooks: %w", err)
		}
	}
	if s.webPushRepo != nil {
		if err := s.webPushRepo.DeleteAllForProfile(ctx, profileID); err != nil {
			return fmt.Errorf("purge web push subscriptions: %w", err)
		}
	}
	if s.pushDeviceRepo != nil {
		if err := s.pushDeviceRepo.DeleteAllForProfile(ctx, profileID); err != nil {
			return fmt.Errorf("purge push devices: %w", err)
		}
	}
	if s.EmailPrefs != nil {
		if err := s.EmailPrefs.DeleteForProfile(ctx, profileID); err != nil {
			return fmt.Errorf("purge email prefs: %w", err)
		}
	}
	return nil
}

// SeedAvailability inserts episode_availability and movie_availability for
// every currently playable episode and movie without creating release events,
// then writes the per-library, per-kind seed markers. Idempotent and
// rerunnable; it exists for libraries that predate the notifications feature.
//
// Library selection is load-bearing for flood and loss safety:
//   - already-seeded libraries are skipped: availability there is owned by
//     the scan-end detector, and a silent (emitEvents=false) insert racing an
//     in-flight scan would permanently suppress the claimed episode's release
//     event;
//   - never-fully-scanned libraries are skipped: their catalog is empty or
//     partial, so seed-marking them now would make the first real scan emit
//     release events for the entire back catalog. The detector writes their
//     marker when that first full scan completes.
func (s *System) SeedAvailability(ctx context.Context, progress func(percent int, message string)) error {
	report := func(percent int, message string) {
		if progress != nil {
			progress(percent, message)
		}
	}
	// Each kind seeds independently with its own seed markers, because the
	// episode pass historically marked every scanned library (movie libraries
	// included) with zero movie rows. The flat item kinds come from the
	// registry so a new kind cannot be forgotten here.
	type seedPass struct {
		kind          string
		seedCondition string
		record        func(ctx context.Context, libraryID int, emitEvents bool) (int, int, error)
	}
	passes := []seedPass{{
		kind:          EventKindEpisode,
		seedCondition: `SELECT 1 FROM notification_library_seed_state seed WHERE seed.library_id = mf.id`,
		record:        s.Releases.RecordAvailabilityForLibrary,
	}}
	for _, k := range flatItemKinds {
		passes = append(passes, seedPass{
			kind: k.Kind,
			seedCondition: `SELECT 1 FROM notification_content_seed_state seed
				WHERE seed.library_id = mf.id AND seed.kind = '` + k.Kind + `'`,
			record: func(ctx context.Context, libraryID int, emitEvents bool) (int, int, error) {
				return s.Releases.RecordItemAvailabilityForLibrary(ctx, k, libraryID, emitEvents)
			},
		})
	}
	for passIdx, pass := range passes {
		rows, err := s.pool.Query(ctx, `
			SELECT mf.id
			FROM media_folders mf
			WHERE mf.last_scanned_at IS NOT NULL
			  AND NOT EXISTS (`+pass.seedCondition+`)
			ORDER BY mf.id`)
		if err != nil {
			return fmt.Errorf("list libraries: %w", err)
		}
		libraryIDs := make([]int, 0, 8)
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("scan library id: %w", err)
			}
			libraryIDs = append(libraryIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		totalSeeded := 0
		for i, libraryID := range libraryIDs {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			inserted, _, err := pass.record(ctx, libraryID, false)
			if err != nil {
				return fmt.Errorf("seed library %d (%s): %w", libraryID, pass.kind, err)
			}
			if err := s.Releases.MarkContentSeeded(ctx, libraryID, pass.kind); err != nil {
				return fmt.Errorf("mark library %d seeded (%s): %w", libraryID, pass.kind, err)
			}
			totalSeeded += inserted
			// Each pass owns an equal slice of the progress range.
			passSpan := 100 / len(passes)
			passBase := passIdx * passSpan
			report(passBase+(i+1)*passSpan/max(len(libraryIDs), 1),
				fmt.Sprintf("Seeded library %d %s availability (%d new rows)", libraryID, pass.kind, inserted))
		}
		s.logger.Info("availability seeding completed",
			"kind", pass.kind, "libraries", len(libraryIDs), "availability_rows", totalSeeded)
	}
	report(100, "Availability seeding completed")
	return nil
}

const interestRebuildTask = "interest_rebuild"

// RebuildInterest incrementally rebuilds profile_series_interest from
// favorites, watchlist, and watch progress for every profile. Checkpointed
// per profile so a crash resumes with at most one profile of repeated work;
// recomputes are idempotent upserts. Completed runs reset and start over
// (the task doubles as periodic drift repair).
func (s *System) RebuildInterest(ctx context.Context, progress func(percent int, message string)) error {
	report := func(percent int, message string) {
		if progress != nil {
			progress(percent, message)
		}
	}
	if s.users == nil {
		return fmt.Errorf("interest rebuild requires a user lister")
	}

	checkpoint, completedAt, err := s.loadBackfillCheckpoint(ctx, interestRebuildTask)
	if err != nil {
		return err
	}
	if completedAt != nil {
		// Start a fresh repair pass.
		checkpoint = ""
		if err := s.resetBackfillCheckpoint(ctx, interestRebuildTask); err != nil {
			return err
		}
	}

	users, err := s.users.List(ctx)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })

	processed := 0
	for userIdx, user := range users {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		store, err := s.stores.ForUser(ctx, user.ID)
		if err != nil {
			s.logger.Warn("interest rebuild: open user store failed", "user_id", user.ID, "error", err)
			continue
		}
		profiles, err := store.ListProfiles(ctx)
		if err != nil {
			s.logger.Warn("interest rebuild: list profiles failed", "user_id", user.ID, "error", err)
			continue
		}
		sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })

		for _, profile := range profiles {
			key := backfillKey(user.ID, profile.ID)
			if checkpoint != "" && key <= checkpoint {
				continue
			}
			if err := s.rebuildProfileInterest(ctx, store, user.ID, profile.ID); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				s.logger.Warn("interest rebuild: profile failed",
					"user_id", user.ID, "profile_id", profile.ID, "error", err)
			}
			if err := s.saveBackfillCheckpoint(ctx, interestRebuildTask, key); err != nil {
				return err
			}
			processed++
		}
		report((userIdx+1)*100/max(len(users), 1),
			fmt.Sprintf("Rebuilt interest for %d profiles", processed))
	}

	if err := s.completeBackfillCheckpoint(ctx, interestRebuildTask); err != nil {
		return err
	}
	s.logger.Info("interest rebuild completed", "profiles", processed)
	return nil
}

// rebuildProfileInterest recomputes every series the profile has any
// relationship with (favorites, watchlist, progress, completed watch
// history), plus every series that already has interest rows in Postgres —
// rows whose sources were all removed must be recomputed too, or the
// drift-repair pass would keep notifying about unfollowed shows forever.
func (s *System) rebuildProfileInterest(ctx context.Context, store userstore.UserStore, userID int, profileID string) error {
	const pageSize = 500
	itemIDs := make(map[string]struct{}, 64)

	for offset := 0; ; offset += pageSize {
		favorites, err := store.ListFavorites(ctx, profileID, pageSize, offset)
		if err != nil {
			return fmt.Errorf("list favorites: %w", err)
		}
		for _, favorite := range favorites {
			itemIDs[favorite.MediaItemID] = struct{}{}
		}
		if len(favorites) < pageSize {
			break
		}
	}
	for offset := 0; ; offset += pageSize {
		watchlist, err := store.ListWatchlist(ctx, profileID, pageSize, offset)
		if err != nil {
			return fmt.Errorf("list watchlist: %w", err)
		}
		for _, entry := range watchlist {
			itemIDs[entry.MediaItemID] = struct{}{}
		}
		if len(watchlist) < pageSize {
			break
		}
	}
	for offset := 0; ; offset += pageSize {
		progress, err := store.ListProgress(ctx, profileID, "", pageSize, offset)
		if err != nil {
			return fmt.Errorf("list progress: %w", err)
		}
		for _, entry := range progress {
			itemIDs[entry.MediaItemID] = struct{}{}
		}
		if len(progress) < pageSize {
			break
		}
	}
	// History imports (watch providers, history import runs) may record
	// watched episodes without any progress row; they are watch relationships
	// all the same.
	for offset := 0; ; offset += pageSize {
		history, err := store.ListCompletedHistory(ctx, userstore.CompletedHistoryQuery{
			ProfileID: profileID,
			Limit:     pageSize,
			Offset:    offset,
		})
		if err != nil {
			return fmt.Errorf("list completed history: %w", err)
		}
		for _, entry := range history {
			itemIDs[entry.MediaItemID] = struct{}{}
		}
		if len(history) < pageSize {
			break
		}
	}

	seriesIDs, err := s.batchResolveSeries(ctx, itemIDs)
	if err != nil {
		return err
	}
	existing, err := s.Interests.ListSeriesForProfile(ctx, profileID)
	if err != nil {
		return err
	}
	for _, seriesID := range existing {
		seriesIDs[seriesID] = struct{}{}
	}
	for seriesID := range seriesIDs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.Interest.RecomputeSeries(ctx, userID, profileID, seriesID); err != nil {
			s.logger.Warn("interest rebuild: series recompute failed",
				"user_id", userID, "profile_id", profileID, "series_id", seriesID, "error", err)
		}
	}
	return nil
}

// batchResolveSeries maps item IDs (episodes, seasons, series) to their
// series IDs; movies resolve to nothing.
func (s *System) batchResolveSeries(ctx context.Context, itemIDs map[string]struct{}) (map[string]struct{}, error) {
	ids := make([]string, 0, len(itemIDs))
	for id := range itemIDs {
		ids = append(ids, id)
	}
	seriesIDs := make(map[string]struct{}, len(ids))
	const chunkSize = 500
	for start := 0; start < len(ids); start += chunkSize {
		end := min(start+chunkSize, len(ids))
		rows, err := s.pool.Query(ctx, `
			SELECT series_id FROM episodes WHERE content_id = ANY($1)
			UNION
			SELECT series_id FROM seasons WHERE content_id = ANY($1)
			UNION
			SELECT content_id FROM media_items WHERE content_id = ANY($1) AND type = 'series'`,
			ids[start:end])
		if err != nil {
			return nil, fmt.Errorf("resolve series ids: %w", err)
		}
		for rows.Next() {
			var seriesID string
			if err := rows.Scan(&seriesID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan series id: %w", err)
			}
			if seriesID != "" {
				seriesIDs[seriesID] = struct{}{}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return seriesIDs, nil
}

// RetentionStats reports what a retention pass removed.
type RetentionStats struct {
	DeliveriesDeleted        int64
	EventsDeleted            int64
	StaleEventsDeleted       int64
	InterestPruned           int64
	WebhookAttemptsDeleted   int64
	WebPushAttemptsDeleted   int64
	DiscordLinkStatesDeleted int64
}

// RunRetention applies the retention policy: read deliveries past the read
// window, unread past the unread window, processed release events past the
// debug window, unprocessed release events past the fanout staleness horizon,
// and inert interest rows.
func (s *System) RunRetention(ctx context.Context) (RetentionStats, error) {
	var stats RetentionStats
	now := time.Now().UTC()
	readCutoff := now.AddDate(0, 0, -s.Settings.ReadRetentionDays(ctx))
	unreadCutoff := now.AddDate(0, 0, -s.Settings.UnreadRetentionDays(ctx))
	eventCutoff := now.AddDate(0, 0, -s.Settings.EventRetentionDays(ctx))

	deleted, err := s.Deliveries.DeleteOld(ctx, readCutoff, unreadCutoff)
	if err != nil {
		return stats, fmt.Errorf("prune deliveries: %w", err)
	}
	stats.DeliveriesDeleted = deleted

	events, err := s.Releases.DeleteProcessedBefore(ctx, eventCutoff)
	if err != nil {
		return stats, fmt.Errorf("prune release events: %w", err)
	}
	stats.EventsDeleted = events

	// Unprocessed events accumulate without bound when fanout is disabled
	// while availability detection keeps emitting; the fanout worker would
	// suppress them as stale anyway, so retention reclaims them directly.
	staleEvents, err := s.Releases.DeleteUnprocessedBefore(ctx, now.Add(-s.Settings.MaxEventAge(ctx)))
	if err != nil {
		return stats, fmt.Errorf("prune stale release events: %w", err)
	}
	stats.StaleEventsDeleted = staleEvents

	pruned, err := s.Interests.PruneInert(ctx)
	if err != nil {
		return stats, fmt.Errorf("prune interest rows: %w", err)
	}
	stats.InterestPruned = pruned

	if s.webhookRepo != nil {
		attempts, err := s.webhookRepo.DeleteOldAttempts(ctx, now)
		if err != nil {
			return stats, fmt.Errorf("prune webhook attempts: %w", err)
		}
		stats.WebhookAttemptsDeleted = attempts
	}
	if s.webPushRepo != nil {
		attempts, err := s.webPushRepo.DeleteOldAttempts(ctx, now)
		if err != nil {
			return stats, fmt.Errorf("prune web push attempts: %w", err)
		}
		stats.WebPushAttemptsDeleted = attempts
	}
	if s.DiscordPrefs != nil {
		states, err := s.DiscordPrefs.DeleteExpiredLinkStates(ctx)
		if err != nil {
			return stats, fmt.Errorf("prune discord link states: %w", err)
		}
		stats.DiscordLinkStatesDeleted = states
	}
	return stats, nil
}

func backfillKey(userID int, profileID string) string {
	return fmt.Sprintf("%010d|%s", userID, profileID)
}

func (s *System) loadBackfillCheckpoint(ctx context.Context, task string) (string, *time.Time, error) {
	var key *string
	var completedAt *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT last_processed_key, completed_at FROM notification_backfill_state WHERE task = $1`,
		task,
	).Scan(&key, &completedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("load backfill checkpoint: %w", err)
	}
	checkpoint := ""
	if key != nil {
		checkpoint = *key
	}
	return checkpoint, completedAt, nil
}

func (s *System) saveBackfillCheckpoint(ctx context.Context, task, key string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO notification_backfill_state (task, last_processed_key, started_at, updated_at)
		VALUES ($1, $2, now(), now())
		ON CONFLICT (task) DO UPDATE SET
			last_processed_key = EXCLUDED.last_processed_key,
			updated_at = now()`,
		task, key)
	if err != nil {
		return fmt.Errorf("save backfill checkpoint: %w", err)
	}
	return nil
}

func (s *System) resetBackfillCheckpoint(ctx context.Context, task string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO notification_backfill_state (task, last_processed_key, started_at, updated_at, completed_at)
		VALUES ($1, NULL, now(), now(), NULL)
		ON CONFLICT (task) DO UPDATE SET
			last_processed_key = NULL,
			started_at = now(),
			updated_at = now(),
			completed_at = NULL`,
		task)
	if err != nil {
		return fmt.Errorf("reset backfill checkpoint: %w", err)
	}
	return nil
}

func (s *System) completeBackfillCheckpoint(ctx context.Context, task string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE notification_backfill_state SET completed_at = now(), updated_at = now() WHERE task = $1`,
		task)
	if err != nil {
		return fmt.Errorf("complete backfill checkpoint: %w", err)
	}
	return nil
}
