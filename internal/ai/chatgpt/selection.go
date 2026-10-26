package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

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
var ErrPlanIneligible = llm.ErrPlanIneligible

// ErrUsageLimited is local admission state, not a fresh provider quota response.
var ErrUsageLimited = errors.New("ChatGPT plan usage is paused; use Test text model to recheck usage")

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

// SettingsTransition contains the exact settings surrounding a committed activation.
// Transports expose keyed validators and whether this activation first connected.
type SettingsTransition struct {
	Before, After   map[string]string
	FirstConnection bool
}

func (s *Service) mutateSettingsWithSnapshot(ctx context.Context, fn func(*credentials, map[string]string) error) (*SettingsTransition, error) {
	store, ok := s.store.(selectionStore)
	if !ok {
		values, err := s.selectionSettings(ctx)
		if err != nil {
			return nil, err
		}
		if config.EffectiveAdminSettings(values)[authModeSetting] == authModeChatGPT {
			return nil, errors.New("ChatGPT settings require coordinated updates")
		}
		return nil, s.mutate(ctx, func(c *credentials) error { return fn(c, values) })
	}
	changed := make(map[string]string)
	var transition *SettingsTransition
	err := store.UpdateValueAndSettingsAtomic(ctx, CredentialsKey, func(values map[string]string) (map[string]string, error) {
		c, err := decode(values[CredentialsKey])
		if err != nil {
			return nil, err
		}
		before := maps.Clone(values)
		hadConnected := c.HasConnected
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
				changed[key] = values[key]
			}
		}
		after := maps.Clone(before)
		maps.Copy(after, writes)
		transition = &SettingsTransition{Before: before, After: after, FirstConnection: !hadConnected && c.HasConnected}
		return writes, nil
	})
	if err == nil && s.OnSettingUpdated != nil && len(changed) > 0 {
		notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		for _, key := range []string{chatModelSetting, reasoningEffortSetting} {
			if value, ok := changed[key]; ok {
				s.OnSettingUpdated(notifyCtx, key, value)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return transition, nil
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

func (s *Service) SelectAccountWithSettings(ctx context.Context, clientID string) (*SettingsTransition, error) {
	values, err := s.selectionSettings(ctx)
	if err != nil {
		return nil, err
	}
	var models []Model
	if config.EffectiveAdminSettings(values)[authModeSetting] == authModeChatGPT {
		models, err = s.modelsForSource(ctx, func(ctx context.Context) (string, error) { return s.bearerToken(ctx, clientID, true) })
		if err != nil {
			return nil, err
		}
	}
	return s.mutateSettingsWithSnapshot(ctx, func(c *credentials, values map[string]string) error {
		i := slices.IndexFunc(c.Accounts, func(a account) bool { return a.ClientID == clientID })
		if i >= 0 && c.Accounts[i].PlanIneligible {
			return ErrPlanIneligible
		}
		if i < 0 || c.Accounts[i].RefreshToken == "" || c.Accounts[i].NeedsLogin {
			return ErrSignInRequired
		}
		if err := reconcileModel(values, models); err != nil {
			return err
		}
		c.Active, c.Pending, c.LoginID, c.LoginResult = clientID, nil, "", loginIdle
		c.HasConnected = true
		c.LoginFirstConnection = false
		return nil
	})
}

// TextConfig snapshots provider settings and the active registration together,
// independently of config watcher delays. Subscription credentials remain pinned
// to this registration and are never installed in an API-key configuration.
func (s *Service) TextConfig(ctx context.Context) (llm.Config, error) {
	return s.textConfig(ctx, false)
}

// CheckTextConfig binds an explicit usage check to the registration and pause
// present at its start, including a draft switch from API-key authentication.
func (s *Service) CheckTextConfig(ctx context.Context) (llm.Config, error) {
	return s.textConfig(ctx, true)
}

func (s *Service) textConfig(ctx context.Context, recheckUsage bool) (llm.Config, error) {
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
	if recheckUsage {
		cfg.AuthMode = authModeChatGPT
	}
	if cfg.AuthMode != authModeChatGPT {
		return cfg, nil
	}
	c, err := decode(values[CredentialsKey])
	if err != nil {
		return llm.Config{}, err
	}
	i := slices.IndexFunc(c.Accounts, func(a account) bool { return a.ClientID == c.Active })
	if i >= 0 && c.Accounts[i].PlanIneligible {
		return llm.Config{}, ErrPlanIneligible
	}
	if i >= 0 && c.Accounts[i].UsageLimited && !recheckUsage {
		return llm.Config{}, ErrUsageLimited
	}
	if i < 0 || c.Accounts[i].RefreshToken == "" || c.Accounts[i].NeedsLogin {
		return llm.Config{}, ErrSignInRequired
	}
	clientID := c.Active
	cfg.ChatGPTTokenSource = func(context.Context) (func(context.Context) (string, error), error) {
		return func(ctx context.Context) (string, error) { return s.bearerToken(ctx, clientID, recheckUsage) }, nil
	}
	cfg.ChatGPTTokenRejected = s.RejectToken
	cfg.ChatGPTPlanRejected = func(ctx context.Context, token string) error { return s.rejectPlan(ctx, clientID, token) }
	cfg.ChatGPTUsageLimited = func(ctx context.Context, token string) error { return s.pauseUsage(ctx, clientID, token) }
	if recheckUsage {
		pauseID := c.Accounts[i].UsagePauseID
		cfg.ChatGPTUsageVerified = func(ctx context.Context, token string) error {
			return s.verifyUsageToken(ctx, clientID, token, pauseID)
		}
	}
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
	validation.models, err = s.modelsForSource(ctx, func(ctx context.Context) (string, error) { return s.bearerToken(ctx, c.Active, true) })
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
	if i >= 0 && c.Accounts[i].PlanIneligible {
		return ErrPlanIneligible
	}
	if i < 0 || c.Accounts[i].NeedsLogin || c.Accounts[i].RefreshToken == "" {
		return ErrSignInRequired
	}
	if !slices.ContainsFunc(v.models, func(model Model) bool { return model.ID == effective[chatModelSetting] }) {
		return ErrModelUnavailable
	}
	return nil
}

func (s *Service) modelCatalog(ctx context.Context, token string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var response struct {
		Models []struct {
			Slug       string `json:"slug"`
			Name       string `json:"display_name"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	for attempt := range 3 {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(&transientProviderError{}, err)
		}
		err := s.getJSON(ctx, s.modelsURL, token, &response)
		if err == nil {
			break
		}
		temporary, retry := errors.AsType[*transientProviderError](err)
		if !retry || attempt == 2 {
			return nil, err
		}
		if !waitProviderRetry(ctx, attempt, temporary.retryAfter) {
			return nil, errors.Join(err, ctx.Err())
		}
	}
	models := []Model{}
	for _, m := range response.Models {
		// These Responses models do not support the streaming text transport.
		if m.Visibility == visibilityListed && m.Slug != "" && !strings.HasPrefix(m.Slug, "o1-pro") && !strings.HasPrefix(m.Slug, "o3-pro") && !strings.HasPrefix(m.Slug, "gpt-5.5-pro") {
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
