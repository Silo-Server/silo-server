package notifications

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"log/slog"
	"net/mail"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/emailalerts"
	silomail "github.com/Silo-Server/silo-server/internal/mail"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	emailAlertsPollInterval = time.Minute
	// emailAlertsFetchLimit caps one leg's read; the rest waits for the
	// next run, behind the advanced watermark.
	emailAlertsFetchLimit = 500
	emailAlertsLogLimit   = 150
	// emailAlertsOpenPath and emailAlertsManagePath are the public pages an
	// email links to.
	emailAlertsOpenPath   = "/api/v2/notifications/email/open"
	emailAlertsManagePath = "/api/v2/notifications/email/manage"
	// emailAlertsProfileSettingsPath is the web page a profile manages its
	// alerts on.
	emailAlertsProfileSettingsPath = "/settings/notifications"
)

// EmailAlerts sends the new-media emails: every recipient leg (instant,
// daily, weekly) keeps a watermark over what it has been sent, and each run
// emails whatever is due.
type EmailAlerts struct {
	repo     *EmailAlertsRepository
	pool     *pgxpool.Pool
	settings *Settings
	sender   silomail.Sender
	brand    *silomail.BrandLoader
	scopes   ScopeResolver
	artwork  *EmailArtwork
	// profileName resolves a profile's display name; best-effort.
	profileName func(ctx context.Context, userID int, profileID string) string
	logger      *slog.Logger
	now         func() time.Time
	nudge       chan struct{}

	runMu    sync.Mutex
	reportMu sync.Mutex
	report   *EmailAlertsReport
}

// EmailAlertsReport describes the last run.
type EmailAlertsReport struct {
	Time       time.Time `json:"time"`
	Reason     string    `json:"reason"`
	Recipients int       `json:"recipients"`
	Sent       int       `json:"sent"`
	Items      int       `json:"items"`
	Skipped    string    `json:"skipped,omitempty"`
	Errors     []string  `json:"errors,omitempty"`
}

func newEmailAlerts(pool *pgxpool.Pool, settings *Settings, sender silomail.Sender, brand *silomail.BrandLoader, scopes ScopeResolver) *EmailAlerts {
	return &EmailAlerts{
		repo:     NewEmailAlertsRepository(pool),
		pool:     pool,
		settings: settings,
		sender:   sender,
		brand:    brand,
		scopes:   scopes,
		logger:   slog.Default().With("component", "notifications.email_alerts"),
		now:      time.Now,
		nudge:    make(chan struct{}, 1),
	}
}

// Repo exposes the settings and recipient storage.
func (a *EmailAlerts) Repo() *EmailAlertsRepository { return a.repo }

// setArtwork links posters, stills and backdrops; dimmed backdrops follow
// the theme's card colour and dim.
func (a *EmailAlerts) setArtwork(artwork *EmailArtwork) {
	a.artwork = artwork
	artwork.SetBackdropStyle(func(ctx context.Context) (color.RGBA, float64) {
		s, err := a.repo.Settings(ctx)
		if err != nil {
			return emailArtworkTint, emailBackdropDim
		}
		tint, ok := parseHexColor(s.Theme.CardColor)
		if !ok {
			tint = emailArtworkTint
		}
		return tint, float64(s.Theme.BackdropDim) / 100
	})
}

func parseHexColor(s string) (color.RGBA, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return color.RGBA{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.RGBA{}, false
	}
	return color.RGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 0xff}, true
}

// Nudge schedules a run soon, for new deliveries. Non-blocking.
func (a *EmailAlerts) Nudge() {
	select {
	case a.nudge <- struct{}{}:
	default:
	}
}

// Run sends due emails every minute until ctx is canceled.
func (a *EmailAlerts) Run(ctx context.Context) {
	ticker := time.NewTicker(emailAlertsPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-a.nudge:
			select {
			case <-ctx.Done():
				return
			case <-time.After(channelNudgeDelay):
			}
		}
		a.RunDue(ctx, "scheduled")
	}
}

// Available reports whether email can be sent at all.
func (a *EmailAlerts) Available(ctx context.Context) bool {
	return a.settings.EmailEnabled(ctx) && a.sender.Enabled(ctx)
}

// Report returns the last run's report, or nil before the first.
func (a *EmailAlerts) Report() *EmailAlertsReport {
	a.reportMu.Lock()
	defer a.reportMu.Unlock()
	return a.report
}

// recipientActive applies the per-recipient and self-service switches.
func recipientActive(r EmailAlertRecipient, s emailalerts.Settings) bool {
	if !r.Enabled || r.Email == "" || !emailalerts.ValidMode(r.Mode) {
		return false
	}
	if r.Kind == EmailRecipientProfile && !s.SelfService.Enabled {
		return false
	}
	return true
}

type emailAlertJob struct {
	rec   EmailAlertRecipient
	leg   string
	state EmailAlertState
	since Cursor
}

// RunDue sends every email that is due.
func (a *EmailAlerts) RunDue(ctx context.Context, reason string) *EmailAlertsReport {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	now := a.now()
	report := &EmailAlertsReport{Time: now, Reason: reason}
	defer func() {
		if reason != "scheduled" || report.Sent > 0 || len(report.Errors) > 0 {
			a.reportMu.Lock()
			a.report = report
			a.reportMu.Unlock()
		}
	}()

	if !a.Available(ctx) {
		report.Skipped = "Email is switched off or outgoing mail isn't set up."
		return report
	}
	s, err := a.repo.Settings(ctx)
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
		return report
	}
	if s.Delivery.Paused {
		report.Skipped = "Sending is paused."
		return report
	}
	recipients, err := a.repo.Recipients(ctx)
	if err != nil {
		report.Errors = append(report.Errors, err.Error())
		return report
	}

	cutoff := now.Add(-time.Duration(s.Delivery.SettleMinutes) * time.Minute)
	var jobs []emailAlertJob
	for _, rec := range recipients {
		if !recipientActive(rec, s) {
			continue
		}
		legs := rec.legs()
		if !a.settings.EmailAllowPerEpisode(ctx) && slices.Contains(legs, emailalerts.LegInstant) {
			// The admin allowance still applies: instant becomes a digest.
			legs = slices.DeleteFunc(slices.Clone(legs), func(l string) bool { return l == emailalerts.LegInstant })
			if len(legs) == 0 {
				legs = []string{emailalerts.LegDaily}
			}
		}
		for _, leg := range legs {
			st, found, err := a.repo.State(ctx, rec.Key(), leg)
			if err != nil {
				report.Errors = append(report.Errors, err.Error())
				continue
			}
			if !found {
				// A new recipient starts from now rather than receiving the
				// back catalogue.
				st = EmailAlertState{Watermark: Cursor{CreatedAt: cutoff}, LastRunAt: &now}
				if err := a.repo.SaveState(ctx, rec.Key(), leg, st); err != nil {
					report.Errors = append(report.Errors, err.Error())
				}
				continue
			}
			if st.NextAttemptAt != nil && now.Before(*st.NextAttemptAt) {
				continue
			}
			if leg == emailalerts.LegInstant {
				if !s.InstantHeldUntil(instantState(st), now).IsZero() {
					continue // rate limited: items wait and go out together
				}
			} else {
				var last time.Time
				if st.LastRunAt != nil {
					last = *st.LastRunAt
				}
				if !s.DigestDue(leg, last, now) {
					continue
				}
			}
			// Never reach back past the recipient's idea of "new", even when
			// the watermark is old (an outage, a rewind, a stale state).
			since := st.Watermark
			if floor := now.Add(-s.MaxAge(rec.MaxAgeHours, leg)); since.CreatedAt.Before(floor) {
				since = Cursor{CreatedAt: floor}
			}
			jobs = append(jobs, emailAlertJob{rec: rec, leg: leg, state: st, since: since})
		}
	}
	report.Recipients = len(jobs)

	base := a.settings.EmailExternalURL(ctx)
	var logs []EmailAlertLogEntry
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		refs, err := a.collect(ctx, job.rec, job.since, cutoff)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: reading new items: %v", job.rec.Email, err))
			continue
		}
		st := job.state
		st.LastRunAt = &now
		if len(refs) == 0 {
			if err := a.repo.SaveState(ctx, job.rec.Key(), job.leg, st); err != nil {
				report.Errors = append(report.Errors, err.Error())
			}
			continue
		}
		last := refs[len(refs)-1].cursor
		refs = dedupeAlertItems(refs)
		more := 0
		if len(refs) > s.Delivery.MaxItems {
			more = len(refs) - s.Delivery.MaxItems
			refs = refs[len(refs)-s.Delivery.MaxItems:]
		}
		entries, err := a.loadEntries(ctx, refs, base)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s: reading item details: %v", job.rec.Email, err))
			continue
		}
		if len(entries) == 0 {
			// Everything was filtered out or has left the catalog.
			st.Watermark = maxCursor(st.Watermark, last)
			if err := a.repo.SaveState(ctx, job.rec.Key(), job.leg, st); err != nil {
				report.Errors = append(report.Errors, err.Error())
			}
			continue
		}
		report.Items += len(entries) + more
		env := a.env(ctx, s, base, job.rec, emailalerts.RenderMode(job.leg), now)
		env.MoreCount = more
		subject, err := a.deliver(ctx, env, job.rec.Email, entries, base)
		entry := EmailAlertLogEntry{SentAt: now, RecipientKey: job.rec.Key(), Email: job.rec.Email, Leg: job.leg, Items: len(entries) + more, Subject: subject}
		if err != nil {
			entry.Error = err.Error()
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", job.rec.Email, err))
			st.Failures++
			next := now.Add(time.Duration(1<<min(st.Failures, 6)) * time.Minute)
			st.NextAttemptAt = &next
			st.LastRunAt = job.state.LastRunAt // retry the digest window
		} else {
			report.Sent++
			st.Failures = 0
			st.NextAttemptAt = nil
			st.LastSentAt = &now
			st.Watermark = maxCursor(st.Watermark, last)
			if job.leg == emailalerts.LegInstant {
				st.LastInstantAt = &now
				if day := s.Day(now); st.Day == day {
					st.DayCount++
				} else {
					st.Day, st.DayCount = day, 1
				}
			}
		}
		if err := a.repo.SaveState(ctx, job.rec.Key(), job.leg, st); err != nil {
			report.Errors = append(report.Errors, err.Error())
		}
		logs = append(logs, entry)
	}
	if len(logs) > 0 {
		if err := a.repo.AppendLog(ctx, logs...); err != nil {
			a.logger.WarnContext(ctx, "append email alert log", "error", err)
		}
	}
	if len(report.Errors) > 0 {
		a.logger.WarnContext(ctx, "email alerts run finished with errors", "reason", reason, "errors", strings.Join(report.Errors, "; "))
	} else if report.Sent > 0 {
		a.logger.InfoContext(ctx, "email alerts sent", "reason", reason, "emails", report.Sent)
	}
	return report
}

func instantState(st EmailAlertState) emailalerts.InstantState {
	out := emailalerts.InstantState{Day: st.Day, DayCount: st.DayCount}
	if st.LastInstantAt != nil {
		out.LastInstant = *st.LastInstantAt
	}
	return out
}

// alertItem is one new episode or movie a recipient may be sent, with the
// cursor of the row that announced it.
type alertItem struct {
	cursor      Cursor
	kind        string // episode | movie
	contentID   string
	seriesID    string
	libraryID   int
	libraryName string
}

// dedupeAlertItems keeps the first of each content id (an episode added to
// two libraries is announced twice).
func dedupeAlertItems(items []alertItem) []alertItem {
	seen := make(map[string]bool, len(items))
	out := items[:0]
	for _, it := range items {
		if seen[it.contentID] {
			continue
		}
		seen[it.contentID] = true
		out = append(out, it)
	}
	return out
}

// collect reads a recipient's new items after since and at or before until,
// oldest first.
func (a *EmailAlerts) collect(ctx context.Context, rec EmailAlertRecipient, since Cursor, until time.Time) ([]alertItem, error) {
	types := emailalerts.CleanTypes(rec.Types)
	if types == nil {
		types = []string{}
	}
	libraries := rec.LibraryIDs
	if libraries == nil {
		libraries = []int{}
	}
	if rec.Kind == EmailRecipientProfile && rec.Content == EmailContentFollowed {
		return a.queryAlertItems(ctx, `
			SELECT d.id, d.created_at,
			       CASE d.type WHEN 'movie.available' THEN 'movie' ELSE 'episode' END,
			       COALESCE(d.episode_id, d.series_id), COALESCE(d.series_id, ''),
			       COALESCE(d.library_id, 0), COALESCE(f.name, '')
			FROM notification_deliveries d
			LEFT JOIN media_folders f ON f.id = d.library_id
			WHERE d.user_id = $1 AND d.profile_id = $2
			  AND d.type IN ('episode.available', 'movie.available')
			  AND (d.created_at, d.id) > ($3, $4) AND d.created_at <= $5
			  AND (cardinality($6::text[]) = 0
			       OR (CASE d.type WHEN 'movie.available' THEN 'movie' ELSE 'episode' END) = ANY($6))
			  AND (cardinality($7::int[]) = 0 OR d.library_id = ANY($7))
			ORDER BY d.created_at, d.id
			LIMIT $8`,
			rec.UserID, rec.ProfileID, since.CreatedAt, since.ID, until, types, libraries, emailAlertsFetchLimit)
	}
	items, err := a.queryAlertItems(ctx, `
		SELECT e.id, e.created_at, e.kind, COALESCE(e.episode_id, e.item_id), COALESCE(e.series_id, ''),
		       e.library_id, COALESCE(f.name, '')
		FROM release_events e
		LEFT JOIN media_folders f ON f.id = e.library_id
		WHERE e.suppressed_reason IS NULL AND e.kind IN ('episode', 'movie')
		  AND (e.created_at, e.id) > ($1, $2) AND e.created_at <= $3
		  AND (cardinality($4::text[]) = 0 OR e.kind = ANY($4))
		  AND (cardinality($5::int[]) = 0 OR e.library_id = ANY($5))
		ORDER BY e.created_at, e.id
		LIMIT $6`,
		since.CreatedAt, since.ID, until, types, libraries, emailAlertsFetchLimit)
	if err != nil || rec.Kind != EmailRecipientProfile || len(items) == 0 {
		return items, err
	}
	// A profile only hears about what it may open. The cursor still passes
	// the rest, so the last item keeps its place even when filtered out.
	return a.filterOpenable(ctx, rec, items)
}

func (a *EmailAlerts) queryAlertItems(ctx context.Context, sql string, args ...any) ([]alertItem, error) {
	rows, err := a.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []alertItem
	for rows.Next() {
		var it alertItem
		if err := rows.Scan(&it.cursor.ID, &it.cursor.CreatedAt, &it.kind, &it.contentID, &it.seriesID,
			&it.libraryID, &it.libraryName); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// filterOpenable drops items the profile may not open. The newest item's
// cursor is kept on a placeholder-free result by returning it last even when
// it is dropped: callers advance the watermark to the last element.
func (a *EmailAlerts) filterOpenable(ctx context.Context, rec EmailAlertRecipient, items []alertItem) ([]alertItem, error) {
	if a.scopes == nil {
		return nil, nil
	}
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	access := newRecipientAccess(a.scopes)
	out := make([]alertItem, 0, len(items))
	for _, it := range items {
		id := it.contentID
		if it.kind == "episode" && it.seriesID != "" {
			id = it.seriesID
		}
		ok, err := access.canOpen(ctx, tx, rec.UserID, rec.ProfileID, id, it.libraryID)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, it)
		}
	}
	if len(out) == 0 || out[len(out)-1].cursor != items[len(items)-1].cursor {
		// Nothing (or not the newest) survived: keep the watermark moving
		// with a marker the caller drops after reading its cursor.
		out = append(out, alertItem{cursor: items[len(items)-1].cursor})
	}
	return out, nil
}

// loadEntries reads the details the templates show for each item. Items
// without a content id (cursor markers) and items no longer in the catalog
// are skipped.
func (a *EmailAlerts) loadEntries(ctx context.Context, items []alertItem, base string) ([]emailalerts.Entry, error) {
	var episodeIDs, titleIDs []string
	for _, it := range items {
		switch {
		case it.contentID == "":
		case it.kind == "episode":
			episodeIDs = append(episodeIDs, it.contentID)
			if it.seriesID != "" {
				titleIDs = append(titleIDs, it.seriesID)
			}
		default:
			titleIDs = append(titleIDs, it.contentID)
		}
	}
	episodes, err := a.loadEpisodes(ctx, episodeIDs, base)
	if err != nil {
		return nil, err
	}
	for _, ep := range episodes {
		if ep.SeriesID != "" && !slices.Contains(titleIDs, ep.SeriesID) {
			titleIDs = append(titleIDs, ep.SeriesID)
		}
	}
	titles, err := a.loadTitles(ctx, titleIDs, base)
	if err != nil {
		return nil, err
	}
	var out []emailalerts.Entry
	for _, it := range items {
		lib := emailalerts.LibraryRef{ID: strconv.Itoa(it.libraryID), Name: it.libraryName}
		switch {
		case it.contentID == "":
		case it.kind == "episode":
			ep, ok := episodes[it.contentID]
			if !ok {
				continue
			}
			card := ep.Card
			card.AddedAt = it.cursor.CreatedAt
			entry := emailalerts.Entry{Card: card, Library: lib, Episode: &ep}
			if series, ok := titles[ep.SeriesID]; ok {
				entry.Series = &series
				entry.Card.SeriesTitle = series.Title
				if entry.Card.PosterURL == "" {
					entry.Card.PosterURL = series.PosterURL
				}
			}
			out = append(out, entry)
		default:
			movie, ok := titles[it.contentID]
			if !ok {
				continue
			}
			card := movie.Card
			card.AddedAt = it.cursor.CreatedAt
			out = append(out, emailalerts.Entry{Card: card, Library: lib, Movie: &movie})
		}
	}
	return out, nil
}

func (a *EmailAlerts) artworkURL(base, kind, contentID string, has bool) string {
	if !has || a.artwork == nil {
		return ""
	}
	return a.artwork.URL(base, kind, contentID)
}

func (a *EmailAlerts) loadEpisodes(ctx context.Context, ids []string, base string) (map[string]emailalerts.Detail, error) {
	out := make(map[string]emailalerts.Detail, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := a.pool.Query(ctx, `
		SELECT content_id, series_id, season_number, episode_number, COALESCE(title, ''),
		       COALESCE(overview, ''), COALESCE(air_date::text, ''), COALESCE(runtime, 0),
		       COALESCE(rating_tmdb, 0), COALESCE(rating_imdb, 0),
		       COALESCE(still_path, '') <> '' OR COALESCE(still_source_path, '') <> ''
		FROM episodes WHERE content_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("load episodes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d emailalerts.Detail
		var season, episode int
		var hasStill bool
		if err := rows.Scan(&d.ContentID, &d.SeriesID, &season, &episode, &d.Title, &d.Overview,
			&d.AirDate, &d.Runtime, &d.RatingTMDB, &d.RatingIMDB, &hasStill); err != nil {
			return nil, err
		}
		d.Type = "episode"
		d.SeasonNumber, d.EpisodeNumber = &season, &episode
		d.ReleaseDate = d.AirDate
		if still := a.artworkURL(base, EmailArtworkStill, d.ContentID, hasStill); still != "" {
			d.BackdropURL = still
			d.BackdropIsEpisodeStill = true
		}
		out[d.ContentID] = d
	}
	return out, rows.Err()
}

func (a *EmailAlerts) loadTitles(ctx context.Context, ids []string, base string) (map[string]emailalerts.Detail, error) {
	out := make(map[string]emailalerts.Detail, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := a.pool.Query(ctx, `
		SELECT content_id, type, COALESCE(title, ''), COALESCE(year, 0), COALESCE(overview, ''),
		       COALESCE(genres, '{}'), COALESCE(content_rating, ''), COALESCE(runtime, 0),
		       COALESCE(tagline, ''), COALESCE(rating_tmdb, 0), COALESCE(rating_imdb, 0),
		       COALESCE(studios, '{}'), COALESCE(networks, '{}'), COALESCE(release_date::text, ''),
		       COALESCE(first_air_date, ''), COALESCE(season_count, 0), COALESCE(show_status, ''),
		       COALESCE(poster_path, '') <> '' OR COALESCE(poster_source_path, '') <> '',
		       COALESCE(backdrop_path, '') <> '' OR COALESCE(backdrop_source_path, '') <> ''
		FROM media_items WHERE content_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("load titles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d emailalerts.Detail
		var hasPoster, hasBackdrop bool
		if err := rows.Scan(&d.ContentID, &d.Type, &d.Title, &d.Year, &d.Overview, &d.Genres,
			&d.ContentRating, &d.Runtime, &d.Tagline, &d.RatingTMDB, &d.RatingIMDB, &d.Studios,
			&d.Networks, &d.ReleaseDate, &d.FirstAirDate, &d.SeasonCount, &d.ShowStatus,
			&hasPoster, &hasBackdrop); err != nil {
			return nil, err
		}
		d.PosterURL = a.artworkURL(base, EmailArtworkPoster, d.ContentID, hasPoster)
		d.BackdropURL = a.artworkURL(base, EmailArtworkWide, d.ContentID, hasBackdrop)
		out[d.ContentID] = d
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	return out, a.loadCast(ctx, ids, out)
}

// loadCast adds up to four billed actors to each title.
func (a *EmailAlerts) loadCast(ctx context.Context, ids []string, titles map[string]emailalerts.Detail) error {
	rows, err := a.pool.Query(ctx, `
		SELECT ip.content_id, p.name, ip.character
		FROM (
			SELECT content_id, person_id, character,
			       row_number() OVER (PARTITION BY content_id ORDER BY sort_order, id) AS n
			FROM item_people WHERE content_id = ANY($1) AND kind = 1
		) ip
		JOIN people p ON p.id = ip.person_id
		WHERE ip.n <= 4
		ORDER BY ip.content_id, ip.n`, ids)
	if err != nil {
		return fmt.Errorf("load cast: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var member emailalerts.CastMember
		if err := rows.Scan(&id, &member.Name, &member.Character); err != nil {
			return err
		}
		if d, ok := titles[id]; ok {
			d.Cast = append(d.Cast, member)
			titles[id] = d
		}
	}
	return rows.Err()
}

// env builds the render environment for one recipient.
func (a *EmailAlerts) env(ctx context.Context, s emailalerts.Settings, base string, rec EmailAlertRecipient, mode string, now time.Time) emailalerts.RenderEnv {
	name := rec.Name
	if rec.Kind == EmailRecipientProfile && a.profileName != nil {
		name = a.profileName(ctx, rec.UserID, rec.ProfileID)
	}
	env := emailalerts.RenderEnv{
		Settings:       s,
		ServerName:     a.serverName(ctx),
		PublicURL:      base,
		RecipientName:  name,
		RecipientEmail: rec.Email,
		Mode:           mode,
		Now:            now,
	}
	if base != "" {
		env.OpenURL = base + emailAlertsOpenPath
		if rec.ManageToken != "" {
			env.ManageURL = base + emailAlertsManagePath + "?token=" + rec.ManageToken
			env.UnsubscribeURL = emailUnsubscribeURL(base, rec.ManageToken)
		}
		// A profile's alerts are managed in its own notification settings,
		// which the token page cannot change; admin recipients have no login.
		if rec.Kind == EmailRecipientProfile {
			env.ManageURL = base + emailAlertsProfileSettingsPath
		}
		if s.Theme.BackdropBehindShow && a.artwork != nil {
			env.BackdropBG = func(id string) string { return a.artwork.URL(base, EmailArtworkBackdrop, id) }
		}
	}
	return env
}

func (a *EmailAlerts) serverName(ctx context.Context) string {
	if a.brand != nil {
		if name := a.brand.Load(ctx).Name; name != "" {
			return name
		}
	}
	return "Silo"
}

// fetcher embeds this server's email artwork; other URLs are left linked.
func (a *EmailAlerts) fetcher(base string) func(context.Context, string) ([]byte, string, error) {
	prefix := strings.TrimRight(base, "/") + emailArtworkRoute
	return func(ctx context.Context, raw string) ([]byte, string, error) {
		if a.artwork == nil || base == "" || !strings.HasPrefix(raw, prefix) {
			return nil, "", errors.New("not server artwork")
		}
		data, err := a.artwork.Serve(ctx, strings.TrimPrefix(raw, prefix))
		if err != nil {
			a.logger.WarnContext(ctx, "email artwork failed", "error", err)
			return nil, "", err
		}
		return data, "image/jpeg", nil
	}
}

// deliver renders and sends one email, returning its subject.
func (a *EmailAlerts) deliver(ctx context.Context, env emailalerts.RenderEnv, to string, entries []emailalerts.Entry, base string) (string, error) {
	rendered, err := emailalerts.Render(ctx, env, entries, a.fetcher(base))
	if err != nil {
		return "", err
	}
	msg := silomail.Message{
		To:       []string{to},
		Subject:  rendered.Subject,
		TextBody: rendered.Text,
		HTMLBody: rendered.HTML,
	}
	for i, img := range rendered.Images {
		msg.Inline = append(msg.Inline, silomail.InlineImage{
			ContentID:   img.CID,
			Filename:    fmt.Sprintf("image%d.jpg", i+1),
			ContentType: img.ContentType,
			Data:        img.Data,
		})
	}
	if env.UnsubscribeURL != "" {
		// RFC 8058 one-click unsubscribe; the POST target is the same URL.
		msg.Headers = map[string]string{
			"List-Unsubscribe":      "<" + env.UnsubscribeURL + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		}
	}
	if err := a.sender.Send(ctx, msg); err != nil {
		return rendered.Subject, err
	}
	return rendered.Subject, nil
}

// latest returns the newest few movies and episodes, for previews and tests.
func (a *EmailAlerts) latest(ctx context.Context, perType int, base string) ([]emailalerts.Entry, error) {
	items, err := a.queryAlertItems(ctx, `
		(SELECT e.id, e.created_at, e.kind, e.item_id, '', e.library_id, COALESCE(f.name, '')
		 FROM release_events e LEFT JOIN media_folders f ON f.id = e.library_id
		 WHERE e.kind = 'movie' AND e.suppressed_reason IS NULL
		 ORDER BY e.created_at DESC LIMIT $1)
		UNION ALL
		(SELECT e.id, e.created_at, e.kind, e.episode_id, e.series_id, e.library_id, COALESCE(f.name, '')
		 FROM release_events e LEFT JOIN media_folders f ON f.id = e.library_id
		 WHERE e.kind = 'episode' AND e.suppressed_reason IS NULL
		 ORDER BY e.created_at DESC LIMIT $1)`, perType)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(items, func(x, y alertItem) int { return x.cursor.CreatedAt.Compare(y.cursor.CreatedAt) })
	return a.loadEntries(ctx, dedupeAlertItems(items), base)
}

// Preview renders s for the admin page, with the newest items (or sample
// content when there are none, or when source is "sample").
func (a *EmailAlerts) Preview(ctx context.Context, s emailalerts.Settings, mode, source string) (*emailalerts.Rendered, string, error) {
	s.Normalize()
	if mode == "" {
		mode = emailalerts.ModeInstant
	}
	base := a.settings.EmailExternalURL(ctx)
	note := ""
	var entries []emailalerts.Entry
	if source != "sample" {
		latest, err := a.latest(ctx, 3, base)
		if err != nil {
			note = "Couldn't read the newest items: " + err.Error()
		}
		entries = latest
	}
	if len(entries) == 0 {
		entries = emailalerts.SampleEntries()
		if note == "" && source != "sample" {
			note = "Nothing has been added yet, so the preview uses sample content."
		}
	}
	env := a.env(ctx, s, base, EmailAlertRecipient{Name: "Alex Example", Email: "alex@example.com", ManageToken: "preview"}, mode, a.now())
	env.Preview = true
	rendered, err := emailalerts.Render(ctx, env, entries, nil)
	return rendered, note, err
}

// SendTest emails the newest items to one address with s (or the saved
// settings when s is nil). pick "episode" or "movie" narrows it to the
// newest one of that type.
func (a *EmailAlerts) SendTest(ctx context.Context, s *emailalerts.Settings, to, mode, pick string) (string, int, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(to))
	if err != nil {
		return "", 0, fmt.Errorf("%q isn't a valid email address", to)
	}
	if !a.Available(ctx) {
		return "", 0, errors.New("email is switched off or outgoing mail isn't set up")
	}
	settings := emailalerts.Settings{}
	if s != nil {
		settings = *s
		settings.Normalize()
	} else if settings, err = a.repo.Settings(ctx); err != nil {
		return "", 0, err
	}
	if mode == "" {
		mode = emailalerts.ModeInstant
	}
	base := a.settings.EmailExternalURL(ctx)
	entries, err := a.latest(ctx, 3, base)
	if err != nil {
		return "", 0, err
	}
	if len(entries) == 0 {
		entries = emailalerts.SampleEntries()
	}
	if pick == "episode" || pick == "movie" {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Card.Type == pick {
				entries = entries[i : i+1]
				break
			}
		}
	}
	env := a.env(ctx, settings, base, EmailAlertRecipient{Email: addr.Address}, mode, a.now())
	subject, err := a.deliver(ctx, env, addr.Address, entries, base)
	a.logTest(ctx, addr.Address, subject, len(entries), err)
	return subject, len(entries), err
}

func (a *EmailAlerts) logTest(ctx context.Context, to, subject string, items int, sendErr error) {
	entry := EmailAlertLogEntry{SentAt: a.now(), RecipientKey: "test", Email: to, Leg: "test", Items: items, Subject: subject}
	if sendErr != nil {
		entry.Error = sendErr.Error()
	}
	if err := a.repo.AppendLog(ctx, entry); err != nil {
		a.logger.WarnContext(ctx, "append email alert log", "error", err)
	}
}

// EmailAlertRecipientView is a recipient with its send status, for pages.
type EmailAlertRecipientView struct {
	EmailAlertRecipient
	Key         string     `json:"key"`
	Active      bool       `json:"active"`
	LastSent    *time.Time `json:"last_sent,omitempty"`
	Failures    int        `json:"failures,omitempty"`
	NextAttempt *time.Time `json:"next_attempt,omitempty"`
	HeldUntil   *time.Time `json:"held_until,omitempty"`
	NextDue     *time.Time `json:"next_due,omitempty"`
}

// EmailAlertsState is everything the admin page shows.
type EmailAlertsState struct {
	Settings      emailalerts.Settings       `json:"settings"`
	Defaults      emailalerts.Settings       `json:"defaults"`
	Recipients    []EmailAlertRecipientView  `json:"recipients"`
	Libraries     []EmailAlertLibrary        `json:"libraries"`
	MaxAgeOptions []emailalerts.MaxAgeOption `json:"max_age_options"`
	Problems      []string                   `json:"problems"`
	Report        *EmailAlertsReport         `json:"report"`
	Log           []EmailAlertLogEntry       `json:"log"`
	PublicURL     string                     `json:"public_url"`
	ServerName    string                     `json:"server_name"`
	SMTPHost      string                     `json:"smtp_host"`
	FromAddress   string                     `json:"from_address"`
	Now           time.Time                  `json:"now"`
}

// State gathers the admin page's data.
func (a *EmailAlerts) State(ctx context.Context) (*EmailAlertsState, error) {
	s, err := a.repo.Settings(ctx)
	if err != nil {
		return nil, err
	}
	recipients, err := a.repo.Recipients(ctx)
	if err != nil {
		return nil, err
	}
	now := a.now()
	views := make([]EmailAlertRecipientView, 0, len(recipients))
	for _, rec := range recipients {
		if rec.Kind == EmailRecipientProfile && a.profileName != nil {
			rec.Name = a.profileName(ctx, rec.UserID, rec.ProfileID)
		}
		v := EmailAlertRecipientView{EmailAlertRecipient: rec, Key: rec.Key(), Active: recipientActive(rec, s)}
		for _, leg := range rec.legs() {
			st, found, err := a.repo.State(ctx, rec.Key(), leg)
			if err != nil || !found {
				continue
			}
			if st.LastSentAt != nil && (v.LastSent == nil || st.LastSentAt.After(*v.LastSent)) {
				v.LastSent = st.LastSentAt
			}
			v.Failures = max(v.Failures, st.Failures)
			if st.NextAttemptAt != nil && now.Before(*st.NextAttemptAt) {
				v.NextAttempt = st.NextAttemptAt
			}
			if leg == emailalerts.LegInstant {
				if held := s.InstantHeldUntil(instantState(st), now); !held.IsZero() {
					v.HeldUntil = &held
				}
			}
		}
		for _, leg := range rec.legs() {
			next := s.NextDue(map[string]string{emailalerts.LegDaily: emailalerts.ModeDaily, emailalerts.LegWeekly: emailalerts.ModeWeekly}[leg], now)
			if !next.IsZero() && (v.NextDue == nil || next.Before(*v.NextDue)) {
				v.NextDue = &next
			}
		}
		views = append(views, v)
	}
	libraries, err := a.repo.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	log, err := a.repo.Log(ctx, emailAlertsLogLimit)
	if err != nil {
		return nil, err
	}
	var problems []string
	if !a.settings.EmailEnabled(ctx) {
		problems = append(problems, "Email notifications are switched off in Notifications settings.")
	}
	if !a.sender.Enabled(ctx) {
		problems = append(problems, "Outgoing mail (SMTP) isn't set up.")
	}
	base := a.settings.EmailExternalURL(ctx)
	if base == "" {
		problems = append(problems, "No public server URL is set, so emails can't link to items or show artwork.")
	}
	return &EmailAlertsState{
		Settings:      s,
		Defaults:      emailalerts.DefaultSettings(),
		Recipients:    views,
		Libraries:     libraries,
		MaxAgeOptions: emailalerts.MaxAgeOptions(s),
		Problems:      problems,
		Report:        a.Report(),
		Log:           log,
		PublicURL:     base,
		ServerName:    a.serverName(ctx),
		SMTPHost:      a.settings.raw(ctx, "email.smtp_host"),
		FromAddress:   a.settings.raw(ctx, "email.from_address"),
		Now:           now,
	}, nil
}

// SaveSettings validates the templates and stores s.
func (a *EmailAlerts) SaveSettings(ctx context.Context, s emailalerts.Settings) (emailalerts.Settings, error) {
	s.Normalize()
	if err := emailalerts.ValidateTemplates(s); err != nil {
		return s, err
	}
	return a.repo.SaveSettings(ctx, s)
}

// ErrEmailRecipientInvalid wraps validation failures of a recipient.
var ErrEmailRecipientInvalid = errors.New("invalid email recipient")

// cleanRecipient validates and tidies a recipient's choices.
func cleanRecipient(rec *EmailAlertRecipient) error {
	if !emailalerts.ValidMode(rec.Mode) {
		return fmt.Errorf("%w: unknown mode %q", ErrEmailRecipientInvalid, rec.Mode)
	}
	rec.Types = emailalerts.CleanTypes(rec.Types)
	rec.MaxAgeHours = emailalerts.CleanMaxAge(rec.MaxAgeHours)
	if rec.Content != EmailContentFollowed {
		rec.Content = EmailContentAll
	}
	return nil
}

// SaveAdminRecipient creates or updates an address-only recipient.
func (a *EmailAlerts) SaveAdminRecipient(ctx context.Context, rec EmailAlertRecipient) (EmailAlertRecipient, error) {
	addr, err := mail.ParseAddress(strings.TrimSpace(rec.Email))
	if err != nil {
		return rec, fmt.Errorf("%w: %q isn't a valid email address", ErrEmailRecipientInvalid, rec.Email)
	}
	rec.Email = addr.Address
	rec.Name = strings.TrimSpace(rec.Name)
	if rec.Name == "" {
		rec.Name = addr.Name
	}
	if err := cleanRecipient(&rec); err != nil {
		return rec, err
	}
	return a.repo.SaveAdminRecipient(ctx, rec)
}

// UpdateRecipientChoices applies a recipient's own choices (from the manage
// page or the profile settings). mode "" switches the recipient off.
func (a *EmailAlerts) UpdateRecipientChoices(ctx context.Context, rec EmailAlertRecipient) error {
	if rec.Mode != "" {
		if err := cleanRecipient(&rec); err != nil {
			return err
		}
	}
	if rec.Kind == EmailRecipientProfile {
		if rec.Content != EmailContentAll {
			rec.Content = EmailContentFollowed
		}
		return a.repo.UpdateProfileRecipient(ctx, rec.UserID, rec.ProfileID, rec.Mode, rec.Content, rec.Types, rec.LibraryIDs, rec.MaxAgeHours)
	}
	if rec.Mode == "" {
		rec.Enabled = false
		current, err := a.repo.AdminRecipient(ctx, rec.ID)
		if err != nil {
			return err
		}
		rec.Mode = current.Mode
	}
	_, err := a.repo.SaveAdminRecipient(ctx, rec)
	return err
}

// Rewind makes the next run resend the last hours to a recipient.
func (a *EmailAlerts) Rewind(ctx context.Context, key string, hours int) error {
	if hours <= 0 {
		hours = 24
	}
	return a.repo.Rewind(ctx, key, a.now().Add(-time.Duration(hours)*time.Hour))
}

// Unsubscribe switches off the recipient a manage token belongs to.
func (a *EmailAlerts) Unsubscribe(ctx context.Context, token string) (bool, error) {
	rec, err := a.repo.RecipientByToken(ctx, token)
	if errors.Is(err, ErrEmailRecipientNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	rec.Mode = ""
	if err := a.UpdateRecipientChoices(ctx, rec); err != nil {
		return false, err
	}
	return true, nil
}

// RecipientByToken finds the recipient a manage link belongs to.
func (a *EmailAlerts) RecipientByToken(ctx context.Context, token string) (EmailAlertRecipient, error) {
	return a.repo.RecipientByToken(ctx, token)
}

// Settings returns the saved email settings.
func (a *EmailAlerts) Settings(ctx context.Context) (emailalerts.Settings, error) {
	return a.repo.Settings(ctx)
}

// PublicURL is the base URL emails link to, or "" when none is set.
func (a *EmailAlerts) PublicURL(ctx context.Context) string {
	return a.settings.EmailExternalURL(ctx)
}

// UpdateRecipient saves the admin page's edit of any recipient, named by
// its key: an admin recipient's whole record, or a profile's choices.
// Disabling a profile recipient switches its email off.
func (a *EmailAlerts) UpdateRecipient(ctx context.Context, key string, rec EmailAlertRecipient) (EmailAlertRecipient, error) {
	switch {
	case strings.HasPrefix(key, "r:"):
		rec.Kind = EmailRecipientAdmin
		rec.ID = strings.TrimPrefix(key, "r:")
		return a.SaveAdminRecipient(ctx, rec)
	case strings.HasPrefix(key, "p:"):
		userPart, profileID, ok := strings.Cut(strings.TrimPrefix(key, "p:"), ":")
		userID, err := strconv.Atoi(userPart)
		if !ok || err != nil || profileID == "" {
			return rec, ErrEmailRecipientNotFound
		}
		rec.Kind, rec.UserID, rec.ProfileID = EmailRecipientProfile, userID, profileID
		if !rec.Enabled {
			rec.Mode = ""
		}
		if err := a.UpdateRecipientChoices(ctx, rec); err != nil {
			return rec, err
		}
		return rec, nil
	}
	return rec, ErrEmailRecipientNotFound
}

// DeleteRecipient removes an admin recipient by key; profile recipients are
// switched off instead, since the address belongs to the profile.
func (a *EmailAlerts) DeleteRecipient(ctx context.Context, key string) error {
	if id, ok := strings.CutPrefix(key, "r:"); ok {
		return a.repo.DeleteAdminRecipient(ctx, id)
	}
	_, err := a.UpdateRecipient(ctx, key, EmailAlertRecipient{Enabled: false})
	return err
}
