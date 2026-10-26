package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// AI provider registry. An administrator connects each AI provider once, in
// ai.providers, and every AI job picks a provider from it by id: text through
// ai.text_provider_id, speech-to-text through ai.speech_provider_id. The
// ChatGPT plan is not stored here; it keeps its own credential bundle and is
// selected with ai.auth_mode.
const (
	// AIProvidersSettingKey holds the registry, API keys included, as one
	// encrypted value. Do not rename it: the ciphertext is bound to this name.
	AIProvidersSettingKey      = "ai.providers"
	AITextProviderSettingKey   = "ai.text_provider_id"
	AISpeechProviderSettingKey = "ai.speech_provider_id"
)

// Default addresses of the provider types that have one.
const (
	openAIBaseURL     = "https://api.openai.com"
	openRouterBaseURL = "https://openrouter.ai/api/v1"
	groqBaseURL       = "https://api.groq.com/openai"
	ollamaBaseURL     = "http://ollama:11434"
	speachesBaseURL   = "http://speaches:8000"
)

// aiAuthModeChatGPT is the ai.auth_mode that serves text from a ChatGPT plan.
const aiAuthModeChatGPT = "chatgpt"

// aiProvidersVersion is the newest legacy conversion PlanAIProviderConversion
// knows. Version 1 moved the text and speech endpoints into the registry.
const aiProvidersVersion = 1

const (
	maxAIProviders         = 32
	maxAIProviderNameRunes = 80
	maxAIProviderKeyBytes  = 4096
)

// AICapability is a kind of request a provider can serve.
type AICapability string

const (
	AICapabilityChat       AICapability = "chat"
	AICapabilityEmbeddings AICapability = "embeddings"
	AICapabilitySpeech     AICapability = "speech"
)

// AICapabilities lists every capability in display order.
var AICapabilities = []AICapability{AICapabilityChat, AICapabilityEmbeddings, AICapabilitySpeech}

// AIProviderType names a kind of provider. Known types fix their capabilities
// and know where each capability is served; a custom provider is any
// OpenAI-compatible endpoint whose capabilities the administrator declares.
type AIProviderType string

const (
	AIProviderOpenAI     AIProviderType = "openai"
	AIProviderGemini     AIProviderType = "gemini"
	AIProviderOpenRouter AIProviderType = "openrouter"
	AIProviderGroq       AIProviderType = "groq"
	AIProviderOllama     AIProviderType = "ollama"
	AIProviderWhisper    AIProviderType = "whisper"
	AIProviderCustom     AIProviderType = "custom"
)

// AIProviderTypeSpec describes a provider type.
type AIProviderTypeSpec struct {
	Type  AIProviderType
	Label string
	// DefaultBaseURL prefills a new provider. Empty for custom.
	DefaultBaseURL string
	// Capabilities is the fixed set a known type serves. Nil for custom.
	Capabilities []AICapability
	KeyRequired  bool
	// chatPath is appended to the base URL for chat requests.
	chatPath string
}

// AIProviderTypes lists every provider type in display order.
var AIProviderTypes = []AIProviderTypeSpec{
	{
		Type: AIProviderOpenAI, Label: "OpenAI", DefaultBaseURL: openAIBaseURL,
		Capabilities: []AICapability{AICapabilityChat, AICapabilityEmbeddings, AICapabilitySpeech}, KeyRequired: true,
	},
	{
		// Chat goes through Gemini's OpenAI-compatible surface; embeddings use
		// the native API, which the embedding client recognizes by host.
		Type: AIProviderGemini, Label: "Google Gemini", DefaultBaseURL: "https://generativelanguage.googleapis.com",
		Capabilities: []AICapability{AICapabilityChat, AICapabilityEmbeddings}, KeyRequired: true, chatPath: "/v1beta/openai",
	},
	{
		Type: AIProviderOpenRouter, Label: "OpenRouter", DefaultBaseURL: openRouterBaseURL,
		Capabilities: []AICapability{AICapabilityChat, AICapabilitySpeech}, KeyRequired: true,
	},
	{
		Type: AIProviderGroq, Label: "Groq", DefaultBaseURL: groqBaseURL,
		Capabilities: []AICapability{AICapabilityChat, AICapabilitySpeech}, KeyRequired: true,
	},
	{
		Type: AIProviderOllama, Label: "Ollama", DefaultBaseURL: ollamaBaseURL,
		Capabilities: []AICapability{AICapabilityChat, AICapabilityEmbeddings},
	},
	{
		Type: AIProviderWhisper, Label: "Self-hosted Whisper", DefaultBaseURL: speachesBaseURL,
		Capabilities: []AICapability{AICapabilitySpeech},
	},
	{Type: AIProviderCustom, Label: "OpenAI-compatible"},
}

// LookupAIProviderType returns the spec for a type.
func LookupAIProviderType(t AIProviderType) (AIProviderTypeSpec, bool) {
	i := slices.IndexFunc(AIProviderTypes, func(spec AIProviderTypeSpec) bool { return spec.Type == t })
	if i < 0 {
		return AIProviderTypeSpec{}, false
	}
	return AIProviderTypes[i], true
}

// endpoint returns where a provider of this type at baseURL serves cap.
func (s AIProviderTypeSpec) endpoint(baseURL string, capability AICapability) string {
	if capability == AICapabilityChat {
		return baseURL + s.chatPath
	}
	return baseURL
}

func (s AIProviderTypeSpec) supports(capability AICapability) bool {
	return s.Capabilities == nil || slices.Contains(s.Capabilities, capability)
}

// AIProvider is one connected provider.
type AIProvider struct {
	ID           string         `json:"id"`
	Type         AIProviderType `json:"type"`
	Name         string         `json:"name"`
	BaseURL      string         `json:"base_url"`
	APIKey       string         `json:"api_key,omitempty"`
	Capabilities []AICapability `json:"capabilities"`
}

// Supports reports whether the provider serves capability.
func (p AIProvider) Supports(capability AICapability) bool {
	return slices.Contains(p.Capabilities, capability)
}

// Endpoint returns the base URL a client uses for capability.
func (p AIProvider) Endpoint(capability AICapability) string {
	spec, _ := LookupAIProviderType(p.Type)
	return spec.endpoint(p.BaseURL, capability)
}

// AIProviderRegistry is the decoded ai.providers value.
type AIProviderRegistry struct {
	// Version records which legacy conversions have run.
	Version   int          `json:"version"`
	Providers []AIProvider `json:"providers"`
}

// ParseAIProviderRegistry decodes ai.providers. An empty value is an empty
// registry that no conversion has seen yet.
func ParseAIProviderRegistry(raw string) (AIProviderRegistry, error) {
	var registry AIProviderRegistry
	if strings.TrimSpace(raw) == "" {
		return registry, nil
	}
	if err := json.Unmarshal([]byte(raw), &registry); err != nil {
		return AIProviderRegistry{}, fmt.Errorf("%s is not valid JSON: %w", AIProvidersSettingKey, err)
	}
	return registry, nil
}

// Encode returns the stored form of the registry.
func (r AIProviderRegistry) Encode() string {
	if r.Providers == nil {
		r.Providers = []AIProvider{}
	}
	raw, _ := json.Marshal(r) // Strings, ints, and slices of them always encode.
	return string(raw)
}

// Find returns the provider with id.
func (r AIProviderRegistry) Find(id string) (AIProvider, bool) {
	if id == "" {
		return AIProvider{}, false
	}
	i := slices.IndexFunc(r.Providers, func(p AIProvider) bool { return p.ID == id })
	if i < 0 {
		return AIProvider{}, false
	}
	return r.Providers[i], true
}

// Resolve returns the endpoint and key the provider with id serves capability
// from. It reports false when there is no such provider or it does not serve
// capability, which leaves the job unconfigured.
func (r AIProviderRegistry) Resolve(id string, capability AICapability) (baseURL, apiKey string, ok bool) {
	provider, found := r.Find(id)
	if !found || !provider.Supports(capability) {
		return "", "", false
	}
	return provider.Endpoint(capability), provider.APIKey, true
}

// ResolveAIEndpoint resolves the provider bound by bindingKey in a settings
// snapshot. A registry that cannot be read resolves nothing.
func ResolveAIEndpoint(values map[string]string, bindingKey string, capability AICapability) (baseURL, apiKey string) {
	registry, err := ParseAIProviderRegistry(values[AIProvidersSettingKey])
	if err != nil {
		return "", ""
	}
	baseURL, apiKey, _ = registry.Resolve(strings.TrimSpace(values[bindingKey]), capability)
	return baseURL, apiKey
}

var aiProviderIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// NewAIProviderID returns a fresh id for a provider of type t.
func NewAIProviderID(t AIProviderType) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return string(t) + "-" + hex.EncodeToString(b[:])
}

// NormalizeAIProvider trims the editable fields, fills a known type's fixed
// capabilities, and reports the first problem with the provider.
func NormalizeAIProvider(p AIProvider) (AIProvider, error) {
	spec, ok := LookupAIProviderType(p.Type)
	if !ok {
		return AIProvider{}, fmt.Errorf("unknown AI provider type %q", p.Type)
	}
	if !aiProviderIDPattern.MatchString(p.ID) {
		return AIProvider{}, fmt.Errorf("AI provider id %q must be lowercase letters, digits, and hyphens", p.ID)
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || utf8.RuneCountInString(p.Name) > maxAIProviderNameRunes {
		return AIProvider{}, fmt.Errorf("AI provider name must be 1 to %d characters", maxAIProviderNameRunes)
	}
	baseURL, err := normalizeAdminURL("AI provider base URL", strings.TrimSpace(p.BaseURL))
	if err != nil {
		return AIProvider{}, err
	}
	if baseURL == "" {
		return AIProvider{}, errors.New("AI provider base URL is required")
	}
	if parsed, _ := url.Parse(baseURL); parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return AIProvider{}, errors.New("AI provider base URL cannot carry credentials, a query, or a fragment")
	}
	p.BaseURL = baseURL
	p.APIKey = strings.TrimSpace(p.APIKey)
	if len(p.APIKey) > maxAIProviderKeyBytes {
		return AIProvider{}, fmt.Errorf("AI provider API key must be at most %d bytes", maxAIProviderKeyBytes)
	}
	if spec.KeyRequired && p.APIKey == "" {
		return AIProvider{}, fmt.Errorf("%s requires an API key", spec.Label)
	}
	if spec.Capabilities != nil {
		p.Capabilities = slices.Clone(spec.Capabilities)
		return p, nil
	}
	capabilities := make([]AICapability, 0, len(AICapabilities))
	for _, capability := range AICapabilities {
		if slices.Contains(p.Capabilities, capability) {
			capabilities = append(capabilities, capability)
		}
	}
	for _, capability := range p.Capabilities {
		if !slices.Contains(AICapabilities, capability) {
			return AIProvider{}, fmt.Errorf("unknown AI capability %q", capability)
		}
	}
	if len(capabilities) == 0 {
		return AIProvider{}, errors.New("choose at least one thing the provider serves")
	}
	p.Capabilities = capabilities
	return p, nil
}

// Validate checks every provider and that ids and names are unique.
func (r AIProviderRegistry) Validate() error {
	if len(r.Providers) > maxAIProviders {
		return fmt.Errorf("at most %d AI providers can be connected", maxAIProviders)
	}
	ids := make(map[string]bool, len(r.Providers))
	names := make(map[string]bool, len(r.Providers))
	for _, provider := range r.Providers {
		normalized, err := NormalizeAIProvider(provider)
		if err != nil {
			return err
		}
		if ids[normalized.ID] {
			return fmt.Errorf("AI provider id %q is used twice", normalized.ID)
		}
		ids[normalized.ID] = true
		name := strings.ToLower(normalized.Name)
		if names[name] {
			return fmt.Errorf("another AI provider is already named %q", normalized.Name)
		}
		names[name] = true
	}
	return nil
}

// AIJob names a job that selects a provider.
type AIJob string

const (
	AIJobText   AIJob = "text"
	AIJobSpeech AIJob = "speech"
)

// aiProviderBindings maps each job to its binding setting and the capability
// it needs.
var aiProviderBindings = []struct {
	job        AIJob
	key        string
	capability AICapability
	label      string
}{
	{AIJobText, AITextProviderSettingKey, AICapabilityChat, "text model"},
	{AIJobSpeech, AISpeechProviderSettingKey, AICapabilitySpeech, "speech-to-text"},
}

// Label names the job in messages.
func (j AIJob) Label() string {
	for _, binding := range aiProviderBindings {
		if binding.job == j {
			return binding.label
		}
	}
	return string(j)
}

// AIProviderBindingKeys lists the settings that select a provider by id.
func AIProviderBindingKeys() []string {
	keys := make([]string, len(aiProviderBindings))
	for i, binding := range aiProviderBindings {
		keys[i] = binding.key
	}
	return keys
}

// ValidateAIProviderBindings rejects a registry that cannot be read and a
// binding that names a missing provider or one that does not serve the job.
func ValidateAIProviderBindings(values map[string]string) error {
	registry, err := ParseAIProviderRegistry(values[AIProvidersSettingKey])
	if err != nil {
		return err
	}
	for _, binding := range aiProviderBindings {
		id := strings.TrimSpace(values[binding.key])
		if id == "" {
			continue
		}
		provider, ok := registry.Find(id)
		if !ok {
			return fmt.Errorf("the %s provider %q is not connected", binding.label, id)
		}
		if !provider.Supports(binding.capability) {
			return fmt.Errorf("%s cannot serve the %s", provider.Name, binding.label)
		}
	}
	return nil
}

// AIProviderUsers returns the jobs that send requests to provider id. The
// text binding is idle while the ChatGPT plan serves text.
func AIProviderUsers(values map[string]string, id string) []AIJob {
	var users []AIJob
	for _, binding := range aiProviderBindings {
		if id == "" || strings.TrimSpace(values[binding.key]) != id {
			continue
		}
		if binding.job == AIJobText && strings.TrimSpace(values["ai.auth_mode"]) == aiAuthModeChatGPT {
			continue
		}
		users = append(users, binding.job)
	}
	return users
}

// Legacy endpoint settings replaced by the registry. Their rows are left in
// place so an older build still finds them; this build no longer reads them.
const (
	legacyAITextURLSetting   = "ai.base_url"
	legacyAITextKeySetting   = "ai.api_key"
	legacyAISpeechURLSetting = "ai.asr_base_url"
	legacyAISpeechKeySetting = "ai.asr_api_key"
	legacySubtitleURLSetting = "subtitle_ai.base_url"
	legacySubtitleKeySetting = "subtitle_ai.api_key"
)

// PlanAIProviderConversion moves endpoints stored by older builds into the
// registry, once. It returns the writes for one atomic settings update, or nil
// when the stored registry has already seen every conversion. A binding the
// administrator already set is never replaced.
func PlanAIProviderConversion(stored map[string]string) (map[string]string, error) {
	registry, err := ParseAIProviderRegistry(stored[AIProvidersSettingKey])
	if err != nil {
		return nil, err
	}
	if registry.Version >= aiProvidersVersion {
		return nil, nil
	}
	writes := map[string]string{}
	if registry.Version < 1 {
		convertLegacyTextAndSpeech(&registry, stored, writes)
	}
	registry.Version = aiProvidersVersion
	writes[AIProvidersSettingKey] = registry.Encode()
	return writes, nil
}

func convertLegacyTextAndSpeech(registry *AIProviderRegistry, stored map[string]string, writes map[string]string) {
	bind := func(key, id string) {
		if id != "" && strings.TrimSpace(stored[key]) == "" {
			writes[key] = id
		}
	}
	textBaseURL := firstNonEmpty(stored[legacyAITextURLSetting], stored[legacySubtitleURLSetting])
	textKey := firstNonEmpty(stored[legacyAITextKeySetting], stored[legacySubtitleKeySetting])
	textID := ""
	if textBaseURL != "" || textKey != "" {
		if textBaseURL == "" {
			textBaseURL = openAIBaseURL
		}
		textID = adoptAIProvider(registry, textBaseURL, textKey, AICapabilityChat)
		bind(AITextProviderSettingKey, textID)
	}

	// An empty speech base URL sent audio to the text endpoint, and an empty
	// speech key reused the text key.
	speechBaseURL := strings.TrimSpace(stored[legacyAISpeechURLSetting])
	speechKey := firstNonEmpty(stored[legacyAISpeechKeySetting], textKey)
	switch {
	case speechBaseURL != "":
		bind(AISpeechProviderSettingKey, adoptAIProvider(registry, speechBaseURL, speechKey, AICapabilitySpeech))
	case textID != "":
		provider, _ := registry.Find(textID)
		// A custom text endpoint served speech only if the administrator
		// turned transcription on against it.
		if provider.Supports(AICapabilitySpeech) ||
			(provider.Type == AIProviderCustom && AdminSettingEnabled("subtitle_ai.transcribe_enabled", stored["subtitle_ai.transcribe_enabled"])) {
			addAIProviderCapability(registry, textID, AICapabilitySpeech)
			bind(AISpeechProviderSettingKey, textID)
		}
	}
}

// adoptAIProvider returns the id of a provider that serves capability at exactly
// baseURL with apiKey, connecting one when none does. It returns "" for a
// hosted endpoint that has no key, which could never have worked.
func adoptAIProvider(r *AIProviderRegistry, baseURL, apiKey string, capability AICapability) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	apiKey = strings.TrimSpace(apiKey)
	providerType, providerBaseURL := detectAIProviderType(baseURL, apiKey, capability)
	if providerType == "" {
		return ""
	}
	for _, provider := range r.Providers {
		if provider.Type == providerType && provider.BaseURL == providerBaseURL && provider.APIKey == apiKey {
			addAIProviderCapability(r, provider.ID, capability)
			return provider.ID
		}
	}
	spec, _ := LookupAIProviderType(providerType)
	name := spec.Label
	capabilities := slices.Clone(spec.Capabilities)
	if providerType == AIProviderCustom {
		if parsed, err := url.Parse(providerBaseURL); err == nil && parsed.Host != "" {
			name = parsed.Host
		}
		capabilities = []AICapability{capability}
	}
	provider := AIProvider{
		ID:           NewAIProviderID(providerType),
		Type:         providerType,
		Name:         uniqueAIProviderName(r, name),
		BaseURL:      providerBaseURL,
		APIKey:       apiKey,
		Capabilities: capabilities,
	}
	r.Providers = append(r.Providers, provider)
	return provider.ID
}

func addAIProviderCapability(r *AIProviderRegistry, id string, capability AICapability) {
	for i := range r.Providers {
		provider := &r.Providers[i]
		if provider.ID != id || provider.Supports(capability) {
			continue
		}
		if provider.Type == AIProviderCustom {
			provider.Capabilities = append(provider.Capabilities, capability)
			slices.SortFunc(provider.Capabilities, func(a, b AICapability) int {
				return slices.Index(AICapabilities, a) - slices.Index(AICapabilities, b)
			})
		}
	}
}

func uniqueAIProviderName(r *AIProviderRegistry, name string) string {
	taken := func(candidate string) bool {
		return slices.ContainsFunc(r.Providers, func(p AIProvider) bool { return strings.EqualFold(p.Name, candidate) })
	}
	if !taken(name) {
		return name
	}
	for n := 2; ; n++ {
		if candidate := fmt.Sprintf("%s (%d)", name, n); !taken(candidate) {
			return candidate
		}
	}
}

// sameRequestEndpoint reports whether two base URLs send capability's requests
// to the same address. The shared AI client inserts /v1 into a chat or speech
// base URL that has none (llm.endpointURL), so https://openrouter.ai/api and
// https://openrouter.ai/api/v1 are the same endpoint there. Embedding base URLs
// are compared exactly: the embedding lock records them as written.
func sameRequestEndpoint(a, b string, capability AICapability) bool {
	if capability == AICapabilityEmbeddings {
		return a == b
	}
	withVersion := func(base string) string {
		if strings.Contains(base, "/v1") {
			return base
		}
		return base + "/v1"
	}
	return withVersion(a) == withVersion(b)
}

// detectAIProviderType picks the known type that serves capability at
// exactly baseURL, so the converted provider resolves to the same endpoint the
// legacy setting held. Anything else becomes a custom provider at baseURL.
func detectAIProviderType(baseURL, apiKey string, capability AICapability) (AIProviderType, string) {
	if _, err := normalizeAdminURL("AI provider base URL", baseURL); err != nil || baseURL == "" {
		return "", ""
	}
	for _, spec := range AIProviderTypes {
		if spec.DefaultBaseURL == "" || spec.Type == AIProviderOllama || spec.Type == AIProviderWhisper || !spec.supports(capability) {
			continue
		}
		if !sameRequestEndpoint(spec.endpoint(spec.DefaultBaseURL, capability), baseURL, capability) {
			continue
		}
		if spec.KeyRequired && apiKey == "" {
			return "", ""
		}
		return spec.Type, spec.DefaultBaseURL
	}
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Port() == "11434" && parsed.Path == "" && capability != AICapabilitySpeech {
		return AIProviderOllama, baseURL
	}
	return AIProviderCustom, baseURL
}
