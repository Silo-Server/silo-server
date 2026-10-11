package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/ai/reasoning"
	"github.com/Silo-Server/silo-server/internal/config"
)

const (
	authModeSetting        = "ai.auth_mode"
	chatModelSetting       = "ai.chat_model"
	legacyChatModelSetting = "subtitle_ai.chat_model"
	reasoningEffortSetting = "ai.reasoning_effort"
	authModeChatGPT        = "chatgpt"
)

var ErrModelUnavailable = errors.New("choose a model available to the selected ChatGPT account")
var ErrAccountChanged = errors.New("ChatGPT account changed; refresh settings and try again")

var selectionKeys = []string{CredentialsKey, authModeSetting, chatModelSetting, legacyChatModelSetting, reasoningEffortSetting, "ai.base_url", "subtitle_ai.base_url", "ai.api_key", "subtitle_ai.api_key"}

type selectionReader interface {
	GetMany(context.Context, ...string) (map[string]string, error)
}

type selectionStore interface {
	selectionReader
	UpdateValueAndSettingsAtomic(context.Context, string, func(map[string]string) (map[string]string, error)) error
}

func (s *Service) selectionSettings(ctx context.Context) (map[string]string, error) {
	if store, ok := s.store.(selectionReader); ok {
		return store.GetMany(ctx, selectionKeys...)
	}
	// Lightweight stores used outside the production router retain the original
	// credential-only behavior. Production always supplies the batch reader.
	values := make(map[string]string, len(selectionKeys))
	for _, key := range selectionKeys {
		value, err := s.store.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, nil
}

func (s *Service) mutateSettings(ctx context.Context, fn func(*credentials, map[string]string) error) error {
	store, ok := s.store.(selectionStore)
	if !ok {
		values, err := s.selectionSettings(ctx)
		if err != nil {
			return err
		}
		if config.EffectiveAdminSettings(values)[authModeSetting] == authModeChatGPT {
			return errors.New("ChatGPT settings require coordinated updates")
		}
		return s.mutate(ctx, func(c *credentials) error { return fn(c, values) })
	}
	return store.UpdateValueAndSettingsAtomic(ctx, CredentialsKey, func(values map[string]string) (map[string]string, error) {
		c, err := decode(values[CredentialsKey])
		if err != nil {
			return nil, err
		}
		before := maps.Clone(values)
		if err := fn(&c, values); err != nil {
			return nil, err
		}
		data, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		writes := map[string]string{CredentialsKey: string(data)}
		for _, key := range []string{chatModelSetting, reasoningEffortSetting} {
			if values[key] != before[key] {
				writes[key] = values[key]
			}
		}
		return writes, nil
	})
}

func reconcileModel(values map[string]string, models []Model) error {
	effective := config.EffectiveAdminSettings(values)
	if effective[authModeSetting] != authModeChatGPT {
		return nil
	}
	if len(models) == 0 {
		return ErrModelUnavailable
	}
	model := effective[chatModelSetting]
	if !slices.ContainsFunc(models, func(m Model) bool { return m.ID == model }) {
		model = models[0].ID
		values[chatModelSetting] = model
	}
	if reasoning.Validate(model, effective[reasoningEffortSetting]) != nil {
		values[reasoningEffortSetting] = ""
	}
	return nil
}

func (s *Service) selectAccount(ctx context.Context, clientID string) error {
	values, err := s.selectionSettings(ctx)
	if err != nil {
		return err
	}
	var models []Model
	if config.EffectiveAdminSettings(values)[authModeSetting] == authModeChatGPT {
		models, err = s.modelsForSource(ctx, func(ctx context.Context) (string, error) { return s.bearerTokenForAccount(ctx, clientID) })
		if err != nil {
			return err
		}
	}
	return s.mutateSettings(ctx, func(c *credentials, values map[string]string) error {
		i := slices.IndexFunc(c.Accounts, func(a account) bool { return a.ClientID == clientID })
		if i < 0 || c.Accounts[i].RefreshToken == "" || c.Accounts[i].NeedsLogin {
			return ErrSignInRequired
		}
		if err := reconcileModel(values, models); err != nil {
			return err
		}
		c.Active, c.Pending, c.LoginID, c.LoginResult = clientID, nil, "", loginIdle
		return nil
	})
}

// TextConfig snapshots provider settings and the active registration together,
// independently of config watcher delays. Subscription credentials remain pinned
// to this registration and are never installed in an API-key configuration.
func (s *Service) TextConfig(ctx context.Context) (llm.Config, error) {
	store, ok := s.store.(selectionReader)
	if !ok {
		return llm.Config{}, errors.New("text provider settings require atomic reads")
	}
	values, err := store.GetMany(ctx, selectionKeys...)
	if err != nil {
		return llm.Config{}, err
	}
	effective := config.EffectiveAdminSettings(values)
	cfg := llm.Config{AuthMode: effective[authModeSetting], BaseURL: effective["ai.base_url"], APIKey: effective["ai.api_key"], ChatModel: effective[chatModelSetting], ReasoningEffort: effective[reasoningEffortSetting]}
	if cfg.AuthMode != authModeChatGPT {
		return cfg, nil
	}
	c, err := decode(values[CredentialsKey])
	if err != nil {
		return llm.Config{}, err
	}
	i := slices.IndexFunc(c.Accounts, func(a account) bool { return a.ClientID == c.Active })
	if i < 0 || c.Accounts[i].RefreshToken == "" || c.Accounts[i].NeedsLogin {
		return llm.Config{}, ErrSignInRequired
	}
	clientID := c.Active
	cfg.ChatGPTTokenSource = func(context.Context) (func(context.Context) (string, error), error) {
		return func(ctx context.Context) (string, error) { return s.bearerTokenForAccount(ctx, clientID) }, nil
	}
	cfg.ChatGPTTokenRejected = s.RejectToken
	return cfg, nil
}

// ModelValidation carries a catalog fetched without the general settings lock.
// Check runs inside a settings transaction and rejects a changed registration.
type ModelValidation struct {
	clientID string
	models   []Model
}

func (s *Service) PrepareModelValidation(ctx context.Context, changes map[string]string) (*ModelValidation, error) {
	values, err := s.selectionSettings(ctx)
	if err != nil {
		return nil, err
	}
	prospective := maps.Clone(values)
	maps.Copy(prospective, changes)
	validation := &ModelValidation{}
	if config.EffectiveAdminSettings(prospective)[authModeSetting] != authModeChatGPT {
		return validation, nil
	}
	c, err := decode(values[CredentialsKey])
	if err != nil {
		return nil, err
	}
	validation.clientID = c.Active
	validation.models, err = s.modelsForSource(ctx, func(ctx context.Context) (string, error) { return s.bearerTokenForAccount(ctx, c.Active) })
	return validation, err
}

func (v *ModelValidation) Check(stored, prospective map[string]string) error {
	effective := config.EffectiveAdminSettings(prospective)
	if effective[authModeSetting] != authModeChatGPT {
		return nil
	}
	c, err := decode(stored[CredentialsKey])
	if err != nil {
		return err
	}
	if c.Active == "" || c.Active != v.clientID {
		return ErrAccountChanged
	}
	i := slices.IndexFunc(c.Accounts, func(a account) bool { return a.ClientID == c.Active })
	if i < 0 || c.Accounts[i].NeedsLogin || c.Accounts[i].RefreshToken == "" {
		return ErrSignInRequired
	}
	if !slices.ContainsFunc(v.models, func(model Model) bool { return model.ID == effective[chatModelSetting] }) {
		return ErrModelUnavailable
	}
	return nil
}

func (s *Service) modelCatalog(ctx context.Context, token string) ([]Model, error) {
	var response struct {
		Models []struct {
			Slug       string `json:"slug"`
			Name       string `json:"display_name"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := s.getJSON(ctx, s.modelsURL, token, &response); err != nil {
		return nil, err
	}
	models := []Model{}
	for _, m := range response.Models {
		if m.Visibility == visibilityListed && m.Slug != "" {
			models = append(models, Model{ID: m.Slug, Name: m.Name})
		}
	}
	return models, nil
}

func (s *Service) modelsForSource(ctx context.Context, source func(context.Context) (string, error)) ([]Model, error) {
	for attempt := range 2 {
		token, err := source(ctx)
		if err != nil {
			return nil, err
		}
		models, err := s.modelCatalog(ctx, token)
		if !errors.Is(err, ErrSignInRequired) {
			return models, err
		}
		if rejectErr := s.RejectToken(ctx, token, attempt == 1); rejectErr != nil {
			return nil, rejectErr
		}
		if attempt == 1 {
			return nil, err
		}
	}
	return nil, ErrProvider
}

func (s *Service) loginModels(ctx context.Context, tokens tokenResponse) ([]Model, error) {
	values, err := s.selectionSettings(ctx)
	if err != nil {
		return nil, err
	}
	if config.EffectiveAdminSettings(values)[authModeSetting] != authModeChatGPT {
		return nil, nil
	}
	return s.modelCatalog(ctx, tokens.AccessToken)
}
