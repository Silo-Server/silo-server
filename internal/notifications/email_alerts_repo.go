package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/emailalerts"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// Email alert recipient kinds: an address an administrator added, or a
// profile's own verified address (notification_email_prefs).
const (
	EmailRecipientAdmin   = "admin"
	EmailRecipientProfile = "profile"
)

// What a recipient receives: everything new it can open, or only what the
// profile's notifications would announce (series it follows, and anything
// its "everything new" switches ask for).
const (
	EmailContentAll      = "all"
	EmailContentFollowed = "followed"
)

// ErrEmailRecipientNotFound means no recipient matches the id or token.
var ErrEmailRecipientNotFound = errors.New("email recipient not found")

// ErrEmailRecipientExists means another recipient already uses the address.
var ErrEmailRecipientExists = errors.New("another recipient already uses that address")

// EmailAlertRecipient is one address the email alerts go to.
type EmailAlertRecipient struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"` // admin recipient id; empty for a profile
	UserID    int    `json:"user_id,omitempty"`
	ProfileID string `json:"profile_id,omitempty"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Enabled   bool   `json:"enabled"`
	Mode      string `json:"mode"`
	// Legs are a profile's sends (instant, daily, weekly) in any
	// combination; an added address's come from its mode.
	Legs        []string  `json:"legs"`
	Content     string    `json:"content"`
	Types       []string  `json:"types"`
	LibraryIDs  []int     `json:"library_ids"`
	MaxAgeHours int       `json:"max_age_hours"`
	ManageToken string    `json:"-"`
	CreatedAt   time.Time `json:"created_at"`
}

// legs returns the recipient's sends.
func (r EmailAlertRecipient) legs() []string {
	if len(r.Legs) > 0 {
		return r.Legs
	}
	return emailalerts.Legs(r.Mode)
}

// profileLegs lists a profile's sends.
func profileLegs(instant, daily, weekly bool) []string {
	var legs []string
	if instant {
		legs = append(legs, emailalerts.LegInstant)
	}
	if daily {
		legs = append(legs, emailalerts.LegDaily)
	}
	if weekly {
		legs = append(legs, emailalerts.LegWeekly)
	}
	return legs
}

// Key identifies the recipient in the send state and log.
func (r EmailAlertRecipient) Key() string {
	if r.Kind == EmailRecipientProfile {
		return "p:" + strconv.Itoa(r.UserID) + ":" + r.ProfileID
	}
	return "r:" + r.ID
}

// profileAlertMode maps a profile's stored email mode to a recipient mode.
// The stored values predate email alerts and stay as they are for the API.
func profileAlertMode(mode string) string {
	switch mode {
	case ChannelModePerEpisode:
		return emailalerts.ModeInstant
	case ChannelModePerEpisodeAndDigest:
		return emailalerts.ModeInstantDaily
	case ChannelModeDailyDigest:
		return emailalerts.ModeDaily
	case ChannelModeWeeklyDigest:
		return emailalerts.ModeWeekly
	case EmailModePerEpisodeAndWeeklyDigest:
		return emailalerts.ModeInstantWeekly
	}
	return ""
}

// ProfileEmailMode maps a recipient mode back to a profile's stored mode.
func ProfileEmailMode(mode string) string {
	switch mode {
	case emailalerts.ModeInstant:
		return ChannelModePerEpisode
	case emailalerts.ModeInstantDaily:
		return ChannelModePerEpisodeAndDigest
	case emailalerts.ModeDaily:
		return ChannelModeDailyDigest
	case emailalerts.ModeWeekly:
		return ChannelModeWeeklyDigest
	case emailalerts.ModeInstantWeekly:
		return EmailModePerEpisodeAndWeeklyDigest
	}
	return ChannelModeOff
}

// EmailAlertState is one recipient leg's send state.
type EmailAlertState struct {
	Watermark     Cursor
	LastRunAt     *time.Time
	LastSentAt    *time.Time
	LastInstantAt *time.Time
	Day           string
	DayCount      int
	Failures      int
	NextAttemptAt *time.Time
}

// EmailAlertLogEntry is one send, successful or not.
type EmailAlertLogEntry struct {
	SentAt       time.Time `json:"sent_at"`
	RecipientKey string    `json:"recipient_key"`
	Email        string    `json:"email"`
	Leg          string    `json:"leg"`
	Items        int       `json:"items"`
	Subject      string    `json:"subject"`
	Error        string    `json:"error,omitempty"`
}

// EmailAlertsRepository owns the email alert settings, admin recipients,
// send state and log, and reads profile recipients from
// notification_email_prefs.
type EmailAlertsRepository struct {
	pool *pgxpool.Pool
}

// NewEmailAlertsRepository creates an EmailAlertsRepository.
func NewEmailAlertsRepository(pool *pgxpool.Pool) *EmailAlertsRepository {
	return &EmailAlertsRepository{pool: pool}
}

// Settings returns the saved settings, or the defaults.
func (r *EmailAlertsRepository) Settings(ctx context.Context) (emailalerts.Settings, error) {
	var raw []byte
	err := r.pool.QueryRow(ctx, `SELECT settings FROM notification_email_alert_settings WHERE id = 1`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return emailalerts.DefaultSettings(), nil
	}
	if err != nil {
		return emailalerts.Settings{}, fmt.Errorf("load email alert settings: %w", err)
	}
	s := emailalerts.DefaultSettings()
	if err := json.Unmarshal(raw, &s); err != nil {
		return emailalerts.Settings{}, fmt.Errorf("decode email alert settings: %w", err)
	}
	s.Normalize()
	return s, nil
}

// SaveSettings stores the settings after normalizing them.
func (r *EmailAlertsRepository) SaveSettings(ctx context.Context, s emailalerts.Settings) (emailalerts.Settings, error) {
	s.Normalize()
	raw, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO notification_email_alert_settings (id, settings, updated_at) VALUES (1, $1, now())
		ON CONFLICT (id) DO UPDATE SET settings = EXCLUDED.settings, updated_at = now()`, raw)
	if err != nil {
		return s, fmt.Errorf("save email alert settings: %w", err)
	}
	return s, nil
}

const adminRecipientColumns = `id, name, email, enabled, mode, types, library_ids, max_age_hours, manage_token, created_at`

func scanAdminRecipient(row pgx.Row) (EmailAlertRecipient, error) {
	rec := EmailAlertRecipient{Kind: EmailRecipientAdmin, Content: EmailContentAll}
	err := row.Scan(&rec.ID, &rec.Name, &rec.Email, &rec.Enabled, &rec.Mode, &rec.Types,
		&rec.LibraryIDs, &rec.MaxAgeHours, &rec.ManageToken, &rec.CreatedAt)
	return rec, err
}

// Recipients lists every admin recipient and every profile with email on
// and a verified address, admin recipients first. Profiles of disabled
// accounts are left out.
func (r *EmailAlertsRepository) Recipients(ctx context.Context) ([]EmailAlertRecipient, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+adminRecipientColumns+`
		FROM notification_email_recipients ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list email recipients: %w", err)
	}
	var out []EmailAlertRecipient
	for rows.Next() {
		rec, err := scanAdminRecipient(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan email recipient: %w", err)
		}
		out = append(out, rec)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = r.pool.Query(ctx, `
		SELECT p.user_id, p.profile_id, p.custom_email, p.mode, p.content, p.types,
		       p.library_ids, p.max_age_hours, p.unsubscribe_token, p.updated_at,
		       p.send_instant, p.send_daily, p.send_weekly
		FROM notification_email_prefs p
		JOIN users u ON u.id = p.user_id AND u.enabled
		WHERE (p.send_instant OR p.send_daily OR p.send_weekly) AND p.custom_email <> ''
		ORDER BY p.user_id, p.profile_id`)
	if err != nil {
		return nil, fmt.Errorf("list profile email recipients: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		rec := EmailAlertRecipient{Kind: EmailRecipientProfile, Enabled: true}
		var mode string
		var instant, daily, weekly bool
		if err := rows.Scan(&rec.UserID, &rec.ProfileID, &rec.Email, &mode, &rec.Content, &rec.Types,
			&rec.LibraryIDs, &rec.MaxAgeHours, &rec.ManageToken, &rec.CreatedAt,
			&instant, &daily, &weekly); err != nil {
			return nil, fmt.Errorf("scan profile email recipient: %w", err)
		}
		rec.Mode = profileAlertMode(mode)
		rec.Legs = profileLegs(instant, daily, weekly)
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// Every email carries a manage link, so a profile without a token gets
	// one now.
	for i, rec := range out {
		if rec.Kind != EmailRecipientProfile || rec.ManageToken != "" {
			continue
		}
		token, _, err := newEmailToken()
		if err != nil {
			return nil, err
		}
		if err := r.pool.QueryRow(ctx, `
			UPDATE notification_email_prefs
			SET unsubscribe_token = CASE WHEN unsubscribe_token = '' THEN $3 ELSE unsubscribe_token END
			WHERE user_id = $1 AND profile_id = $2
			RETURNING unsubscribe_token`, rec.UserID, rec.ProfileID, token).Scan(&out[i].ManageToken); err != nil {
			return nil, fmt.Errorf("mint email manage token: %w", err)
		}
	}
	return out, nil
}

// AdminRecipient returns one admin recipient.
func (r *EmailAlertsRepository) AdminRecipient(ctx context.Context, id string) (EmailAlertRecipient, error) {
	rec, err := scanAdminRecipient(r.pool.QueryRow(ctx, `SELECT `+adminRecipientColumns+`
		FROM notification_email_recipients WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return rec, ErrEmailRecipientNotFound
	}
	return rec, err
}

// RecipientByToken finds the recipient a manage link belongs to: an admin
// recipient's manage token, or a profile's unsubscribe token.
func (r *EmailAlertsRepository) RecipientByToken(ctx context.Context, token string) (EmailAlertRecipient, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return EmailAlertRecipient{}, ErrEmailRecipientNotFound
	}
	rec, err := scanAdminRecipient(r.pool.QueryRow(ctx, `SELECT `+adminRecipientColumns+`
		FROM notification_email_recipients WHERE manage_token = $1`, token))
	if err == nil {
		return rec, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return rec, err
	}
	rec = EmailAlertRecipient{Kind: EmailRecipientProfile, Enabled: true}
	var mode string
	var instant, daily, weekly bool
	err = r.pool.QueryRow(ctx, `
		SELECT user_id, profile_id, custom_email, mode, content, types, library_ids,
		       max_age_hours, unsubscribe_token, updated_at, send_instant, send_daily, send_weekly
		FROM notification_email_prefs WHERE unsubscribe_token = $1`, token,
	).Scan(&rec.UserID, &rec.ProfileID, &rec.Email, &mode, &rec.Content, &rec.Types,
		&rec.LibraryIDs, &rec.MaxAgeHours, &rec.ManageToken, &rec.CreatedAt, &instant, &daily, &weekly)
	if errors.Is(err, pgx.ErrNoRows) {
		return rec, ErrEmailRecipientNotFound
	}
	if err != nil {
		return rec, err
	}
	rec.Mode = profileAlertMode(mode)
	rec.Legs = profileLegs(instant, daily, weekly)
	rec.Enabled = len(rec.Legs) > 0
	return rec, nil
}

// SaveAdminRecipient creates (empty ID) or updates an admin recipient.
func (r *EmailAlertsRepository) SaveAdminRecipient(ctx context.Context, rec EmailAlertRecipient) (EmailAlertRecipient, error) {
	if rec.Types == nil {
		rec.Types = []string{}
	}
	if rec.LibraryIDs == nil {
		rec.LibraryIDs = []int{}
	}
	var row pgx.Row
	if rec.ID == "" {
		token, _, err := newEmailToken()
		if err != nil {
			return rec, err
		}
		row = r.pool.QueryRow(ctx, `
			INSERT INTO notification_email_recipients
				(id, name, email, enabled, mode, types, library_ids, max_age_hours, manage_token)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING `+adminRecipientColumns,
			ulid.Make().String(), rec.Name, rec.Email, rec.Enabled, rec.Mode, rec.Types, rec.LibraryIDs, rec.MaxAgeHours, token)
	} else {
		row = r.pool.QueryRow(ctx, `
			UPDATE notification_email_recipients SET
				name = $2, email = $3, enabled = $4, mode = $5, types = $6, library_ids = $7,
				max_age_hours = $8, updated_at = now()
			WHERE id = $1
			RETURNING `+adminRecipientColumns,
			rec.ID, rec.Name, rec.Email, rec.Enabled, rec.Mode, rec.Types, rec.LibraryIDs, rec.MaxAgeHours)
	}
	saved, err := scanAdminRecipient(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return saved, ErrEmailRecipientNotFound
	}
	if isUniqueViolation(err) {
		return saved, ErrEmailRecipientExists
	}
	if err != nil {
		return saved, fmt.Errorf("save email recipient: %w", err)
	}
	return saved, nil
}

// DeleteAdminRecipient removes an admin recipient and its send state.
func (r *EmailAlertsRepository) DeleteAdminRecipient(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM notification_email_recipients WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete email recipient: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrEmailRecipientNotFound
	}
	return r.DeleteState(ctx, EmailAlertRecipient{Kind: EmailRecipientAdmin, ID: id}.Key())
}

// UpdateProfileRecipient changes a profile recipient's choices. mode is a
// recipient mode, or "" to switch the profile's email off.
func (r *EmailAlertsRepository) UpdateProfileRecipient(ctx context.Context, userID int, profileID, mode, content string, types []string, libraryIDs []int, maxAgeHours int) error {
	var legs []string
	if mode != "" {
		legs = emailalerts.Legs(mode)
	}
	if types == nil {
		types = []string{}
	}
	if libraryIDs == nil {
		libraryIDs = []int{}
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE notification_email_prefs SET
			mode = $3, content = $4, types = $5, library_ids = $6, max_age_hours = $7,
			send_instant = $8, send_daily = $9, send_weekly = $10, updated_at = now()
		WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID, ProfileEmailMode(mode), content, types, libraryIDs, maxAgeHours,
		slices.Contains(legs, emailalerts.LegInstant), slices.Contains(legs, emailalerts.LegDaily),
		slices.Contains(legs, emailalerts.LegWeekly))
	if err != nil {
		return fmt.Errorf("update profile email recipient: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrEmailRecipientNotFound
	}
	return nil
}

// State returns one leg's state; found is false for a leg never run.
func (r *EmailAlertsRepository) State(ctx context.Context, key, leg string) (EmailAlertState, bool, error) {
	var st EmailAlertState
	err := r.pool.QueryRow(ctx, `
		SELECT watermark_created_at, watermark_id, last_run_at, last_sent_at, last_instant_at,
		       day, day_count, failures, next_attempt_at
		FROM notification_email_alert_state WHERE recipient_key = $1 AND leg = $2`, key, leg,
	).Scan(&st.Watermark.CreatedAt, &st.Watermark.ID, &st.LastRunAt, &st.LastSentAt, &st.LastInstantAt,
		&st.Day, &st.DayCount, &st.Failures, &st.NextAttemptAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, false, nil
	}
	if err != nil {
		return st, false, fmt.Errorf("load email alert state: %w", err)
	}
	return st, true, nil
}

// SaveState writes one leg's state.
func (r *EmailAlertsRepository) SaveState(ctx context.Context, key, leg string, st EmailAlertState) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_email_alert_state
			(recipient_key, leg, watermark_created_at, watermark_id, last_run_at, last_sent_at,
			 last_instant_at, day, day_count, failures, next_attempt_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (recipient_key, leg) DO UPDATE SET
			watermark_created_at = EXCLUDED.watermark_created_at,
			watermark_id = EXCLUDED.watermark_id,
			last_run_at = EXCLUDED.last_run_at,
			last_sent_at = EXCLUDED.last_sent_at,
			last_instant_at = EXCLUDED.last_instant_at,
			day = EXCLUDED.day,
			day_count = EXCLUDED.day_count,
			failures = EXCLUDED.failures,
			next_attempt_at = EXCLUDED.next_attempt_at`,
		key, leg, st.Watermark.CreatedAt, st.Watermark.ID, st.LastRunAt, st.LastSentAt,
		st.LastInstantAt, st.Day, st.DayCount, st.Failures, st.NextAttemptAt)
	if err != nil {
		return fmt.Errorf("save email alert state: %w", err)
	}
	return nil
}

// DeleteState forgets every leg of a recipient.
func (r *EmailAlertsRepository) DeleteState(ctx context.Context, key string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM notification_email_alert_state WHERE recipient_key = $1`, key)
	return err
}

// Rewind moves every leg of a recipient back to since, so the next run
// sends what arrived after it again.
func (r *EmailAlertsRepository) Rewind(ctx context.Context, key string, since time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE notification_email_alert_state SET
			watermark_created_at = $2, watermark_id = '', last_run_at = NULL,
			last_instant_at = NULL, day_count = 0, failures = 0, next_attempt_at = NULL
		WHERE recipient_key = $1`, key, since)
	return err
}

// emailAlertLogCap is how many log entries are kept.
const emailAlertLogCap = 300

// AppendLog records sends and trims the log.
func (r *EmailAlertsRepository) AppendLog(ctx context.Context, entries ...EmailAlertLogEntry) error {
	for _, e := range entries {
		if _, err := r.pool.Exec(ctx, `
			INSERT INTO notification_email_alert_log (sent_at, recipient_key, email, leg, items, subject, error)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			e.SentAt, e.RecipientKey, e.Email, e.Leg, e.Items, e.Subject, e.Error); err != nil {
			return fmt.Errorf("append email alert log: %w", err)
		}
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM notification_email_alert_log
		WHERE id < (SELECT id FROM notification_email_alert_log ORDER BY id DESC OFFSET $1 LIMIT 1)`,
		emailAlertLogCap-1)
	return err
}

// Log returns the newest sends first.
func (r *EmailAlertsRepository) Log(ctx context.Context, limit int) ([]EmailAlertLogEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT sent_at, recipient_key, email, leg, items, subject, error
		FROM notification_email_alert_log ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list email alert log: %w", err)
	}
	defer rows.Close()
	var out []EmailAlertLogEntry
	for rows.Next() {
		var e EmailAlertLogEntry
		if err := rows.Scan(&e.SentAt, &e.RecipientKey, &e.Email, &e.Leg, &e.Items, &e.Subject, &e.Error); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EmailAlertLibrary is a movie or TV library recipients can choose.
type EmailAlertLibrary struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
}

// Libraries lists the enabled movie and TV libraries.
func (r *EmailAlertsRepository) Libraries(ctx context.Context) ([]EmailAlertLibrary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, type FROM media_folders
		WHERE COALESCE(enabled, true) AND type IN ('movies', 'series')
		ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list email libraries: %w", err)
	}
	defer rows.Close()
	var out []EmailAlertLibrary
	for rows.Next() {
		var l EmailAlertLibrary
		if err := rows.Scan(&l.ID, &l.Name, &l.MediaType); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
