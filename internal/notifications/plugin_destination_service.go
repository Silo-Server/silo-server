package notifications

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// pluginValidateTimeout bounds a plugin's ValidateDestination call.
const pluginValidateTimeout = 10 * time.Second

// PluginDestinationService manages profiles' plugin destinations. Settings
// are checked by the plugin that provides the channel before they are saved,
// stored encrypted, and returned without their secret fields.
type PluginDestinationService struct {
	repo   *PluginDestinationRepository
	sender *pluginDestinationSender
	// profileExists asks the user store, which owns profiles in every
	// configuration (PostgreSQL or the per-account SQLite store), whether an
	// account profile exists. nil skips the check.
	profileExists func(ctx context.Context, userID int, profileID string) (bool, error)
}

func newPluginDestinationService(repo *PluginDestinationRepository, sender *pluginDestinationSender) *PluginDestinationService {
	return &PluginDestinationService{repo: repo, sender: sender}
}

// PluginDestinationInput is the create and update request. On create the
// channel and name are required. On update a nil field keeps its value, and
// Settings merges over the stored settings: a secret field sent blank or
// left out keeps its stored value, and ClearSecrets removes one.
type PluginDestinationInput struct {
	Channel        PluginChannelRef
	Name           *string
	Settings       map[string]any
	ClearSecrets   []string
	Enabled        *bool
	NotifyNextUp   *bool
	NotifyRequests *bool
}

// PluginDestinationView is a destination as its owner sees it.
type PluginDestinationView struct {
	PluginDestination
	// Channel is the channel's current description, or nil when no enabled
	// plugin provides it now.
	Channel *PluginChannel
	// Settings holds the stored settings without secret fields; nil when
	// the channel is unavailable, since its secret fields are unknown then.
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
// the plugin runtime is wired. Whether any channel exists is Channels' answer.
func (s *PluginDestinationService) Available() bool {
	return s != nil && s.sender.host() != nil
}

// Channels lists the channels the enabled plugins provide.
func (s *PluginDestinationService) Channels(ctx context.Context) ([]PluginChannel, error) {
	host := s.sender.host()
	if host == nil {
		return nil, nil
	}
	return host.Channels(ctx)
}

// connect resolves the installation that provides ref, so the schema the
// settings are checked against and the plugin that validates them are the
// same installation's.
func (s *PluginDestinationService) connect(ctx context.Context, ref PluginChannelRef) (*PluginChannel, PluginChannelClient, error) {
	host := s.sender.host()
	if host == nil {
		return nil, nil, ErrPluginChannelUnavailable
	}
	return host.Connect(ctx, ref)
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
	if len(in.ClearSecrets) > 0 {
		return nil, fmt.Errorf("%w: clear_secrets applies only to an update", ErrPluginDestinationInvalid)
	}
	channel, client, err := s.connect(ctx, in.Channel)
	if err != nil {
		return nil, err
	}
	settings, err := mergePluginSettings(*channel, pluginSettings{}, in.Settings, nil)
	if err != nil {
		return nil, err
	}
	if err := s.validate(ctx, *channel, client, settings, pluginSettings{}); err != nil {
		return nil, err
	}
	d := PluginDestination{
		ID:             ulid.Make().String(),
		UserID:         userID,
		ProfileID:      profileID,
		PluginID:       channel.PluginID,
		CapabilityID:   channel.CapabilityID,
		Name:           name,
		Enabled:        boolOrDefault(in.Enabled, true),
		NotifyNextUp:   boolOrDefault(in.NotifyNextUp, true),
		NotifyRequests: boolOrDefault(in.NotifyRequests, true),
		CreatedAt:      time.Now(),
	}
	if d.ConfigCiphertext, err = s.sender.encryptConfig(d.ID, settings); err != nil {
		return nil, err
	}
	if err := s.checkProfile(ctx, userID, profileID); err != nil {
		return nil, err
	}
	if err := s.repo.InsertWithLimit(ctx, d, PluginDestinationMaxPerProfile); err != nil {
		return nil, err
	}
	// The profile is checked again after the insert commits. A deletion
	// that committed before this check is seen here, and the new
	// destination goes; one that commits after it purges the profile's
	// destinations once it has, this one included.
	if err := s.checkProfile(ctx, userID, profileID); err != nil {
		if errors.Is(err, ErrPluginDestinationProfileGone) {
			if deleteErr := s.repo.deleteByID(context.WithoutCancel(ctx), d.ID); deleteErr != nil {
				return nil, fmt.Errorf("remove a destination of a deleted profile: %w", deleteErr)
			}
		}
		return nil, err
	}
	return s.repo.GetByID(ctx, userID, profileID, d.ID)
}

// checkProfile refuses a create for a profile that doesn't exist.
func (s *PluginDestinationService) checkProfile(ctx context.Context, userID int, profileID string) error {
	if s.profileExists == nil {
		return nil
	}
	exists, err := s.profileExists(ctx, userID, profileID)
	if err != nil {
		return fmt.Errorf("check plugin destination profile: %w", err)
	}
	if !exists {
		return ErrPluginDestinationProfileGone
	}
	return nil
}

// UpdateGuarded applies in after check accepts the stored revision, under
// the row lock. Changed settings are checked by the plugin again, and like a
// changed webhook URL they clear the failure streak and the disable reason.
//
// The plugin can take seconds to answer, so it checks the settings before
// the row is locked, against the revision read then. The write applies only
// while the row is still at that revision; otherwise it fails with
// PluginDestinationConflictError and writes nothing.
func (s *PluginDestinationService) UpdateGuarded(ctx context.Context, userID int, profileID, id string, in PluginDestinationInput, check func(int64) error) (*PluginDestination, error) {
	var name string
	if in.Name != nil {
		validName, err := validateChannelName(*in.Name, ErrPluginDestinationInvalid)
		if err != nil {
			return nil, err
		}
		name = validName
	}
	var validated *PluginDestination
	if in.Settings != nil || len(in.ClearSecrets) > 0 {
		current, err := s.repo.GetByID(ctx, userID, profileID, id)
		if err != nil {
			return nil, err
		}
		if err := check(current.Revision); err != nil {
			return nil, err
		}
		if current.ConfigCiphertext, err = s.validatedSettings(ctx, current, in); err != nil {
			return nil, err
		}
		validated = current
	}
	return s.repo.UpdateGuarded(ctx, userID, profileID, id, func(d *PluginDestination) error {
		if err := check(d.Revision); err != nil {
			return err
		}
		if in.Name != nil {
			d.Name = name
		}
		if validated != nil {
			if d.Revision != validated.Revision {
				return &PluginDestinationConflictError{Revision: d.Revision}
			}
			d.ConfigCiphertext = validated.ConfigCiphertext
			d.ConsecutiveFailures = 0
			d.DisabledReason = nil
		}
		if in.Enabled != nil {
			d.Enabled = *in.Enabled
			if d.Enabled {
				d.ConsecutiveFailures = 0
				d.DisabledReason = nil
			}
		}
		setIfPresent(&d.NotifyNextUp, in.NotifyNextUp)
		setIfPresent(&d.NotifyRequests, in.NotifyRequests)
		return nil
	})
}

// validatedSettings merges a settings write over d's stored settings, has
// the plugin check the result, and returns it encrypted. Stored settings
// this server can't decrypt refuse the edit: merging over nothing would
// silently drop the stored secrets, and a server with the right key can
// still read them.
func (s *PluginDestinationService) validatedSettings(ctx context.Context, d *PluginDestination, in PluginDestinationInput) (string, error) {
	stored, err := s.sender.decryptConfig(d)
	if err != nil {
		return "", ErrPluginDestinationSettingsUnreadable
	}
	channel, client, err := s.connect(ctx, d.Ref())
	if err != nil {
		return "", err
	}
	settings, err := mergePluginSettings(*channel, stored, in.Settings, in.ClearSecrets)
	if err != nil {
		return "", err
	}
	if err := s.validate(ctx, *channel, client, settings, stored); err != nil {
		return "", err
	}
	return s.sender.encryptConfig(d.ID, settings)
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

// ListPage lists the account profile's destinations with their channels.
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
	result := s.sender.send(ctx, host, d, row, nil)
	if result.stopErr != nil {
		return nil, result.stopErr
	}
	return &PluginDestinationTestResult{
		OK:         result.outcome == pluginSendDelivered,
		Message:    result.message,
		DurationMS: result.duration.Milliseconds(),
	}, nil
}

// validate checks settings against the channel's schema, then asks the
// plugin whether they work. stored is what the settings replace, if anything.
func (s *PluginDestinationService) validate(ctx context.Context, channel PluginChannel, client PluginChannelClient, settings, stored pluginSettings) error {
	if err := checkPluginSettings(channel, settings.Values); err != nil {
		return err
	}
	config, err := structpb.NewStruct(settings.Values)
	if err != nil {
		return fmt.Errorf("%w: settings must be JSON values", ErrPluginDestinationInvalid)
	}
	callCtx, cancel := context.WithTimeout(ctx, pluginValidateTimeout)
	defer cancel()
	resp, err := client.ValidateDestination(callCtx, &pluginv1.ValidateNotificationDestinationRequest{
		Config:       config,
		CapabilityId: channel.CapabilityID,
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPluginChannelUnavailable, err)
	}
	if !resp.GetValid() {
		// The reason reaches the client, and secret settings never may.
		message := redactPluginText(resp.GetMessage(), &channel, settings, stored)
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
// clearSecrets removes secrets. A field saved as secret counts as secret
// even when the current schema no longer marks it, until a write replaces
// it, which then takes the current schema's classification. Fields the
// schema doesn't declare are refused in the write and dropped from the
// stored settings: a plugin update that removed or renamed a setting would
// otherwise keep sending the old key to validation, which the owner can't
// see or remove.
func mergePluginSettings(channel PluginChannel, stored pluginSettings, write map[string]any, clearSecrets []string) (pluginSettings, error) {
	declared := channel.declaredFields()
	merged := make(map[string]any, len(stored.Values)+len(write))
	for key, value := range stored.Values {
		if _, ok := declared[key]; ok {
			merged[key] = value
		}
	}
	var unknown []PluginSettingError
	for key := range write {
		if _, ok := declared[key]; !ok {
			unknown = append(unknown, PluginSettingError{Field: key, Code: "unknown_field", Message: "Not a setting of this destination."})
		}
	}
	if len(unknown) > 0 {
		slices.SortFunc(unknown, func(a, b PluginSettingError) int { return strings.Compare(a.Field, b.Field) })
		return pluginSettings{}, &PluginDestinationSettingsError{Fields: unknown}
	}
	replaced := make(map[string]bool, len(write))
	for key, value := range write {
		if stored.isSecret(channel, key) {
			if text, isString := value.(string); value == nil || (isString && text == "") {
				continue
			}
		}
		replaced[key] = true
		if value == nil {
			delete(merged, key)
			continue
		}
		merged[key] = value
	}
	for _, key := range clearSecrets {
		if !stored.isSecret(channel, key) {
			return pluginSettings{}, fmt.Errorf("%w: %q is not a secret setting", ErrPluginDestinationInvalid, key)
		}
		delete(merged, key)
	}
	secret := []string{}
	for key := range merged {
		// A value the save didn't replace keeps its record; settings without
		// one keep every such value write-only.
		if slices.Contains(channel.SecretFields, key) || (stored.isSecret(channel, key) && !replaced[key]) {
			secret = append(secret, key)
		}
	}
	slices.Sort(secret)
	return pluginSettings{Values: merged, WriteOnly: secret}, nil
}

// publicPluginSettings returns the stored settings the owner may read (the
// fields the current schema declares public and that weren't saved as
// secret) and the names of the secret fields that hold a value. A stored
// field the schema no longer declares is neither returned nor named.
func publicPluginSettings(channel PluginChannel, stored pluginSettings) (map[string]any, []string) {
	declared := channel.declaredFields()
	public := make(map[string]any, len(stored.Values))
	var set []string
	for key, value := range stored.Values {
		if _, ok := declared[key]; !ok {
			continue
		}
		if stored.isPublic(&channel, key) {
			public[key] = value
		} else {
			set = append(set, key)
		}
	}
	slices.Sort(set)
	return public, set
}
