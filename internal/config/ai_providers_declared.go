package config

import (
	"fmt"
	"slices"
	"strings"
)

// NewDeclaredAIProvider builds a provider the deployment declares. A name or
// address left empty comes from the type, as when an admin connects one, and
// the result is normalized like an admin's. Only a custom provider takes
// capabilities; a known type's are fixed, so declaring them is an error
// rather than silently ignored.
func NewDeclaredAIProvider(id string, t AIProviderType, name, baseURL, apiKey string, capabilities []AICapability) (AIProvider, error) {
	spec, ok := LookupAIProviderType(t)
	if !ok {
		return AIProvider{}, fmt.Errorf("unknown AI provider type %q", t)
	}
	if spec.Capabilities != nil && len(capabilities) > 0 {
		return AIProvider{}, fmt.Errorf("a %s provider's capabilities are fixed; capabilities apply only to custom providers", spec.Label)
	}
	provider := AIProvider{
		ID: id, Type: t, Name: spec.Label, BaseURL: spec.DefaultBaseURL, APIKey: apiKey,
		Capabilities: slices.Clone(capabilities),
	}
	if name != "" {
		provider.Name = name
	}
	if baseURL != "" {
		provider.BaseURL = baseURL
	}
	return NormalizeAIProvider(provider)
}

// ApplyDeclaredAIProviders returns the stored registry raw with the declared
// providers in it, each owned by managedBy. A declared provider replaces the
// stored one with its id whole, key included, or is added after the others.
// A provider an earlier declaration owned that this one leaves out is
// released: it stays connected, and admins can edit or delete it again. The
// registry's version is kept, so a legacy conversion that has not run yet
// still runs.
func ApplyDeclaredAIProviders(raw string, declared []AIProvider, managedBy string) (string, error) {
	registry, err := ParseAIProviderRegistry(raw)
	if err != nil {
		return "", err
	}
	byID := make(map[string]bool, len(declared))
	byName := make(map[string]string, len(declared))
	for _, provider := range declared {
		byID[provider.ID] = true
		name := strings.ToLower(provider.Name)
		if other, taken := byName[name]; taken {
			return "", fmt.Errorf("%s and %s are both named %q; give each declared provider a name of its own", other, provider.ID, provider.Name)
		}
		byName[name] = provider.ID
	}
	for i, stored := range registry.Providers {
		if !byID[stored.ID] {
			registry.Providers[i].ManagedBy = ""
		}
	}
	for _, provider := range declared {
		// A declared name an undeclared provider already has would otherwise
		// fail as a duplicate name with no hint of the fix.
		for _, stored := range registry.Providers {
			if !byID[stored.ID] && strings.EqualFold(provider.Name, stored.Name) {
				return "", nameTakenError(provider, stored)
			}
		}
		provider.ManagedBy = managedBy
		if i := slices.IndexFunc(registry.Providers, func(p AIProvider) bool { return p.ID == provider.ID }); i >= 0 {
			// As for an admin's edit: the models bound to a provider are its
			// type's, so a new type takes a new id.
			if stored := registry.Providers[i]; stored.Type != provider.Type {
				return "", fmt.Errorf("%s: a provider's type cannot change (it is %s, declared as %s); declare the %s provider under a new id with a name of its own",
					provider.ID, stored.Type, provider.Type, provider.Type)
			}
			registry.Providers[i] = provider
		} else {
			registry.Providers = append(registry.Providers, provider)
		}
	}
	if err := registry.Validate(); err != nil {
		return "", err
	}
	return registry.Encode(), nil
}

func nameTakenError(declared, stored AIProvider) error {
	// Taking the other provider over is a way out only when it keeps its type.
	if stored.Type != declared.Type {
		return fmt.Errorf("%s: another AI provider, %s, is already named %q; give the declared provider a name of its own",
			declared.ID, stored.ID, stored.Name)
	}
	return fmt.Errorf("%s: another AI provider, %s, is already named %q; give the declared provider a name of its own, or declare %s to take that provider over",
		declared.ID, stored.ID, stored.Name, stored.ID)
}
