package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/discord"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// discordDMBlockedMessage is the link_failure text surfaced in the settings
// UI when Discord refuses the DM (error 50007).
const discordDMBlockedMessage = "Discord rejected the direct message. " +
	"Make sure you share a server with the bot and allow direct messages from server members."

// discordChannel implements accountChannel over the Discord bot REST API,
// keyed by login account: the linked identity is account-level, so one DM
// collapses cross-profile duplicates. The bot token is read live from
// settings on every pass, so admin changes apply without a restart (same
// pattern as the SMTP sender).
type discordChannel struct {
	prefs      *DiscordPrefsRepository
	deliveries *DeliveryRepository
	settings   *Settings
	client     *discord.Client
	// posterURL picks the artwork URL DM embeds may carry. Wired by
	// NewSystem after construction; nil renders embeds without images.
	posterURL func(ctx context.Context, posterPath, posterSourcePath string) string
}

// The assertion also keeps staticcheck's unused-analysis aware that the
// adapter methods are consumed through the generic engine interface.
var _ accountChannel[int] = (*discordChannel)(nil)

// newDiscordWorker assembles the Discord DM channel on the shared
// account-channel engine, returning the channel too so NewSystem can wire
// post-construction hooks (posterURL) on it.
func newDiscordWorker(
	pool *pgxpool.Pool,
	deliveries *DeliveryRepository,
	prefs *DiscordPrefsRepository,
	settings *Settings,
	client *discord.Client,
) (*accountChannelWorker[int], *discordChannel) {
	channel := &discordChannel{
		prefs:      prefs,
		deliveries: deliveries,
		settings:   settings,
		client:     client,
	}
	return newAccountChannelWorker(pool, channel), channel
}

func (c *discordChannel) name() string { return "discord" }

func (c *discordChannel) enabled(ctx context.Context) bool {
	return c.settings.DiscordEnabled(ctx) && c.settings.DiscordBotToken(ctx) != ""
}

func (c *discordChannel) allowPerEpisode(ctx context.Context) bool {
	return c.settings.DiscordAllowPerEpisode(ctx)
}

func (c *discordChannel) digestHour(ctx context.Context) int {
	return c.settings.DiscordDigestHour(ctx)
}

func (c *discordChannel) listRecipients(ctx context.Context) ([]accountRecipient[int], error) {
	return c.prefs.ListActiveRecipients(ctx)
}

func (c *discordChannel) hasPendingSince(ctx context.Context, userID int, since Cursor) (bool, error) {
	return c.deliveries.HasForUserSince(ctx, userID, since)
}

func (c *discordChannel) hasTransactionalPendingSince(ctx context.Context, userID int, since Cursor) (bool, error) {
	return c.deliveries.HasTransactionalForUserSince(ctx, userID, since)
}

func (c *discordChannel) listSince(ctx context.Context, tx pgx.Tx, userID int, since Cursor, until time.Time, limit int) ([]DeliveryRow, error) {
	return c.deliveries.ListForUserSince(ctx, tx, userID, since, until, limit)
}

func (c *discordChannel) claim(ctx context.Context, tx pgx.Tx, userID int) (*accountRecipient[int], error) {
	return c.prefs.claimForUpdate(ctx, tx, userID)
}

func (c *discordChannel) markSent(ctx context.Context, tx pgx.Tx, userID int, watermark Cursor, digestAt *time.Time) error {
	return c.prefs.markSent(ctx, tx, userID, watermark, digestAt)
}

func (c *discordChannel) markFailure(ctx context.Context, tx pgx.Tx, userID int, sendErr error) error {
	message := "Discord delivery failed"
	switch {
	case errors.Is(sendErr, discord.ErrDMBlocked):
		message = discordDMBlockedMessage
	case sendErr != nil:
		message = truncateWithEllipsis("Discord delivery failed: "+sendErr.Error(), 300)
	}
	return c.prefs.markFailure(ctx, tx, userID, message)
}

// send delivers one account's pending rows as a single bot DM. Failures that
// indicate a global problem (missing/rejected bot token, rate limiting) wrap
// errChannelUnavailable so the pass aborts without penalizing the account;
// everything else (notably 50007 DM-blocked) backs off per account and is
// surfaced as link health.
func (c *discordChannel) send(ctx context.Context, tx pgx.Tx, userID int, _ string, rows []DeliveryRow) error {
	botToken := c.settings.DiscordBotToken(ctx)
	if botToken == "" {
		return fmt.Errorf("bot token not configured: %w", errChannelUnavailable)
	}

	discordUserID, dmChannelID, err := c.prefs.identityForSend(ctx, tx, userID)
	if err != nil {
		return err
	}
	if discordUserID == "" {
		return fmt.Errorf("account %d has no linked discord identity", userID)
	}
	if dmChannelID == "" {
		dmChannelID, err = c.client.OpenDMChannel(ctx, botToken, discordUserID)
		if err != nil {
			return classifyDiscordSendError(err)
		}
		if err := c.prefs.cacheDMChannel(ctx, tx, userID, dmChannelID); err != nil {
			return err
		}
	}

	if c.posterURL != nil {
		for i := range rows {
			rows[i].PosterURL = c.posterURL(ctx, rows[i].PosterPath, rows[i].PosterSourcePath)
		}
	}
	payload, err := BuildDiscordDMPayload(rows)
	if err != nil {
		return fmt.Errorf("build discord dm payload: %w", err)
	}
	if err := c.client.SendDM(ctx, botToken, dmChannelID, payload); err != nil {
		return classifyDiscordSendError(err)
	}
	return nil
}

// classifyDiscordSendError separates global transport problems (which abort
// the pass) from per-account failures (which back off and surface as link
// health).
func classifyDiscordSendError(err error) error {
	if errors.Is(err, discord.ErrUnauthorized) || errors.Is(err, discord.ErrRateLimited) {
		return fmt.Errorf("%w: %w", err, errChannelUnavailable)
	}
	return err
}

// Errors surfaced by the Discord System methods for the API layer to map to
// 4xx responses.
var (
	ErrDiscordModeInvalid    = errors.New("invalid discord notification mode")
	ErrDiscordModeNotAllowed = errors.New("per-episode discord DMs are disabled by the administrator")
	ErrDiscordNotLinked      = errors.New("no linked discord account")
	ErrDiscordNotConfigured  = errors.New("discord integration is not configured")
)

// discordLinkStateTTL bounds how long a started link flow stays redeemable.
const discordLinkStateTTL = 10 * time.Minute

// DiscordConfigured reports whether the admin has supplied the full Discord
// application credential set (linking needs client ID + secret; DM delivery
// needs the bot token).
func (s *System) DiscordConfigured(ctx context.Context) bool {
	return s != nil && s.DiscordPrefs != nil &&
		s.Settings.DiscordClientID(ctx) != "" &&
		s.Settings.DiscordClientSecret(ctx) != "" &&
		s.Settings.DiscordBotToken(ctx) != ""
}

// DiscordAvailable reports whether the Discord DM channel can deliver right
// now: fully configured and the kill switch is on.
func (s *System) DiscordAvailable(ctx context.Context) bool {
	return s.DiscordConfigured(ctx) && s.Settings.DiscordEnabled(ctx)
}

// DiscordPrefsFor returns the account's Discord link + mode state.
func (s *System) DiscordPrefsFor(ctx context.Context, userID int) (DiscordPrefs, error) {
	if s == nil || s.DiscordPrefs == nil {
		return DiscordPrefs{UserID: userID, Mode: ChannelModeOff}, nil
	}
	return s.DiscordPrefs.Get(ctx, userID)
}

// SetDiscordMode validates and stores the account's Discord DM mode.
// Enabling requires a linked Discord account and, for per-episode, the admin
// allowance.
func (s *System) SetDiscordMode(ctx context.Context, userID int, mode string) error {
	if s == nil || s.DiscordPrefs == nil {
		return ErrDiscordModeInvalid
	}
	if !ValidChannelMode(mode) {
		return ErrDiscordModeInvalid
	}
	if ModeIncludesPerEpisode(mode) && !s.Settings.DiscordAllowPerEpisode(ctx) {
		return ErrDiscordModeNotAllowed
	}
	return s.DiscordPrefs.SetMode(ctx, userID, mode)
}

// BeginDiscordLink records a one-time state row for a link flow started by
// userID.
func (s *System) BeginDiscordLink(ctx context.Context, state string, userID int) error {
	if s == nil || s.DiscordPrefs == nil {
		return ErrDiscordNotConfigured
	}
	return s.DiscordPrefs.CreateLinkState(ctx, state, userID, time.Now().UTC().Add(discordLinkStateTTL))
}

// ConsumeDiscordLinkState redeems a one-time link state, returning the user
// who started the flow. ok is false for unknown, used, or expired states.
func (s *System) ConsumeDiscordLinkState(ctx context.Context, state string) (int, bool, error) {
	if s == nil || s.DiscordPrefs == nil {
		return 0, false, ErrDiscordNotConfigured
	}
	return s.DiscordPrefs.ConsumeLinkState(ctx, state)
}

// CompleteDiscordLink exchanges the OAuth authorization code, resolves the
// Discord identity behind it, and links it to the account.
func (s *System) CompleteDiscordLink(ctx context.Context, userID int, code, redirectURI string) (discord.User, error) {
	if s == nil || s.discordClient == nil || s.DiscordPrefs == nil {
		return discord.User{}, ErrDiscordNotConfigured
	}
	clientID := s.Settings.DiscordClientID(ctx)
	clientSecret := s.Settings.DiscordClientSecret(ctx)
	if clientID == "" || clientSecret == "" {
		return discord.User{}, ErrDiscordNotConfigured
	}
	accessToken, err := s.discordClient.ExchangeCode(ctx, clientID, clientSecret, code, redirectURI)
	if err != nil {
		return discord.User{}, err
	}
	user, err := s.discordClient.GetUser(ctx, accessToken)
	if err != nil {
		return discord.User{}, err
	}
	if err := s.DiscordPrefs.SetIdentity(ctx, userID, user.ID, user.Username); err != nil {
		return discord.User{}, err
	}
	s.logger.InfoContext(ctx, "discord account linked", "user_id", userID, "discord_user_id", user.ID)
	return user, nil
}

// UnlinkDiscord removes the account's Discord identity and switches the
// channel off.
func (s *System) UnlinkDiscord(ctx context.Context, userID int) error {
	if s == nil || s.DiscordPrefs == nil {
		return nil
	}
	return s.DiscordPrefs.ClearIdentity(ctx, userID)
}

// TestDiscordBot verifies the configured bot token by fetching the bot's own
// identity. Used by the admin test endpoint.
func (s *System) TestDiscordBot(ctx context.Context) (discord.User, error) {
	if s == nil || s.discordClient == nil {
		return discord.User{}, ErrDiscordNotConfigured
	}
	// The admin typically tests seconds after saving; don't let the read
	// cache report a stale "not configured".
	s.Settings.Invalidate(SettingDiscordBotToken)
	botToken := s.Settings.DiscordBotToken(ctx)
	if botToken == "" {
		return discord.User{}, ErrDiscordNotConfigured
	}
	return s.discordClient.GetBotUser(ctx, botToken)
}
