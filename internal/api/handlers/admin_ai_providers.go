package handlers

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
)

var (
	ErrAIProviderNotFound = errors.New("AI provider not found")
	// ErrAIProviderInvalid wraps a message safe to show the administrator.
	ErrAIProviderInvalid = errors.New("invalid AI provider")
	// ErrAIProviderConflict wraps a message naming the jobs that block the change.
	ErrAIProviderConflict = errors.New("AI provider is in use")
)

// AdminAIProviderView is one connected provider without its key.
type AdminAIProviderView struct {
	ID           string
	Type         string
	Name         string
	BaseURL      string
	HasAPIKey    bool
	Capabilities []string
	UsedBy       []string
}

// AdminAIProviderInput carries the editable fields. On update a nil field is
// left as stored; an empty APIKey clears the stored key.
type AdminAIProviderInput struct {
	Type         string
	Name         *string
	BaseURL      *string
	APIKey       *string
	Capabilities []string
}

// AdminAIProviderCheckInput is a provider draft to test without saving. ID
// names the stored provider it edits, if any, whose key the check may reuse.
type AdminAIProviderCheckInput struct {
	ID      string
	Type    string
	BaseURL string
	APIKey  *string
}

// AdminAIProviderCheckResult reports whether the provider answered and the
// models it lists.
type AdminAIProviderCheckResult struct {
	Success bool
	Message string
	Models  []llm.ListedModel
}

func aiProviderInvalid(err error) error {
	return fmt.Errorf("%w: %s", ErrAIProviderInvalid, err.Error())
}

// ListAIProviders returns every connected provider in stored order.
func (h *AdminHandler) ListAIProviders(ctx context.Context) ([]AdminAIProviderView, error) {
	if h.SettingsRepo == nil {
		return nil, ErrAdminSettingsUnavailable
	}
	stored, err := h.SettingsRepo.GetAll(ctx)
	if err != nil {
		return nil, err
	}
	registry, err := config.ParseAIProviderRegistry(stored[config.AIProvidersSettingKey])
	if err != nil {
		return nil, err
	}
	effective := h.effectiveAdminSettings(stored)
	views := make([]AdminAIProviderView, 0, len(registry.Providers))
	for _, provider := range registry.Providers {
		views = append(views, aiProviderView(provider, effective))
	}
	return views, nil
}

// CreateAIProvider connects a provider.
func (h *AdminHandler) CreateAIProvider(ctx context.Context, in AdminAIProviderInput) (AdminAIProviderView, error) {
	providerType := config.AIProviderType(in.Type)
	spec, ok := config.LookupAIProviderType(providerType)
	if !ok {
		return AdminAIProviderView{}, aiProviderInvalid(fmt.Errorf("unknown AI provider type %q", in.Type))
	}
	provider := config.AIProvider{
		ID:           config.NewAIProviderID(providerType),
		Type:         providerType,
		Name:         spec.Label,
		BaseURL:      spec.DefaultBaseURL,
		Capabilities: aiCapabilities(in.Capabilities),
	}
	if in.Name != nil {
		provider.Name = *in.Name
	}
	if in.BaseURL != nil {
		provider.BaseURL = *in.BaseURL
	}
	if in.APIKey != nil {
		provider.APIKey = *in.APIKey
	}
	return h.mutateAIProviders(ctx, provider.ID, func(registry *config.AIProviderRegistry, _ map[string]string) error {
		normalized, err := config.NormalizeAIProvider(provider)
		if err != nil {
			return aiProviderInvalid(err)
		}
		registry.Providers = append(registry.Providers, normalized)
		return nil
	})
}

// UpdateAIProvider edits a provider. Its type is fixed. A new address must
// come with the key it should use, so a saved key never follows the provider
// to another host unasked.
func (h *AdminHandler) UpdateAIProvider(ctx context.Context, id string, in AdminAIProviderInput) (AdminAIProviderView, error) {
	return h.mutateAIProviders(ctx, id, func(registry *config.AIProviderRegistry, _ map[string]string) error {
		i := aiProviderIndex(registry, id)
		if i < 0 {
			return ErrAIProviderNotFound
		}
		provider := registry.Providers[i]
		if in.Type != "" && in.Type != string(provider.Type) {
			return aiProviderInvalid(errors.New("a provider's type cannot change; connect a new provider instead"))
		}
		if in.Name != nil {
			provider.Name = *in.Name
		}
		if in.BaseURL != nil && strings.TrimRight(strings.TrimSpace(*in.BaseURL), "/") != provider.BaseURL {
			if in.APIKey == nil && provider.APIKey != "" && endpointAuthority(*in.BaseURL) != endpointAuthority(provider.BaseURL) {
				return aiProviderInvalid(errors.New("enter the API key again when changing the provider's address"))
			}
			provider.BaseURL = *in.BaseURL
		}
		if in.APIKey != nil {
			provider.APIKey = *in.APIKey
		}
		if in.Capabilities != nil {
			provider.Capabilities = aiCapabilities(in.Capabilities)
		}
		normalized, err := config.NormalizeAIProvider(provider)
		if err != nil {
			return aiProviderInvalid(err)
		}
		registry.Providers[i] = normalized
		return nil
	})
}

// DeleteAIProvider disconnects a provider no job uses.
func (h *AdminHandler) DeleteAIProvider(ctx context.Context, id string) error {
	_, err := h.mutateAIProviders(ctx, "", func(registry *config.AIProviderRegistry, effective map[string]string) error {
		i := aiProviderIndex(registry, id)
		if i < 0 {
			return ErrAIProviderNotFound
		}
		if users := config.AIProviderUsers(effective, id); len(users) > 0 {
			return fmt.Errorf("%w: %s is used by the %s; choose another provider there first", ErrAIProviderConflict, registry.Providers[i].Name, aiJobLabels(users))
		}
		registry.Providers = append(registry.Providers[:i], registry.Providers[i+1:]...)
		// An idle binding, such as the text provider kept while the ChatGPT
		// plan serves text, goes with it.
		for _, key := range config.AIProviderBindingKeys() {
			if strings.TrimSpace(effective[key]) == id {
				effective[key] = ""
			}
		}
		return nil
	})
	return err
}

// mutateAIProviders applies change to the stored registry under the settings
// lock, validates the result together with every binding, and announces the
// change so each node reloads its AI configuration. It returns the provider
// named viewID afterwards, when given.
func (h *AdminHandler) mutateAIProviders(
	ctx context.Context,
	viewID string,
	change func(registry *config.AIProviderRegistry, effective map[string]string) error,
) (AdminAIProviderView, error) {
	if h.SettingsRepo == nil {
		return AdminAIProviderView{}, ErrAdminSettingsUnavailable
	}
	var (
		view            AdminAIProviderView
		changeErr       error
		changed         []string
		embeddingsMoved bool
	)
	err := updateServerSettingsInTransaction(ctx, h.SettingsRepo, func(stored map[string]string, _ pgx.Tx) (map[string]string, error) {
		registry, err := config.ParseAIProviderRegistry(stored[config.AIProvidersSettingKey])
		if err != nil {
			changeErr = err
			return nil, err
		}
		effective := h.effectiveAdminSettings(stored)
		before := maps.Clone(effective)
		if err := change(&registry, effective); err != nil {
			changeErr = err
			return nil, err
		}
		if err := registry.Validate(); err != nil {
			changeErr = aiProviderInvalid(err)
			return nil, changeErr
		}
		encoded := registry.Encode()
		writes := map[string]string{config.AIProvidersSettingKey: encoded}
		for _, key := range config.AIProviderBindingKeys() {
			if effective[key] != before[key] {
				writes[key] = effective[key]
			}
		}
		effective[config.AIProvidersSettingKey] = encoded
		if err := config.ValidateAIProviderBindings(effective); err != nil {
			changeErr = fmt.Errorf("%w: %s", ErrAIProviderConflict, err.Error())
			return nil, changeErr
		}
		embeddingsMoved = embeddingEndpointMoved(before, effective)
		if provider, ok := registry.Find(viewID); ok {
			view = aiProviderView(provider, effective)
		}
		for key := range writes {
			changed = append(changed, key)
		}
		return writes, nil
	})
	if changeErr != nil {
		return AdminAIProviderView{}, changeErr
	}
	if err != nil {
		return AdminAIProviderView{}, err
	}
	if embeddingsMoved {
		// The engine and its endpoint are built at startup.
		h.markServerRestartRequired("setting:" + config.EmbeddingProviderSettingKey)
	}
	for _, key := range changed {
		if h.EventBus != nil {
			_ = h.EventBus.Publish(ctx, cache.ChannelAdmin, cache.Event{Type: cache.EventSettingsChanged, Payload: key})
		}
		if h.OnServerSettingUpdated != nil {
			// The registry's value carries keys; listeners only need the name.
			h.OnServerSettingUpdated(ctx, key, "")
		}
	}
	return view, nil
}

// embeddingEndpointMoved reports whether a registry change moves the
// recommendation embeddings to another endpoint, which applies at restart.
// The embedding lock follows the model, not the address, so a move needs no
// reset.
func embeddingEndpointMoved(before, after map[string]string) bool {
	key := config.EmbeddingProviderSettingKey
	beforeURL, _ := config.ResolveAIEndpoint(before, key, config.AICapabilityEmbeddings)
	afterURL, _ := config.ResolveAIEndpoint(after, key, config.AICapabilityEmbeddings)
	return beforeURL != afterURL
}

// CheckAIProvider asks a provider draft for its models without saving it. A
// stored key is reused only for the address it was saved with.
func (h *AdminHandler) CheckAIProvider(ctx context.Context, in AdminAIProviderCheckInput) (AdminAIProviderCheckResult, error) {
	draft := config.AIProvider{ID: "draft", Type: config.AIProviderType(in.Type), Name: "Draft", BaseURL: in.BaseURL}
	if in.APIKey != nil {
		draft.APIKey = *in.APIKey
	} else if in.ID != "" {
		stored, err := h.storedAIProvider(ctx, in.ID)
		if err != nil {
			return AdminAIProviderCheckResult{}, err
		}
		if endpointAuthority(stored.BaseURL) == endpointAuthority(in.BaseURL) {
			draft.APIKey = stored.APIKey
		}
	}
	if spec, ok := config.LookupAIProviderType(draft.Type); ok && spec.Capabilities == nil {
		// A custom draft's capabilities do not change how it is reached.
		draft.Capabilities = []config.AICapability{config.AICapabilityChat}
	}
	normalized, err := config.NormalizeAIProvider(draft)
	if err != nil {
		return AdminAIProviderCheckResult{}, aiProviderInvalid(err)
	}
	return listAIProviderModels(ctx, normalized), nil
}

// AIProviderModels lists a stored provider's models.
func (h *AdminHandler) AIProviderModels(ctx context.Context, id string) (AdminAIProviderCheckResult, error) {
	provider, err := h.storedAIProvider(ctx, id)
	if err != nil {
		return AdminAIProviderCheckResult{}, err
	}
	return listAIProviderModels(ctx, provider), nil
}

func (h *AdminHandler) storedAIProvider(ctx context.Context, id string) (config.AIProvider, error) {
	if h.SettingsRepo == nil {
		return config.AIProvider{}, ErrAdminSettingsUnavailable
	}
	raw, err := h.SettingsRepo.Get(ctx, config.AIProvidersSettingKey)
	if err != nil {
		return config.AIProvider{}, err
	}
	registry, err := config.ParseAIProviderRegistry(raw)
	if err != nil {
		return config.AIProvider{}, err
	}
	provider, ok := registry.Find(id)
	if !ok {
		return config.AIProvider{}, ErrAIProviderNotFound
	}
	return provider, nil
}

// listAIProviderModels reports in messages authored here, never in provider
// text, which can echo keys or addresses.
func listAIProviderModels(ctx context.Context, provider config.AIProvider) AdminAIProviderCheckResult {
	listing, baseURL := llm.ModelListingOpenAI, provider.Endpoint(config.AICapabilityChat)
	switch provider.Type {
	case config.AIProviderGemini:
		listing, baseURL = llm.ModelListingGemini, provider.BaseURL
	case config.AIProviderOllama:
		listing, baseURL = llm.ModelListingOllama, provider.BaseURL
	}
	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	models, err := llm.ListModels(checkCtx, listing, baseURL, provider.APIKey)
	if errors.Is(err, llm.ErrModelListingUnsupported) {
		return AdminAIProviderCheckResult{Success: true, Message: "The provider answered but does not list its models. Type model names by hand."}
	}
	if err != nil {
		return AdminAIProviderCheckResult{Message: aiProviderCheckFailureMessage(err)}
	}
	return AdminAIProviderCheckResult{Success: true, Message: fmt.Sprintf("Connected. The provider lists %d models.", len(models)), Models: models}
}

func aiProviderCheckFailureMessage(err error) string {
	if status, ok := errors.AsType[*llm.HTTPError](err); ok {
		switch {
		case status.StatusCode == 401:
			return "The provider rejected the API key."
		case status.StatusCode == 403:
			return "The provider denied access. Check the API key's permissions."
		case status.StatusCode == 402:
			return "The provider requires payment. Check your balance and spending limit."
		case status.StatusCode == 429:
			return "The provider is rate limiting requests. Try again later."
		case status.StatusCode >= 300 && status.StatusCode < 400:
			return "The provider redirected the request. Enter the address it redirects to."
		case status.StatusCode >= 500:
			return "The provider is temporarily unavailable. Try again later."
		}
		return fmt.Sprintf("The provider answered with HTTP %d.", status.StatusCode)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "The provider did not answer in time. Check the address and that Silo can reach it."
	}
	return "Silo could not reach the provider. Check the address and that Silo can reach it."
}

func aiProviderView(provider config.AIProvider, effective map[string]string) AdminAIProviderView {
	capabilities := make([]string, len(provider.Capabilities))
	for i, capability := range provider.Capabilities {
		capabilities[i] = string(capability)
	}
	users := config.AIProviderUsers(effective, provider.ID)
	usedBy := make([]string, len(users))
	for i, job := range users {
		usedBy[i] = string(job)
	}
	return AdminAIProviderView{
		ID:           provider.ID,
		Type:         string(provider.Type),
		Name:         provider.Name,
		BaseURL:      provider.BaseURL,
		HasAPIKey:    provider.APIKey != "",
		Capabilities: capabilities,
		UsedBy:       usedBy,
	}
}

func aiProviderIndex(registry *config.AIProviderRegistry, id string) int {
	return slices.IndexFunc(registry.Providers, func(provider config.AIProvider) bool { return provider.ID == id })
}

func aiCapabilities(values []string) []config.AICapability {
	if values == nil {
		return nil
	}
	capabilities := make([]config.AICapability, len(values))
	for i, value := range values {
		capabilities[i] = config.AICapability(value)
	}
	return capabilities
}

func aiJobLabels(jobs []config.AIJob) string {
	labels := make([]string, len(jobs))
	for i, job := range jobs {
		labels[i] = job.Label()
	}
	return strings.Join(labels, " and ")
}
