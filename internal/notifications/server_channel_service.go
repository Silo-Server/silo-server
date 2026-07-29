package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/oklog/ulid/v2"
)

// Server channel service errors surfaced to the API layer.
var (
	ErrServerChannelInvalid   = errors.New("invalid server channel")
	ErrServerChannelNotFound  = errors.New("server channel not found")
	ErrServerChannelLimit     = errors.New("server channel limit reached")
	ErrServerChannelsDisabled = errors.New("server channels are disabled")
)

// serverChannelMaxChannels caps how many broadcast destinations a server can
// have. Far above any realistic deployment; exists so a scripted runaway
// cannot fill the table.
const serverChannelMaxChannels = 20

// ServerChannelService owns admin CRUD, validation, and signing-secret
// handling for server channels. It mirrors WebhookService: URLs and secrets
// are encrypted at rest, bound to the row identity, and never returned after
// creation.
type ServerChannelService struct {
	repo     *ServerChannelRepository
	cipher   *secret.Cipher
	settings *Settings
	sender   *serverChannelSender
}

func newServerChannelService(repo *ServerChannelRepository, cipher *secret.Cipher, settings *Settings, sender *serverChannelSender) *ServerChannelService {
	return &ServerChannelService{repo: repo, cipher: cipher, settings: settings, sender: sender}
}

// ServerChannelInput is the create/update request shape. Pointer fields are
// optional on update; Create requires Name and URL.
type ServerChannelInput struct {
	Name                   *string
	URL                    *string
	Type                   *string
	Enabled                *bool
	NotifyNewMovies        *bool
	NotifyNewEpisodes      *bool
	NotifyNewAudiobooks    *bool
	NotifyNewEbooks        *bool
	NotifyRequestSubmitted *bool
	NotifyRequestApproved  *bool
	NotifyRequestDeclined  *bool
	NotifyRequestFulfilled *bool
}

// List returns every server channel (ciphertext fields are for internal use;
// the handler view must expose url_host only).
func (s *ServerChannelService) List(ctx context.Context) ([]ServerChannel, error) {
	return s.repo.List(ctx)
}

// Create validates and persists a new server channel. For generic channels
// the returned signingSecret is shown exactly once.
func (s *ServerChannelService) Create(ctx context.Context, createdByUserID int, input ServerChannelInput) (*ServerChannel, string, error) {
	// The kill switch gates delivery in the worker, not configuration. Admins
	// can prepare and test destinations while delivery remains disabled.
	if input.Name == nil || input.URL == nil {
		return nil, "", fmt.Errorf("%w: name and url are required", ErrServerChannelInvalid)
	}
	name, err := validateChannelName(*input.Name, ErrServerChannelInvalid)
	if err != nil {
		return nil, "", err
	}

	rawURL := strings.TrimSpace(*input.URL)
	host, err := ValidateWebhookURL(rawURL, s.settings.WebhooksAllowPrivateDestinations(ctx))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s", ErrServerChannelInvalid, err.Error())
	}

	channelType := ""
	if input.Type != nil {
		channelType = strings.TrimSpace(*input.Type)
	}
	channelType, err = resolveWebhookType(rawURL, channelType, ErrServerChannelInvalid)
	if err != nil {
		return nil, "", err
	}

	ch := ServerChannel{
		ID:                     ulid.Make().String(),
		Name:                   name,
		Type:                   channelType,
		URLHost:                host,
		Enabled:                boolOrDefault(input.Enabled, true),
		NotifyNewMovies:        boolOrDefault(input.NotifyNewMovies, true),
		NotifyNewEpisodes:      boolOrDefault(input.NotifyNewEpisodes, true),
		NotifyNewAudiobooks:    boolOrDefault(input.NotifyNewAudiobooks, true),
		NotifyNewEbooks:        boolOrDefault(input.NotifyNewEbooks, true),
		NotifyRequestSubmitted: boolOrDefault(input.NotifyRequestSubmitted, false),
		NotifyRequestApproved:  boolOrDefault(input.NotifyRequestApproved, false),
		NotifyRequestDeclined:  boolOrDefault(input.NotifyRequestDeclined, false),
		NotifyRequestFulfilled: boolOrDefault(input.NotifyRequestFulfilled, false),
		CreatedByUserID:        createdByUserID,
	}
	ch.URLCiphertext, err = s.cipher.Encrypt(rawURL, serverChannelURLAAD(ch.ID))
	if err != nil {
		return nil, "", fmt.Errorf("encrypt server channel url: %w", err)
	}

	signingSecret := ""
	if channelType == WebhookTypeGeneric {
		signingSecret, err = newSigningSecret()
		if err != nil {
			return nil, "", err
		}
		ciphertext, err := s.cipher.Encrypt(signingSecret, serverChannelSecretAAD(ch.ID))
		if err != nil {
			return nil, "", fmt.Errorf("encrypt signing secret: %w", err)
		}
		ch.SigningSecretCiphertext = &ciphertext
	}

	if err := s.repo.InsertWithLimit(ctx, ch, serverChannelMaxChannels); err != nil {
		if errors.Is(err, ErrServerChannelNameTaken) {
			return nil, "", fmt.Errorf("%w: %s", ErrServerChannelInvalid, err.Error())
		}
		return nil, "", err
	}
	return &ch, signingSecret, nil
}

// Update applies the provided fields. A URL change re-validates the
// destination; URL changes and enable transitions reset the failure streak
// and fast-forward the content watermark to now (a long-dead channel must
// resume from the present, not replay the gap).
func (s *ServerChannelService) Update(ctx context.Context, id string, input ServerChannelInput) (*ServerChannel, error) {
	ch, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if ch == nil {
		return nil, ErrServerChannelNotFound
	}
	wasDelivering := ch.Enabled && ch.DisabledReason == nil

	if input.Name != nil {
		name, err := validateChannelName(*input.Name, ErrServerChannelInvalid)
		if err != nil {
			return nil, err
		}
		ch.Name = name
	}
	resetDispatch := false
	if input.URL != nil {
		rawURL := strings.TrimSpace(*input.URL)
		host, err := ValidateWebhookURL(rawURL, s.settings.WebhooksAllowPrivateDestinations(ctx))
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrServerChannelInvalid, err.Error())
		}
		if err := validateReplacementURL(ch.Type, rawURL, ErrServerChannelInvalid); err != nil {
			return nil, err
		}
		ch.URLCiphertext, err = s.cipher.Encrypt(rawURL, serverChannelURLAAD(ch.ID))
		if err != nil {
			return nil, fmt.Errorf("encrypt server channel url: %w", err)
		}
		ch.URLHost = host
		resetDispatch = true
	}
	if input.Enabled != nil {
		ch.Enabled = *input.Enabled
	}
	if input.NotifyNewMovies != nil {
		ch.NotifyNewMovies = *input.NotifyNewMovies
	}
	if input.NotifyNewEpisodes != nil {
		ch.NotifyNewEpisodes = *input.NotifyNewEpisodes
	}
	if input.NotifyNewAudiobooks != nil {
		ch.NotifyNewAudiobooks = *input.NotifyNewAudiobooks
	}
	if input.NotifyNewEbooks != nil {
		ch.NotifyNewEbooks = *input.NotifyNewEbooks
	}
	if input.NotifyRequestSubmitted != nil {
		ch.NotifyRequestSubmitted = *input.NotifyRequestSubmitted
	}
	if input.NotifyRequestApproved != nil {
		ch.NotifyRequestApproved = *input.NotifyRequestApproved
	}
	if input.NotifyRequestDeclined != nil {
		ch.NotifyRequestDeclined = *input.NotifyRequestDeclined
	}
	if input.NotifyRequestFulfilled != nil {
		ch.NotifyRequestFulfilled = *input.NotifyRequestFulfilled
	}

	if err := s.repo.Update(ctx, *ch); err != nil {
		if errors.Is(err, ErrServerChannelNameTaken) {
			return nil, fmt.Errorf("%w: %s", ErrServerChannelInvalid, err.Error())
		}
		return nil, err
	}

	// Reset after the update commits: an enable transition (off→on or
	// auto-disabled→re-enabled) or a replacement URL clears the streak and
	// moves the watermark to now.
	nowDelivering := ch.Enabled
	if resetDispatch || (nowDelivering && !wasDelivering) {
		if err := s.repo.ResetDispatchState(ctx, ch.ID); err != nil {
			return nil, err
		}
		ch.ConsecutiveFailures = 0
		ch.DisabledReason = nil
	}
	return ch, nil
}

// Delete removes a server channel. Idempotent.
func (s *ServerChannelService) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// RotateSecret generates and stores a new signing secret for a generic
// channel, returning it exactly once.
func (s *ServerChannelService) RotateSecret(ctx context.Context, id string) (string, error) {
	ch, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}
	if ch == nil {
		return "", ErrServerChannelNotFound
	}
	if ch.Type != WebhookTypeGeneric {
		return "", fmt.Errorf("%w: only generic channels have signing secrets", ErrServerChannelInvalid)
	}
	signingSecret, err := newSigningSecret()
	if err != nil {
		return "", err
	}
	ciphertext, err := s.cipher.Encrypt(signingSecret, serverChannelSecretAAD(ch.ID))
	if err != nil {
		return "", fmt.Errorf("encrypt signing secret: %w", err)
	}
	ch.SigningSecretCiphertext = &ciphertext
	if err := s.repo.Update(ctx, *ch); err != nil {
		return "", err
	}
	return signingSecret, nil
}

// Test synchronously POSTs a clearly marked sample content digest. Test sends
// never touch the watermark or failure counters.
func (s *ServerChannelService) Test(ctx context.Context, id string) (*WebhookTestResult, error) {
	ch, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if ch == nil {
		return nil, ErrServerChannelNotFound
	}
	return s.sender.sendContent(ctx, ch, sampleContentGroups(), true).testResult(), nil
}

// sampleContentGroups is the fixture used for test sends: one group per flat
// item kind plus an episode group, so a test post exercises every render
// path the channel can receive.
func sampleContentGroups() []ContentGroup {
	groups := make([]ContentGroup, 0, len(flatItemKinds)+1)
	for _, k := range flatItemKinds {
		meta := ContentMeta{Title: "Silo Test " + capitalize(k.ItemType), Year: 2026}
		if k.Kind != EventKindMovie {
			meta.Author = "Test Author"
		}
		groups = append(groups, ContentGroup{
			Kind:      k.Kind,
			LibraryID: 1,
			ItemID:    "test-" + k.Kind,
			Meta:      meta,
		})
	}
	return append(groups, ContentGroup{
		Kind:      EventKindEpisode,
		LibraryID: 1,
		SeriesID:  "test-series",
		Meta:      ContentMeta{Title: "Silo Test Series"},
		Episodes: []ReleaseEvent{
			{Kind: EventKindEpisode, LibraryID: 1, SeriesID: "test-series",
				SeasonNumber: 1, EpisodeNumber: 1, EpisodeKey: EpisodeKey(1, 1)},
			{Kind: EventKindEpisode, LibraryID: 1, SeriesID: "test-series",
				SeasonNumber: 1, EpisodeNumber: 2, EpisodeKey: EpisodeKey(1, 2)},
		},
	})
}

// capitalize upper-cases the first ASCII letter of a display noun.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
