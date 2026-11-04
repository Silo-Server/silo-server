package notifications

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// pluginValidateTimeout bounds a plugin's ValidateDestination call.
const pluginValidateTimeout = 10 * time.Second

// PluginDestinationService manages profiles' plugin destinations. Settings
// are checked by the plugin that provides the kind before they are saved,
// stored encrypted, and returned without their secret fields.
type PluginDestinationService struct {
	repo   *PluginDestinationRepository
	sender *pluginDestinationSender
}

func newPluginDestinationService(repo *PluginDestinationRepository, sender *pluginDestinationSender) *PluginDestinationService {
	return &PluginDestinationService{repo: repo, sender: sender}
}

// PluginDestinationInput is the create and update request. On create the
// channel and name are required. On update a nil field keeps its value, and
// Settings merges over the stored settings: a secret field sent blank or
// left out keeps its stored value, and ClearSecrets removes one.
type PluginDestinationInput struct {
	Channel                PluginChannelRef
	Name                   *string
	Settings               map[string]any
	ClearSecrets           []string
	Enabled                *bool
	NotifyFavorites        *bool
	NotifyWatchlist        *bool
	NotifyContinueWatching *bool
	NotifyNextUp           *bool
	NotifyRequests         *bool
}

// PluginDestinationView is a destination as its owner sees it.
type PluginDestinationView struct {
	PluginDestination
	// Channel is the kind's current description, or nil when no enabled
	// plugin provides it now.
	Channel *PluginChannel
	// Settings holds the stored settings without secret fields; nil when
	// the kind is unavailable, since its secret fields are unknown then.
	Settings map[string]any
	// SecretsSet lists the secret fields that have a stored value.
	SecretsSet []string
}

// PluginDestinationTestResult is the outcome of a test message.
type PluginDestinationTestResult struct {
	OK         bool
	Message    string
	DurationMS int64
}

// Available reports whether plugin destinations can be used on this server:
// the plugin runtime is wired. Whether any kind exists is Channels' answer.
func (s *PluginDestinationService) Available() bool {
	return s != nil && s.sender.host() != nil
}

// Channels lists the destination kinds the enabled plugins provide.
func (s *PluginDestinationService) Channels(ctx context.Context) ([]PluginChannel, error) {
	host := s.sender.host()
	if host == nil {
		return nil, nil
	}
	return host.Channels(ctx)
}

func (s *PluginDestinationService) channel(ctx context.Context, ref PluginChannelRef) (*PluginChannel, error) {
	channels, err := s.Channels(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPluginChannelUnavailable, err)
	}
	for i := range channels {
		if channels[i].PluginChannelRef == ref {
			return &channels[i], nil
		}
	}
	return nil, ErrPluginChannelUnavailable
}

// Create checks a new destination with its plugin and stores it.
func (s *PluginDestinationService) Create(ctx context.Context, userID int, profileID string, in PluginDestinationInput) (*PluginDestination, error) {
	if in.Name == nil {
		return nil, fmt.Errorf("%w: name is required", ErrPluginDestinationInvalid)
	}
	name, err := validateChannelName(*in.Name, ErrPluginDestinationInvalid)
	if err != nil {
		return nil, err
	}
	channel, err := s.channel(ctx, in.Channel)
	if err != nil {
		return nil, err
	}
	settings, err := mergePluginSettings(*channel, nil, in.Settings, nil)
	if err != nil {
		return nil, err
	}
	if err := s.validate(ctx, *channel, settings); err != nil {
		return nil, err
	}
	d := PluginDestination{
		ID:                     ulid.Make().String(),
		UserID:                 userID,
		ProfileID:              profileID,
		PluginID:               channel.PluginID,
		CapabilityID:           channel.CapabilityID,
		Name:                   name,
		Enabled:                true,
		NotifyFavorites:        boolOrDefault(in.NotifyFavorites, true),
		NotifyWatchlist:        boolOrDefault(in.NotifyWatchlist, true),
		NotifyContinueWatching: boolOrDefault(in.NotifyContinueWatching, true),
		NotifyNextUp:           boolOrDefault(in.NotifyNextUp, true),
		NotifyRequests:         boolOrDefault(in.NotifyRequests, true),
		CreatedAt:              time.Now(),
	}
	if d.ConfigCiphertext, err = s.sender.encryptConfig(d.ID, settings); err != nil {
		return nil, err
	}
	if err := s.repo.InsertWithLimit(ctx, d, PluginDestinationMaxPerProfile); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, userID, profileID, d.ID)
}

// UpdateGuarded applies in after check accepts the stored revision, under
// the row lock. Changed settings are checked by the plugin again, and like a
// changed webhook URL they clear the failure streak and the disable reason.
func (s *PluginDestinationService) UpdateGuarded(ctx context.Context, userID int, profileID, id string, in PluginDestinationInput, check func(int64) error) (*PluginDestination, error) {
	return s.repo.UpdateGuarded(ctx, userID, profileID, id, func(d *PluginDestination) error {
		if err := check(d.Revision); err != nil {
			return err
		}
		if in.Name != nil {
			name, err := validateChannelName(*in.Name, ErrPluginDestinationInvalid)
			if err != nil {
				return err
			}
			d.Name = name
		}
		if in.Settings != nil || len(in.ClearSecrets) > 0 {
			if err := s.replaceSettings(ctx, d, in); err != nil {
				return err
			}
		}
		if in.Enabled != nil {
			d.Enabled = *in.Enabled
			if d.Enabled {
				d.ConsecutiveFailures = 0
				d.DisabledReason = nil
			}
		}
		setIfPresent(&d.NotifyFavorites, in.NotifyFavorites)
		setIfPresent(&d.NotifyWatchlist, in.NotifyWatchlist)
		setIfPresent(&d.NotifyContinueWatching, in.NotifyContinueWatching)
		setIfPresent(&d.NotifyNextUp, in.NotifyNextUp)
		setIfPresent(&d.NotifyRequests, in.NotifyRequests)
		return nil
	})
}

// replaceSettings merges a settings write into d, has the plugin check the
// result, and stores it encrypted.
func (s *PluginDestinationService) replaceSettings(ctx context.Context, d *PluginDestination, in PluginDestinationInput) error {
	channel, err := s.channel(ctx, d.Ref())
	if err != nil {
		return err
	}
	stored, err := s.sender.decryptConfig(d)
	if err != nil {
		// Unreadable settings are replaced wholesale.
		stored = map[string]any{}
	}
	settings, err := mergePluginSettings(*channel, stored, in.Settings, in.ClearSecrets)
	if err != nil {
		return err
	}
	if err := s.validate(ctx, *channel, settings); err != nil {
		return err
	}
	if d.ConfigCiphertext, err = s.sender.encryptConfig(d.ID, settings); err != nil {
		return err
	}
	d.ConsecutiveFailures = 0
	d.DisabledReason = nil
	return nil
}

func setIfPresent(field *bool, value *bool) {
	if value != nil {
		*field = *value
	}
}

// DeleteGuarded removes a destination after check accepts its revision.
func (s *PluginDestinationService) DeleteGuarded(ctx context.Context, userID int, profileID, id string, check func(int64) error) error {
	return s.repo.DeleteGuarded(ctx, userID, profileID, id, check)
}

// ListPage lists the account profile's destinations with their kinds.
func (s *PluginDestinationService) ListPage(ctx context.Context, userID int, profileID string, limit int, after *Cursor) ([]PluginDestinationView, error) {
	rows, err := s.repo.ListPage(ctx, userID, profileID, limit, after)
	if err != nil {
		return nil, err
	}
	channels, err := s.Channels(ctx)
	if err != nil {
		// The list still answers while the plugin runtime can't; every
		// destination then reads as unavailable.
		channels = nil
	}
	views := make([]PluginDestinationView, 0, len(rows))
	for _, row := range rows {
		views = append(views, s.view(row, channels))
	}
	return views, nil
}

// View describes one destination for its owner.
func (s *PluginDestinationService) View(ctx context.Context, d PluginDestination) PluginDestinationView {
	channels, _ := s.Channels(ctx)
	return s.view(d, channels)
}

func (s *PluginDestinationService) view(d PluginDestination, channels []PluginChannel) PluginDestinationView {
	view := PluginDestinationView{PluginDestination: d}
	for i := range channels {
		if channels[i].PluginChannelRef == d.Ref() {
			view.Channel = &channels[i]
		}
	}
	if view.Channel == nil {
		return view
	}
	stored, err := s.sender.decryptConfig(&d)
	if err != nil {
		return view
	}
	view.Settings, view.SecretsSet = publicPluginSettings(*view.Channel, stored)
	return view
}

// Test sends a test message to the destination now. It doesn't touch the
// delivery queue or the failure count.
func (s *PluginDestinationService) Test(ctx context.Context, userID int, profileID, id string) (*PluginDestinationTestResult, error) {
	host := s.sender.host()
	if host == nil {
		return nil, ErrPluginChannelUnavailable
	}
	d, err := s.repo.GetByID(ctx, userID, profileID, id)
	if err != nil {
		return nil, err
	}
	row := sampleDeliveryRow(profileID)
	row.UserID = userID
	result := s.sender.send(ctx, host, d, row, 1, true)
	return &PluginDestinationTestResult{
		OK:         result.outcome == pluginSendDelivered,
		Message:    result.message,
		DurationMS: result.duration.Milliseconds(),
	}, nil
}

// validate asks the plugin whether settings work.
func (s *PluginDestinationService) validate(ctx context.Context, channel PluginChannel, settings map[string]any) error {
	host := s.sender.host()
	if host == nil {
		return ErrPluginChannelUnavailable
	}
	config, err := structpb.NewStruct(settings)
	if err != nil {
		return fmt.Errorf("%w: settings must be JSON values", ErrPluginDestinationInvalid)
	}
	callCtx, cancel := context.WithTimeout(ctx, pluginValidateTimeout)
	defer cancel()
	resp, err := host.ValidateDestination(callCtx, channel.PluginChannelRef, &pluginv1.ValidateNotificationDestinationRequest{
		Config:       config,
		CapabilityId: channel.CapabilityID,
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPluginChannelUnavailable, err)
	}
	if !resp.GetValid() {
		message := resp.GetMessage()
		if message == "" {
			message = "The plugin didn't accept these settings."
		}
		return &PluginDestinationRejectedError{Message: message}
	}
	return nil
}

// mergePluginSettings applies a settings write over stored settings: a
// non-secret field is set, or removed when sent as null; a secret field is
// set only when sent non-empty, so leaving it blank keeps the stored value;
// clearSecrets removes secrets. Fields the schema doesn't declare are
// refused.
func mergePluginSettings(channel PluginChannel, stored, write map[string]any, clearSecrets []string) (map[string]any, error) {
	secrets := channel.SecretFields()
	declared, restricted := channel.declaredFields()
	merged := make(map[string]any, len(stored)+len(write))
	for key, value := range stored {
		merged[key] = value
	}
	for key, value := range write {
		if _, ok := declared[key]; restricted && !ok {
			return nil, fmt.Errorf("%w: %q is not a setting of this destination", ErrPluginDestinationInvalid, key)
		}
		if slices.Contains(secrets, key) {
			if text, isString := value.(string); value == nil || (isString && text == "") {
				continue
			}
		}
		if value == nil {
			delete(merged, key)
			continue
		}
		merged[key] = value
	}
	for _, key := range clearSecrets {
		if !slices.Contains(secrets, key) {
			return nil, fmt.Errorf("%w: %q is not a secret setting", ErrPluginDestinationInvalid, key)
		}
		delete(merged, key)
	}
	return merged, nil
}

// publicPluginSettings splits stored settings into what the owner may read
// and the names of the secret fields that hold a value.
func publicPluginSettings(channel PluginChannel, stored map[string]any) (map[string]any, []string) {
	secrets := channel.SecretFields()
	public := make(map[string]any, len(stored))
	var set []string
	for key, value := range stored {
		if slices.Contains(secrets, key) {
			set = append(set, key)
			continue
		}
		public[key] = value
	}
	slices.Sort(set)
	return public, set
}
