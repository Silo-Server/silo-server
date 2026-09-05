package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
)

// The settings domain: the settings contract (the manifest clients vendor
// and its capability document), the server-wide overlay defaults, device
// overrides of the subtitle appearance, and per-installation plugin settings.

// deviceIDHeader is the device header the v1 settings routes read
// (handlers.DeviceMetadataFromHeaders reads it and the name/platform pair);
// a device is not an identity the gates resolve, so the three stay declared
// parameters rather than context values.
const deviceIDHeader = "X-Silo-Device-Id"

// fieldDeviceID is the seam's name for a rejected device id; it is rendered
// at the header the framework declared, not as a body member.
const fieldDeviceID = "device_id"

// cachePrivateNoCache is the policy of a per-build document served behind
// authentication: a proxy may not keep it, a client revalidates by ETag.
const cachePrivateNoCache = "private, no-cache"

// SettingsContractOutput is the getSettingsContract response: the public
// settings manifest, byte for byte what contracts/settings/v1 embeds after
// canonicalization, so a client's vendored copy compares equal. The body is
// written by hand so nothing re-encodes it.
type SettingsContractOutput struct {
	ContentType string `header:"Content-Type"`
	// The manifest is fixed per build but served behind authentication, so
	// it is private; the ETag is the canonical public projection's tag, the
	// same value v1 sends, and identifies the revision without a transfer.
	CacheControl string `header:"Cache-Control"`
	ETag         string `header:"ETag"`
	Body         func(huma.Context)
}

// SettingsContractCapabilities reports what this server's settings API
// supports, for feature detection rather than version sniffing.
type SettingsContractCapabilities struct {
	APIVersion               int      `json:"api_version" doc:"Settings protocol version; changes only for a change no revision rule can express" example:"1"`
	Revision                 int      `json:"revision" doc:"Manifest revision clients filter definitions against" example:"12"`
	ContractETag             string   `json:"contract_etag" doc:"Entity tag of the public manifest getSettingsContract serves" example:"\"a1b2c3\""`
	DefinitionCount          int      `json:"definition_count" doc:"Number of setting definitions in the manifest" example:"40"`
	Scopes                   []string `json:"scopes" doc:"Setting scopes this server resolves" example:"[\"account\",\"profile\"]"`
	ClientFamilies           []string `json:"client_families" doc:"Client families a profile_client scope may name" example:"[\"tv\",\"mobile\"]"`
	SupportsBatchedEffective bool     `json:"supports_batched_effective" doc:"Whether the effective resolver accepts a batch of keys" example:"true"`
	SupportsIdempotentWrites bool     `json:"supports_idempotent_writes" doc:"Whether repeating a value write converges on the same state" example:"true"`
	SupportsAtomicShortcuts  bool     `json:"supports_atomic_shortcuts" doc:"Whether navigation shortcut list mutations are atomic" example:"true"`
}

// SettingsContractCapabilitiesOutput is the getSettingsContractCapabilities
// response.
type SettingsContractCapabilitiesOutput struct {
	Body SettingsContractCapabilities
}

// OverlayConfig is the server-wide card overlay configuration. Enabled and
// QuickActionsEnabled are defaults for profiles that have not chosen; an
// explicit profile setting overrides them either way.
type OverlayConfig struct {
	Enabled             bool   `json:"enabled" doc:"Whether card overlays are enabled server-wide" example:"true"`
	Defaults            string `json:"defaults,omitempty" doc:"Administrator-chosen overlay defaults document; absent when none is set" example:"{\"badges\":true}"`
	QuickActionsEnabled bool   `json:"quick_actions_enabled" doc:"Default for profiles that have not chosen whether cards show quick actions" example:"false"`
	QuickActionsDefault string `json:"quick_actions_default" doc:"Default quick-action mode; one of the ui.card_quick_actions values in the settings contract" example:"both"`
}

// OverlayConfigOutput is the getOverlayConfig response.
type OverlayConfigOutput struct {
	// Server-wide but served behind authentication: private, revalidated.
	CacheControl string `header:"Cache-Control"`
	Body         OverlayConfig
}

// EffectiveSubtitleAppearance is the subtitle_appearance setting resolved
// for the acting profile on the declared device: the device override when
// one exists, otherwise the profile-wide value.
type EffectiveSubtitleAppearance struct {
	Key               string   `json:"key" doc:"Always subtitle_appearance" example:"subtitle_appearance"`
	ProfileID         ID       `json:"profile_id" doc:"The acting profile" example:"1"`
	GlobalValue       string   `json:"global_value" doc:"The profile-wide value as a JSON document; empty when unset" example:"{\"fontSize\":\"large\"}"`
	DeviceValue       string   `json:"device_value,omitempty" doc:"The device override as a JSON document; absent when the device has none" example:"{\"fontSize\":\"xxlarge\"}"`
	EffectiveValue    string   `json:"effective_value" doc:"The value that applies on this device; empty when nothing is set" example:"{\"fontSize\":\"xxlarge\"}"`
	HasDeviceOverride bool     `json:"has_device_override" doc:"Whether device_value is what applies" example:"true"`
	DeviceID          string   `json:"device_id,omitempty" doc:"The device the override belongs to; absent when there is none" example:"iphone-1"`
	DeviceName        string   `json:"device_name,omitempty" doc:"The name the device registered with; absent when unknown" example:"Living room"`
	DevicePlatform    string   `json:"device_platform,omitempty" doc:"The platform the device registered with; absent when unknown" example:"iOS"`
	UpdatedAt         *Instant `json:"updated_at,omitempty" doc:"When the device override was last written; absent when there is none" example:"2026-01-02T03:04:05.000Z"`
}

// EffectiveSubtitleAppearanceOutput is the response of
// getEffectiveSubtitleAppearance and updateSubtitleAppearanceDeviceOverride.
type EffectiveSubtitleAppearanceOutput struct {
	Body EffectiveSubtitleAppearance
}

// DeviceHeaders are the declared device parameters of a device override
// mutation; the id is required because an override has no meaning without a
// device.
type DeviceHeaders struct {
	DeviceID       string `header:"X-Silo-Device-Id" required:"true" maxLength:"128" doc:"The client's stable device identifier" example:"iphone-1"`
	DeviceName     string `header:"X-Silo-Device-Name" maxLength:"120" doc:"Optional display name recorded on the device registry" example:"Living room"`
	DevicePlatform string `header:"X-Silo-Device-Platform" maxLength:"40" doc:"Optional platform recorded on the device registry" example:"iOS"`
}

func (d DeviceHeaders) metadata() handlers.DeviceMetadata {
	return handlers.NewDeviceMetadata(d.DeviceID, d.DeviceName, d.DevicePlatform)
}

// EffectiveSubtitleAppearanceInput is the getEffectiveSubtitleAppearance
// request. The device headers are optional here: without a device only the
// profile-wide value can apply.
type EffectiveSubtitleAppearanceInput struct {
	DeviceID       string `header:"X-Silo-Device-Id" maxLength:"128" doc:"The client's stable device identifier; absent resolves the profile-wide value" example:"iphone-1"`
	DeviceName     string `header:"X-Silo-Device-Name" maxLength:"120" doc:"Optional display name recorded on the device registry" example:"Living room"`
	DevicePlatform string `header:"X-Silo-Device-Platform" maxLength:"40" doc:"Optional platform recorded on the device registry" example:"iOS"`
}

// SubtitleAppearanceDeviceOverride is the value a device stores.
type SubtitleAppearanceDeviceOverride struct {
	Value string `json:"value" required:"true" doc:"The subtitle appearance as a JSON document; the server validates only that it is JSON" example:"{\"fontSize\":\"xxlarge\"}"`
}

// SubtitleAppearanceDeviceOverrideInput is the
// updateSubtitleAppearanceDeviceOverride request.
type SubtitleAppearanceDeviceOverrideInput struct {
	DeviceHeaders
	Body SubtitleAppearanceDeviceOverride
}

// DeleteSubtitleAppearanceDeviceOverrideInput is the
// deleteSubtitleAppearanceDeviceOverride request.
type DeleteSubtitleAppearanceDeviceOverrideInput struct {
	DeviceHeaders
}

// PluginSettingSchema is one user-facing setting a plugin declares.
type PluginSettingSchema struct {
	Key         string `json:"key" doc:"The value's key in the installation's settings map" example:"region"`
	Title       string `json:"title" doc:"Display title; empty when the plugin gives none" example:"Region"`
	Description string `json:"description" doc:"Help text; empty when the plugin gives none" example:"Two-letter country code"`
	JSONSchema  string `json:"json_schema" doc:"JSON Schema for the value, as the plugin wrote it; empty when unconstrained" example:"{\"type\":\"string\"}"`
	Required    bool   `json:"required" doc:"Whether the plugin needs a value" example:"false"`
}

// PluginRoute is one HTTP route a plugin exposes; user-navigable routes
// appear in the Apps navigation.
type PluginRoute struct {
	ID              string `json:"id" doc:"Route identifier within the plugin" example:"dashboard"`
	Method          string `json:"method" doc:"HTTP method" example:"GET"`
	Path            string `json:"path" doc:"Path under the plugin's proxy prefix" example:"/dashboard"`
	Access          string `json:"access" doc:"Who may call it, as the plugin declared" example:"user"`
	Navigable       bool   `json:"navigable" doc:"Whether clients list it in navigation" example:"true"`
	NavigationLabel string `json:"navigation_label" doc:"Label for navigation; empty when not navigable" example:"Dashboard"`
	NavigationKind  string `json:"navigation_kind" doc:"Navigation surface: user or admin" example:"user"`
	StaticAsset     bool   `json:"static_asset" doc:"Whether the route serves a packaged asset" example:"false"`
}

// PluginAsset is one packaged static asset.
type PluginAsset struct {
	Path        string `json:"path" doc:"Asset path under the plugin's proxy prefix" example:"/app.js"`
	ContentType string `json:"content_type" doc:"Media type" example:"text/javascript"`
	Integrity   string `json:"integrity" doc:"Subresource integrity digest" example:"sha256-..."`
}

// PluginSettingsInstallation is an enabled installation's user settings
// surface.
type PluginSettingsInstallation struct {
	ID               ID                    `json:"id" doc:"Installation identifier" example:"3"`
	PluginID         string                `json:"plugin_id" doc:"The plugin's stable identifier" example:"org.example.subtitles"`
	Version          string                `json:"version" doc:"Installed version" example:"1.2.0"`
	UserConfigSchema []PluginSettingSchema `json:"user_config_schema" doc:"Settings the plugin asks each account for; empty when it only exposes navigable routes"`
	Routes           []PluginRoute         `json:"routes" doc:"Routes the plugin exposes; empty, never null"`
	Assets           []PluginAsset         `json:"assets" doc:"Packaged assets; empty, never null"`
	Category         string                `json:"category,omitempty" doc:"Slash-delimited grouping for the Apps navigation; absent when the manifest declares none" example:"Tools/Utilities"`
}

// PluginSettingsInstallationCollection is the listPluginSettings response
// body: bounded by the installed plugins, so unpaginated.
type PluginSettingsInstallationCollection struct {
	Collection[PluginSettingsInstallation]
}

// PluginSettingsInstallationCollectionOutput is the listPluginSettings
// response.
type PluginSettingsInstallationCollectionOutput struct {
	Body PluginSettingsInstallationCollection
}

// PluginSettingValues is an account's values for one installation. The
// plugin defines the keys, so this is a named extension bag of strings.
type PluginSettingValues map[string]string

// Schema declares the bag: string values under keys this contract does not
// fix. The extension-bag marker is what lets the spec lint accept it.
func (PluginSettingValues) Schema(_ huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type:                 huma.TypeObject,
		Description:          "Setting values keyed by the plugin's user_config_schema keys; every value is a string.",
		AdditionalProperties: &huma.Schema{Type: huma.TypeString},
		Extensions:           map[string]any{extExtensionBag: "plugin-setting-values"},
	}
}

// PluginSettings is one installation's user settings surface plus the
// account's stored values for it.
type PluginSettings struct {
	Installation PluginSettingsInstallation `json:"installation" doc:"The installation and what it asks for"`
	Values       PluginSettingValues        `json:"values" doc:"The account's stored values; empty, never null"`
}

// PluginSettingsInput is the getPluginSettings request.
type PluginSettingsInput struct {
	InstallationID ID `path:"installation_id" doc:"The plugin installation" example:"3"`
}

// PluginSettingsOutput is the getPluginSettings response.
type PluginSettingsOutput struct {
	Body PluginSettings
}

func registerSettings(reg *Registry) {
	contract := Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/settings/contract", "getSettingsContract", "settings",
			"Get the public settings manifest this server was built with."),
		// v1 serves it to any authenticated caller; a present profile header
		// is judged, not required.
		Class:           ClassProfileScoped,
		ProfileOptional: true,
	}
	contract.Responses = map[string]*huma.Response{
		"200": {
			Description: "The public settings manifest, exactly the canonical bytes of contracts/settings/v1 with maintainer-only fields removed; the same document v1 /settings/manifest serves.",
			Content: map[string]*huma.MediaType{
				mediaTypeJSON: {Schema: &huma.Schema{
					Type:                 huma.TypeObject,
					Description:          "A settings manifest. Its members are fixed by the settings contract (contracts/settings/v1), not by this document.",
					AdditionalProperties: true,
					Extensions:           map[string]any{extExtensionBag: "settings-manifest"},
				}},
			},
		},
	}
	Register(reg, contract, getSettingsContract)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/settings/contract/capabilities", "getSettingsContractCapabilities", "settings",
			"Get what this server's settings API supports."),
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		ServiceBacked:   true,
	}, reg.getSettingsContractCapabilities)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/settings/overlay-config", "getOverlayConfig", "settings",
			"Get the server-wide card overlay defaults."),
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		ServiceBacked:   true,
	}, reg.getOverlayConfig)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/settings/subtitle-appearance/effective", "getEffectiveSubtitleAppearance", "settings",
			"Get the subtitle appearance that applies to the acting profile on this device."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, reg.getEffectiveSubtitleAppearance)

	Register(reg, Operation{
		Operation: humaOp(http.MethodPut, Prefix+"/settings/device/subtitle-appearance", "updateSubtitleAppearanceDeviceOverride", "settings",
			"Replace this device's subtitle appearance override for the acting profile."),
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.updateSubtitleAppearanceDeviceOverride)

	Register(reg, Operation{
		Operation: humaOp(http.MethodDelete, Prefix+"/settings/device/subtitle-appearance", "deleteSubtitleAppearanceDeviceOverride", "settings",
			"Remove this device's subtitle appearance override for the acting profile."),
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.deleteSubtitleAppearanceDeviceOverride)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/settings/plugins", "listPluginSettings", "settings",
			"List the enabled plugins that expose user settings or navigable routes."),
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		ServiceBacked:   true,
	}, reg.listPluginSettings)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/settings/plugins/{installation_id}", "getPluginSettings", "settings",
			"Get a plugin installation's user settings and the account's values for them."),
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		ServiceBacked:   true,
	}, reg.getPluginSettings)

	Register(reg, Operation{
		Operation: humaOp(http.MethodPut, Prefix+"/settings/plugins/{installation_id}", "updatePluginSettings", "settings",
			"Replace the account's values for a plugin installation's user settings."),
		Class:           ClassProfileScoped,
		ProfileOptional: true,
		DemoRestricted:  true,
		ServiceBacked:   true,
	}, reg.updatePluginSettings)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/settings/values", "listSettingValues", "settings",
			"Get the explicit values of several settings at one scope."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, reg.listSettingValues)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/settings/values/effective", "listEffectiveSettings", "settings",
			"Resolve the effective values of settings for the acting profile."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, reg.listEffectiveSettings)

	Register(reg, Operation{
		Operation: humaOp(http.MethodPost, Prefix+"/settings/values/effective", "resolveEffectiveSettings", "settings",
			"Resolve the effective values of settings under several content contexts at once."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, reg.resolveEffectiveSettings)

	shortcut := humaOp(http.MethodPut, Prefix+"/settings/values/nav.shortcuts/item", "updateNavigationShortcut", "settings",
		"Add or remove one navigation shortcut of the acting profile.")
	// The seam applies the mutation with a bounded compare-and-set loop and
	// answers 409 setting_update_conflict when concurrent shortcut updates
	// exhaust it; serviceProblem carries that through as conflict. The
	// status is this operation's own, not one the class implies, and the
	// response says the request is safe to retry.
	shortcut.Errors = []int{http.StatusConflict}
	shortcut.Responses = map[string]*huma.Response{
		strconv.Itoa(http.StatusConflict): {Description: navigationShortcutConflictDescription},
	}
	Register(reg, Operation{
		Operation:      shortcut,
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.updateNavigationShortcut)

	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/settings/values/{key}", "getSettingValue", "settings",
			"Get the explicit value of a setting at one scope."),
		Class:         ClassProfileScoped,
		ServiceBacked: true,
	}, reg.getSettingValue)

	Register(reg, Operation{
		Operation: humaOp(http.MethodPut, Prefix+"/settings/values/{key}", "updateSettingValue", "settings",
			"Replace the explicit value of a setting at one scope."),
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.updateSettingValue)

	Register(reg, Operation{
		Operation: humaOp(http.MethodDelete, Prefix+"/settings/values/{key}", "deleteSettingValue", "settings",
			"Remove the explicit value of a setting at one scope so it inherits again."),
		Class:          ClassProfileScoped,
		DemoRestricted: true,
		ServiceBacked:  true,
	}, reg.deleteSettingValue)
}

// getSettingsContract serves the canonical public manifest bytes with the
// same ETag v1 GET /settings/contract and /settings/manifest send. A
// conditional GET (If-None-Match, 304) lands with the foundation caching
// rules, not here.
func getSettingsContract(_ context.Context, _ *struct{}) (*SettingsContractOutput, error) {
	body, err := settingscontract.PublicBytes()
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	etag, err := settingscontract.PublicETag()
	if err != nil {
		return nil, NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	return &SettingsContractOutput{
		ContentType:  mediaTypeJSON,
		CacheControl: cachePrivateNoCache,
		ETag:         etag,
		Body: func(ctx huma.Context) {
			ctx.SetStatus(http.StatusOK)
			_, _ = ctx.BodyWriter().Write(body)
		},
	}, nil
}

func (reg *Registry) getSettingsContractCapabilities(ctx context.Context, _ *struct{}) (*SettingsContractCapabilitiesOutput, error) {
	if reg.deps.SettingsContract == nil {
		return nil, unavailable("settings contract")
	}
	view, err := reg.deps.SettingsContract.Capabilities(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &SettingsContractCapabilitiesOutput{Body: SettingsContractCapabilities{
		APIVersion:               view.APIVersion,
		Revision:                 view.Revision,
		ContractETag:             view.ContractETag,
		DefinitionCount:          view.DefinitionCount,
		Scopes:                   NonNil(view.Scopes),
		ClientFamilies:           NonNil(view.ClientFamilies),
		SupportsBatchedEffective: view.SupportsBatchedEffective,
		SupportsIdempotentWrites: view.SupportsIdempotentWrites,
		SupportsAtomicShortcuts:  view.SupportsAtomicShortcuts,
	}}, nil
}

func (reg *Registry) getOverlayConfig(ctx context.Context, _ *struct{}) (*OverlayConfigOutput, error) {
	if reg.deps.Settings == nil {
		return nil, unavailable("settings")
	}
	view := reg.deps.Settings.OverlayConfig(ctx)
	return &OverlayConfigOutput{
		CacheControl: cachePrivateNoCache,
		Body: OverlayConfig{
			Enabled:             view.Enabled,
			Defaults:            view.Defaults,
			QuickActionsEnabled: view.QuickActionsEnabled,
			QuickActionsDefault: view.QuickActionsDefault,
		},
	}, nil
}

func (reg *Registry) getEffectiveSubtitleAppearance(ctx context.Context, in *EffectiveSubtitleAppearanceInput) (*EffectiveSubtitleAppearanceOutput, error) {
	if reg.deps.Settings == nil {
		return nil, unavailable("settings")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	device := handlers.NewDeviceMetadata(in.DeviceID, in.DeviceName, in.DevicePlatform)
	view, err := reg.deps.Settings.EffectiveSubtitleAppearance(ctx, claims.UserID, profileFrom(ctx), device)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &EffectiveSubtitleAppearanceOutput{Body: effectiveSubtitleAppearanceFromView(view)}, nil
}

// updateSubtitleAppearanceDeviceOverride stores the override through the
// same path as v1 PUT /settings/device/subtitle_appearance and answers with
// the resolved appearance, the canonical representation after the write.
func (reg *Registry) updateSubtitleAppearanceDeviceOverride(ctx context.Context, in *SubtitleAppearanceDeviceOverrideInput) (*EffectiveSubtitleAppearanceOutput, error) {
	if reg.deps.Settings == nil {
		return nil, unavailable("settings")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	cmd := handlers.DeviceSettingCommand{
		UserID:    claims.UserID,
		ProfileID: profileFrom(ctx),
		Device:    in.metadata(),
		Key:       handlers.SubtitleAppearanceSettingKey,
	}
	if err := reg.deps.Settings.SetDeviceSetting(ctx, cmd, in.Body.Value); err != nil {
		return nil, deviceSettingProblem(err)
	}
	view, err := reg.deps.Settings.EffectiveSubtitleAppearance(ctx, claims.UserID, cmd.ProfileID, cmd.Device)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &EffectiveSubtitleAppearanceOutput{Body: effectiveSubtitleAppearanceFromView(view)}, nil
}

func (reg *Registry) deleteSubtitleAppearanceDeviceOverride(ctx context.Context, in *DeleteSubtitleAppearanceDeviceOverrideInput) (*struct{}, error) {
	if reg.deps.Settings == nil {
		return nil, unavailable("settings")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	cmd := handlers.DeviceSettingCommand{
		UserID:    claims.UserID,
		ProfileID: profileFrom(ctx),
		Device:    in.metadata(),
		Key:       handlers.SubtitleAppearanceSettingKey,
	}
	if err := reg.deps.Settings.DeleteDeviceSetting(ctx, cmd); err != nil {
		return nil, deviceSettingProblem(err)
	}
	return nil, nil
}

// deviceSettingProblem renders the seam's decision: a rejected value is a
// validation failure at body.value; a rejected device names the header the
// framework already required, so it can only be reached by a blank one.
func deviceSettingProblem(err error) *Problem {
	apiErr, ok := err.(*handlers.APIError) //nolint:errorlint // the seam returns the value directly
	if !ok || apiErr.Field == "" {
		return serviceProblem(err)
	}
	location := locationBody + "." + apiErr.Field
	if apiErr.Field == fieldDeviceID {
		location = "header." + deviceIDHeader
	}
	return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
		WithErrors(ProblemError{Location: location, Code: codeInvalid, Detail: apiErr.Message})
}

func effectiveSubtitleAppearanceFromView(v handlers.EffectiveSubtitleAppearanceView) EffectiveSubtitleAppearance {
	return EffectiveSubtitleAppearance{
		Key:               v.Key,
		ProfileID:         ID(v.ProfileID),
		GlobalValue:       v.GlobalValue,
		DeviceValue:       v.DeviceValue,
		EffectiveValue:    v.EffectiveValue,
		HasDeviceOverride: v.HasDeviceOverride,
		DeviceID:          v.DeviceID,
		DeviceName:        v.DeviceName,
		DevicePlatform:    v.DevicePlatform,
		UpdatedAt:         storedInstant(v.UpdatedAt),
	}
}

// storedInstant converts a store's textual timestamp (RFC 3339, or the
// Postgres text form of a timestamptz) into the wire instant; an empty or
// unparsable value is omitted rather than guessed.
func storedInstant(raw string) *Instant {
	if raw == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07", "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999"} {
		if t, err := time.Parse(layout, raw); err == nil {
			i := NewInstant(t)
			return &i
		}
	}
	return nil
}

func (reg *Registry) listPluginSettings(ctx context.Context, _ *struct{}) (*PluginSettingsInstallationCollectionOutput, error) {
	if reg.deps.PluginSettings == nil {
		return nil, unavailable("plugin settings")
	}
	views, err := reg.deps.PluginSettings.ListUserPluginSettings(ctx)
	if err != nil {
		return nil, serviceProblem(err)
	}
	items := make([]PluginSettingsInstallation, 0, len(views))
	for _, v := range views {
		items = append(items, pluginSettingsInstallationFromView(v))
	}
	return &PluginSettingsInstallationCollectionOutput{Body: PluginSettingsInstallationCollection{Collection: NewCollection(items)}}, nil
}

func (reg *Registry) getPluginSettings(ctx context.Context, in *PluginSettingsInput) (*PluginSettingsOutput, error) {
	if reg.deps.PluginSettings == nil {
		return nil, unavailable("plugin settings")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	// Identifiers are opaque on the wire; one that names no installation is
	// the same 404 as an unknown one, not a parse error.
	id, err := intOfID(in.InstallationID)
	if err != nil || id <= 0 {
		return nil, NewProblem(TypeNotFound, "Plugin installation not found")
	}
	view, err := reg.deps.PluginSettings.GetUserPluginSettings(ctx, claims.UserID, id)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &PluginSettingsOutput{Body: PluginSettings{
		Installation: pluginSettingsInstallationFromView(view.Installation),
		Values:       PluginSettingValues(NonNilMap(view.Values)),
	}}, nil
}

func pluginSettingsInstallationFromView(v handlers.PluginUserSettingsView) PluginSettingsInstallation {
	schemas := make([]PluginSettingSchema, 0, len(v.UserConfigSchema))
	for _, s := range v.UserConfigSchema {
		schemas = append(schemas, PluginSettingSchema{Key: s.Key, Title: s.Title, Description: s.Description, JSONSchema: s.JSONSchema, Required: s.Required})
	}
	routes := make([]PluginRoute, 0, len(v.Routes))
	for _, r := range v.Routes {
		routes = append(routes, PluginRoute(r))
	}
	assets := make([]PluginAsset, 0, len(v.Assets))
	for _, a := range v.Assets {
		assets = append(assets, PluginAsset(a))
	}
	return PluginSettingsInstallation{
		ID:               IDFromInt(int64(v.ID)),
		PluginID:         v.PluginID,
		Version:          v.Version,
		UserConfigSchema: schemas,
		Routes:           routes,
		Assets:           assets,
		Category:         v.Category,
	}
}

// --- Setting values: the explicit and effective values of the contract ------

// clientFamilyHeader is the header v1 reads a profile_client scope's family
// from; the same header names it here.
const clientFamilyHeader = "X-Silo-Client-Family"

// JSONValue is a setting value on the wire: any JSON document. The settings
// contract's value_schema for the key fixes its shape, so this document does
// not restate it.
type JSONValue []byte

func (v JSONValue) MarshalJSON() ([]byte, error) {
	if len(v) == 0 {
		return []byte("null"), nil
	}
	return v, nil
}

func (v *JSONValue) UnmarshalJSON(b []byte) error {
	*v = append((*v)[:0], b...)
	return nil
}

// Schema declares the value untyped: the key's value_schema in the settings
// contract (getSettingsContract) is the authority on its shape.
func (JSONValue) Schema(_ huma.Registry) *huma.Schema {
	return &huma.Schema{Description: "A JSON value whose shape is the setting's value_schema in the settings contract."}
}

// NavigationShortcutItem is one nav.shortcuts entry; its members are fixed by
// the settings contract's navigation-shortcuts.json schema, not here. The
// bytes are carried as sent so the seam normalizes what the client wrote.
type NavigationShortcutItem []byte

func (v NavigationShortcutItem) MarshalJSON() ([]byte, error) {
	if len(v) == 0 {
		return []byte("null"), nil
	}
	return v, nil
}

func (v *NavigationShortcutItem) UnmarshalJSON(b []byte) error {
	*v = append((*v)[:0], b...)
	return nil
}

// Schema declares the bag: the contract's object schema owns the members.
func (NavigationShortcutItem) Schema(_ huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type:                 huma.TypeObject,
		Description:          "One navigation shortcut, shaped by the settings contract's navigation-shortcuts.json item schema (type, label, and the library_id/section_id/collection_id the type needs).",
		AdditionalProperties: true,
		Extensions:           map[string]any{extExtensionBag: "navigation-shortcut-item"},
	}
}

// SettingScopeQuery is everything a request says about which stored value
// it addresses beyond the key: the scope and the identity members that
// scope needs. The acting profile is the X-Profile-Id the gate verified.
type SettingScopeQuery struct {
	Scope          string `query:"scope" required:"true" enum:"account,profile,profile_client,profile_device,profile_library,profile_series" doc:"The scope the value is stored at" example:"profile"`
	ProfileID      ID     `query:"profile_id" doc:"Another profile on the account to act for; only the household parent may name one" example:"2"`
	DeviceID       string `query:"device_id" maxLength:"128" doc:"Another registered device of the profile whose profile_device value to address; absent means the declared device" example:"tv-1"`
	LibraryID      ID     `query:"library_id" doc:"The library a profile_library value belongs to" example:"3"`
	SeriesID       string `query:"series_id" maxLength:"128" doc:"The series a profile_series value belongs to" example:"tv:12345"`
	ClientFamily   string `header:"X-Silo-Client-Family" enum:"tv,mobile,tablet,desktop,web" doc:"The client family a profile_client value belongs to" example:"tv"`
	DeviceHeader   string `header:"X-Silo-Device-Id" maxLength:"128" doc:"The client's stable device identifier; the profile_device scope stores against it when device_id is absent" example:"iphone-1"`
	DeviceName     string `header:"X-Silo-Device-Name" maxLength:"120" doc:"Optional display name recorded on the device registry" example:"Living room"`
	DevicePlatform string `header:"X-Silo-Device-Platform" maxLength:"40" doc:"Optional platform recorded on the device registry" example:"iOS"`
}

func (q SettingScopeQuery) identity(ctx context.Context, key string) handlers.SettingIdentityRequest {
	return handlers.SettingIdentityRequest{
		Key:             key,
		Scope:           q.Scope,
		ActiveProfileID: profileFrom(ctx),
		ProfileID:       string(q.ProfileID),
		DeviceID:        q.DeviceID,
		Device:          handlers.NewDeviceMetadata(q.DeviceHeader, q.DeviceName, q.DevicePlatform),
		ClientFamily:    q.ClientFamily,
		LibraryID:       string(q.LibraryID),
		SeriesID:        q.SeriesID,
		VerifyProfile:   profileVerifier(ctx),
	}
}

// profileVerifier answers whether the gate verified a PIN-locked profile for
// this request, the same rule updateProfile applies.
func profileVerifier(ctx context.Context) func(profileID string) error {
	return func(profileID string) error {
		if scope, ok := scopeFrom(ctx); ok && scope.ProfileID == profileID && scope.ProfileVerified && !scope.PINVerificationSkipped {
			return nil
		}
		return access.ErrProfileUnverified
	}
}

// SettingValue is one explicit value as stored: the key, the scope row it
// lives in, and the value itself.
type SettingValue struct {
	Key          string    `json:"key" doc:"The setting key" example:"ui.theme"`
	Scope        string    `json:"scope" doc:"The scope the value is stored at: account, profile, profile_client, profile_device, profile_library or profile_series" example:"profile"`
	ProfileID    ID        `json:"profile_id,omitempty" doc:"The profile the value belongs to; absent at account scope" example:"1"`
	ClientFamily string    `json:"client_family,omitempty" doc:"The client family of a profile_client value" example:"tv"`
	DeviceID     string    `json:"device_id,omitempty" doc:"The device of a profile_device value" example:"iphone-1"`
	LibraryID    ID        `json:"library_id,omitempty" doc:"The library of a profile_library value" example:"3"`
	SeriesID     string    `json:"series_id,omitempty" doc:"The series of a profile_series value" example:"tv:12345"`
	Value        JSONValue `json:"value" doc:"The stored value, normalized to the key's value_schema"`
	Revision     int64     `json:"revision" doc:"The row's revision, incremented on every write" example:"4"`
	UpdatedAt    *Instant  `json:"updated_at,omitempty" doc:"When the value was last written; absent when the store did not record it" example:"2026-01-02T03:04:05.000Z"`
}

// SettingValueOutput is the response of getSettingValue, updateSettingValue
// and updateNavigationShortcut.
type SettingValueOutput struct {
	Body SettingValue
}

// SettingKeyInput names one setting at one scope.
type SettingKeyInput struct {
	Key string `path:"key" minLength:"1" maxLength:"128" doc:"The setting key, as defined in the settings contract" example:"ui.theme"`
	SettingScopeQuery
}

// SettingValueWrite is the value an updateSettingValue request stores.
type SettingValueWrite struct {
	Value JSONValue `json:"value" required:"true" doc:"The value to store; validated and normalized against the key's value_schema"`
}

// SettingValueWriteInput is the updateSettingValue request.
type SettingValueWriteInput struct {
	SettingKeyInput
	Body SettingValueWrite
}

// SettingValuesInput is the listSettingValues request: several keys at one
// scope.
type SettingValuesInput struct {
	Keys []string `query:"keys,explode" required:"true" minItems:"1" maxItems:"200" doc:"The setting keys to read, one keys parameter per key" example:"[\"ui.theme\"]"`
	SettingScopeQuery
}

// ExplicitSettingValue is one key's explicit value at the requested scope,
// or the fact that none is stored there.
type ExplicitSettingValue struct {
	Key          string    `json:"key" doc:"The setting key" example:"ui.theme"`
	Scope        string    `json:"scope" doc:"The scope that was read" example:"profile"`
	ProfileID    ID        `json:"profile_id,omitempty" doc:"The profile the scope belongs to; absent at account scope" example:"1"`
	ClientFamily string    `json:"client_family,omitempty" doc:"The client family of a profile_client scope" example:"tv"`
	DeviceID     string    `json:"device_id,omitempty" doc:"The device of a profile_device scope" example:"iphone-1"`
	LibraryID    ID        `json:"library_id,omitempty" doc:"The library of a profile_library scope" example:"3"`
	SeriesID     string    `json:"series_id,omitempty" doc:"The series of a profile_series scope" example:"tv:12345"`
	IsSet        bool      `json:"is_set" doc:"Whether a value is stored at this scope" example:"true"`
	Value        JSONValue `json:"value,omitempty" doc:"The stored value; absent when is_set is false"`
	Revision     int64     `json:"revision,omitempty" doc:"The row's revision; absent when is_set is false" example:"4"`
	UpdatedAt    *Instant  `json:"updated_at,omitempty" doc:"When the value was last written; absent when unset or unrecorded" example:"2026-01-02T03:04:05.000Z"`
}

// SettingValueCollection is the listSettingValues response body: one entry
// per requested key, in request order, plus the contract revision the
// server resolved them against.
type SettingValueCollection struct {
	Collection[ExplicitSettingValue]
	Revision int `json:"revision" doc:"The settings contract revision this server serves" example:"8"`
}

// SettingValueCollectionOutput is the listSettingValues response.
type SettingValueCollectionOutput struct {
	Body SettingValueCollection
}

// SettingConstraint names the policy input that narrowed an effective value.
type SettingConstraint struct {
	PolicyInput string `json:"policy_input" doc:"The access-policy input the constraint reads" example:"max_playback_quality"`
	Constraint  string `json:"constraint" doc:"How the policy narrows the value; one of the settings contract's constraint kinds" example:"ceiling"`
}

// SettingSourceContext locates the stored row an effective value came from.
// It is the winning row's identity, not the content context a batched
// resolve was asked for: a value inherited from the profile scope answers a
// library context with a source context naming only the profile. The same
// members appear flat on EffectiveSettingValue next to scope; v1 carries both
// and v2 keeps them so a client written against one keeps working.
type SettingSourceContext struct {
	ProfileID    ID     `json:"profile_id,omitempty" doc:"The profile of the winning row" example:"1"`
	ClientFamily string `json:"client_family,omitempty" doc:"The client family of the winning row" example:"tv"`
	DeviceID     string `json:"device_id,omitempty" doc:"The device of the winning row" example:"iphone-1"`
	LibraryID    ID     `json:"library_id,omitempty" doc:"The library of the winning row" example:"3"`
	SeriesID     string `json:"series_id,omitempty" doc:"The series of the winning row" example:"tv:12345"`
}

// EffectiveSettingValue is one key resolved through the contract's
// resolution order: the value that applies, where it came from, and how
// policy narrowed it.
type EffectiveSettingValue struct {
	Key                string                `json:"key" doc:"The setting key" example:"ui.theme"`
	Value              JSONValue             `json:"value" doc:"The value that applies after resolution and policy"`
	Source             string                `json:"source" doc:"The scope the value came from, or default for the contract default" example:"profile"`
	StoredValue        JSONValue             `json:"stored_value,omitempty" doc:"The authored value when policy narrowed it; absent otherwise"`
	Constrained        bool                  `json:"constrained,omitempty" doc:"Whether policy narrowed the authored value" example:"false"`
	ConstraintKind     string                `json:"constraint_kind,omitempty" doc:"How policy narrowed it; absent when unconstrained" example:"ceiling"`
	RequestedValue     JSONValue             `json:"requested_value,omitempty" doc:"The value the profile asked for before the constraint; absent when unconstrained"`
	ConstrainedBy      *SettingConstraint    `json:"constrained_by,omitempty" doc:"The policy input that narrowed it; absent when unconstrained"`
	PermittedValues    []JSONValue           `json:"permitted_values,omitempty" doc:"The values policy still allows; absent when unconstrained"`
	SuggestedValues    []string              `json:"suggested_values,omitempty" doc:"Advisory suggestions for an open setting; never a write allowlist" example:"[\"en\",\"fr\"]"`
	DefinitionRevision int                   `json:"definition_revision" doc:"The contract revision that last changed this key's definition" example:"3"`
	UpdatedAt          *Instant              `json:"updated_at,omitempty" doc:"When the winning stored row was last written; absent for a default" example:"2026-01-02T03:04:05.000Z"`
	SourceContext      *SettingSourceContext `json:"source_context,omitempty" doc:"The identity of the winning stored row (the members scope through series_id, nested); absent for a default. Not the content context a batched resolve was asked for"`
	Scope              string                `json:"scope,omitempty" doc:"The scope of the winning row, so a client can reset exactly that scope; absent for a default" example:"profile"`
	ProfileID          ID                    `json:"profile_id,omitempty" doc:"The profile of the winning row" example:"1"`
	ClientFamily       string                `json:"client_family,omitempty" doc:"The client family of the winning row" example:"tv"`
	DeviceID           string                `json:"device_id,omitempty" doc:"The device of the winning row" example:"iphone-1"`
	LibraryID          ID                    `json:"library_id,omitempty" doc:"The library of the winning row" example:"3"`
	SeriesID           string                `json:"series_id,omitempty" doc:"The series of the winning row" example:"tv:12345"`
}

// EffectiveSettingsQuery is what an effective resolution names beyond the
// keys: the profile and device to resolve for and the content ids in play.
type EffectiveSettingsQuery struct {
	ProfileID      ID       `query:"profile_id" doc:"Another profile on the account to resolve for; only the household parent may name one" example:"2"`
	DeviceID       string   `query:"device_id" maxLength:"128" doc:"Another registered device of the profile to resolve for; absent means the declared device" example:"tv-1"`
	LibraryIDs     []ID     `query:"library_ids,explode" maxItems:"200" doc:"Libraries whose profile_library values take part, one library_ids parameter per id" example:"[\"3\"]"`
	SeriesIDs      []string `query:"series_ids,explode" maxItems:"200" doc:"Series whose profile_series values take part, one series_ids parameter per id" example:"[\"tv:12345\"]"`
	ClientFamily   string   `header:"X-Silo-Client-Family" enum:"tv,mobile,tablet,desktop,web" doc:"The client family whose profile_client values take part; required when a requested key has that scope" example:"tv"`
	DeviceHeader   string   `header:"X-Silo-Device-Id" maxLength:"128" doc:"The client's stable device identifier; its profile_device values take part" example:"iphone-1"`
	DeviceName     string   `header:"X-Silo-Device-Name" maxLength:"120" doc:"Optional display name recorded on the device registry" example:"Living room"`
	DevicePlatform string   `header:"X-Silo-Device-Platform" maxLength:"40" doc:"Optional platform recorded on the device registry" example:"iOS"`
}

func (q EffectiveSettingsQuery) query(ctx context.Context, keys []string) (handlers.EffectiveSettingsQuery, *Problem) {
	libraries := make([]int, 0, len(q.LibraryIDs))
	for _, id := range q.LibraryIDs {
		n, err := intOfID(id)
		if err != nil || n <= 0 {
			return handlers.EffectiveSettingsQuery{}, NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
				WithErrors(ProblemError{Location: locationQueryLibraryIDs, Code: codeInvalid, Detail: "library_ids must name libraries by id"})
		}
		libraries = append(libraries, n)
	}
	return handlers.EffectiveSettingsQuery{
		Keys:            keys,
		ActiveProfileID: profileFrom(ctx),
		ProfileID:       string(q.ProfileID),
		DeviceID:        q.DeviceID,
		Device:          handlers.NewDeviceMetadata(q.DeviceHeader, q.DeviceName, q.DevicePlatform),
		ClientFamily:    q.ClientFamily,
		LibraryIDs:      libraries,
		SeriesIDs:       q.SeriesIDs,
		VerifyProfile:   profileVerifier(ctx),
	}, nil
}

// EffectiveSettingsInput is the listEffectiveSettings request.
type EffectiveSettingsInput struct {
	Keys []string `query:"keys,explode" maxItems:"200" doc:"The setting keys to resolve, one keys parameter per key; absent resolves every server-stored setting" example:"[\"ui.theme\"]"`
	EffectiveSettingsQuery
}

// EffectiveSettingCollection is the listEffectiveSettings response body.
type EffectiveSettingCollection struct {
	Collection[EffectiveSettingValue]
	Revision int `json:"revision" doc:"The settings contract revision this server serves" example:"8"`
}

// EffectiveSettingCollectionOutput is the listEffectiveSettings response.
type EffectiveSettingCollectionOutput struct {
	Body EffectiveSettingCollection
}

// SettingContextRequest is one content context of a batched resolve.
type SettingContextRequest struct {
	ContextID string `json:"context_id" required:"true" minLength:"1" maxLength:"128" doc:"Caller-chosen identifier echoed on the matching result; unique within the request" example:"row-1"`
	LibraryID ID     `json:"library_id,omitempty" doc:"The library the context is in; this or series_id is required" example:"3"`
	SeriesID  string `json:"series_id,omitempty" maxLength:"128" doc:"The series the context is in; this or library_id is required" example:"tv:12345"`
}

// EffectiveSettingsBatch is the resolveEffectiveSettings request body.
//
// The contexts bound is half the seam's content-id budget (200, shared with
// the single-shot GET's library_ids plus series_ids) so that every context
// shape the schema admits — a library, a series, or both — fits in one
// request. Declaring 200 would advertise a capacity the seam refuses once
// more than 100 contexts name both ids.
type EffectiveSettingsBatch struct {
	Keys     []string                `json:"keys" required:"true" minItems:"1" maxItems:"200" doc:"The setting keys to resolve under every context" example:"[\"playback.preferred_quality\"]"`
	Contexts []SettingContextRequest `json:"contexts" required:"true" minItems:"1" maxItems:"100" doc:"The content contexts to resolve under; each context may name a library and a series"`
}

// EffectiveSettingsBatchTarget is what a batch resolution names beyond the
// body: the client family and the declared device. The batch resolves for
// the acting profile under each body context, so it carries none of the
// profile_id, device_id, library_ids or series_ids parameters of
// listEffectiveSettings; v1 read those on the batch route and ignored them.
type EffectiveSettingsBatchTarget struct {
	ClientFamily   string `header:"X-Silo-Client-Family" enum:"tv,mobile,tablet,desktop,web" doc:"The client family whose profile_client values take part; required when a requested key has that scope" example:"tv"`
	DeviceHeader   string `header:"X-Silo-Device-Id" maxLength:"128" doc:"The client's stable device identifier; its profile_device values take part" example:"iphone-1"`
	DeviceName     string `header:"X-Silo-Device-Name" maxLength:"120" doc:"Optional display name recorded on the device registry" example:"Living room"`
	DevicePlatform string `header:"X-Silo-Device-Platform" maxLength:"40" doc:"Optional platform recorded on the device registry" example:"iOS"`
}

func (t EffectiveSettingsBatchTarget) query(ctx context.Context, keys []string) handlers.EffectiveSettingsQuery {
	return handlers.EffectiveSettingsQuery{
		Keys:            keys,
		ActiveProfileID: profileFrom(ctx),
		Device:          handlers.NewDeviceMetadata(t.DeviceHeader, t.DeviceName, t.DevicePlatform),
		ClientFamily:    t.ClientFamily,
	}
}

// EffectiveSettingsBatchInput is the resolveEffectiveSettings request.
type EffectiveSettingsBatchInput struct {
	EffectiveSettingsBatchTarget
	Body EffectiveSettingsBatch
}

// EffectiveSettingContext is one context's resolved settings.
type EffectiveSettingContext struct {
	ContextID string                  `json:"context_id" doc:"The context_id of the request entry this answers" example:"row-1"`
	Settings  []EffectiveSettingValue `json:"settings" doc:"The requested keys resolved under this context, in request order"`
}

// EffectiveSettingContextCollection is the resolveEffectiveSettings
// response body.
type EffectiveSettingContextCollection struct {
	Collection[EffectiveSettingContext]
	Revision int `json:"revision" doc:"The settings contract revision this server serves" example:"8"`
}

// EffectiveSettingContextCollectionOutput is the resolveEffectiveSettings
// response.
type EffectiveSettingContextCollectionOutput struct {
	Body EffectiveSettingContextCollection
}

// NavigationShortcutMutation is one desired-state edit of the acting
// profile's nav.shortcuts document.
type NavigationShortcutMutation struct {
	Item    NavigationShortcutItem `json:"item" required:"true" doc:"The shortcut to add or remove; its destination identity, not its label, decides which entry it is"`
	Present bool                   `json:"present" required:"true" doc:"true adds or relabels the shortcut, false removes it" example:"true"`
}

// NavigationShortcutMutationInput is the updateNavigationShortcut request.
type NavigationShortcutMutationInput struct {
	Body NavigationShortcutMutation
}

// PluginSettingsWrite is the values an updatePluginSettings request stores.
type PluginSettingsWrite struct {
	Values PluginSettingValues `json:"values" required:"true" doc:"The account's values, replacing the stored set; keys are the plugin's user_config_schema keys"`
}

// PluginSettingsWriteInput is the updatePluginSettings request.
type PluginSettingsWriteInput struct {
	InstallationID ID `path:"installation_id" doc:"The plugin installation" example:"3"`
	Body           PluginSettingsWrite
}

// The seam's names for rejected request members, and the locations the
// operations declare those members at.
const (
	fieldKey    = "key"
	fieldKeys   = "keys"
	fieldValues = "values"

	locationPathKey         = "path.key"
	locationQueryKeys       = "query.keys"
	locationQueryScope      = "query.scope"
	locationQueryLibraryID  = "query.library_id"
	locationQueryLibraryIDs = "query.library_ids"
	locationQuerySeriesIDs  = "query.series_ids"
	locationBodyValue       = "body.value"
	locationBodyValues      = "body.values"
	locationBodyKeys        = "body.keys"
	locationBodyItem        = "body.item"
	locationBodyContexts    = "body.contexts"
)

// settingFieldLocations maps the seam's rejected-field names onto the
// request locations the operations declare them at.
var settingFieldLocations = map[string]string{
	fieldKey:         locationPathKey,
	fieldKeys:        locationQueryKeys,
	"scope":          locationQueryScope,
	"profile_id":     "query.profile_id",
	"profile_header": locationProfileHeader,
	fieldDeviceID:    "query.device_id",
	"device_header":  "header." + deviceIDHeader,
	"client_family":  "header." + clientFamilyHeader,
	"library_id":     locationQueryLibraryID,
	"series_id":      "query.series_id",
	"value":          locationBodyValue,
	"item":           locationBodyItem,
	"present":        "body.present",
	"contexts":       locationBodyContexts,
}

// settingValueProblem renders the seam's decision. A rejected request
// member is a validation failure at the location it was declared at; a key
// the contract does not define is one too (v1 answers 404), since the key
// is request input, not a resource. Everything else keeps its status.
func settingValueProblem(err error, overrides map[string]string) *Problem {
	var apiErr *handlers.APIError
	if !errors.As(err, &apiErr) || apiErr.Field == "" || apiErr.Status >= 500 {
		return serviceProblem(err)
	}
	unknownKey := apiErr.Status == http.StatusNotFound && (apiErr.Field == fieldKey || apiErr.Field == fieldKeys)
	if apiErr.Status != http.StatusBadRequest && !unknownKey {
		return serviceProblem(err)
	}
	location, ok := overrides[apiErr.Field]
	if !ok {
		location = settingFieldLocations[apiErr.Field]
	}
	if location == "" {
		location = locationBody
	}
	return NewProblem(TypeValidationFailed, "The request did not pass validation; see errors.").
		WithErrors(ProblemError{Location: location, Code: codeInvalid, Detail: apiErr.Message})
}

func (reg *Registry) settingValuesReady(ctx context.Context) (int, *Problem) {
	if reg.deps.SettingValues == nil {
		return 0, unavailable("setting values")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return 0, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	return claims.UserID, nil
}

func (reg *Registry) getSettingValue(ctx context.Context, in *SettingKeyInput) (*SettingValueOutput, error) {
	userID, p := reg.settingValuesReady(ctx)
	if p != nil {
		return nil, p
	}
	view, err := reg.deps.SettingValues.GetSettingValue(ctx, userID, in.identity(ctx, in.Key))
	if err != nil {
		return nil, settingValueProblem(err, nil)
	}
	return &SettingValueOutput{Body: settingValueOf(view)}, nil
}

func (reg *Registry) updateSettingValue(ctx context.Context, in *SettingValueWriteInput) (*SettingValueOutput, error) {
	userID, p := reg.settingValuesReady(ctx)
	if p != nil {
		return nil, p
	}
	view, err := reg.deps.SettingValues.SetSettingValue(ctx, userID, in.identity(ctx, in.Key), json.RawMessage(in.Body.Value))
	if err != nil {
		return nil, settingValueProblem(err, nil)
	}
	return &SettingValueOutput{Body: settingValueOf(view)}, nil
}

func (reg *Registry) deleteSettingValue(ctx context.Context, in *SettingKeyInput) (*struct{}, error) {
	userID, p := reg.settingValuesReady(ctx)
	if p != nil {
		return nil, p
	}
	if err := reg.deps.SettingValues.DeleteSettingValue(ctx, userID, in.identity(ctx, in.Key)); err != nil {
		return nil, settingValueProblem(err, nil)
	}
	return nil, nil
}

func (reg *Registry) listSettingValues(ctx context.Context, in *SettingValuesInput) (*SettingValueCollectionOutput, error) {
	userID, p := reg.settingValuesReady(ctx)
	if p != nil {
		return nil, p
	}
	views, err := reg.deps.SettingValues.ListSettingValues(ctx, userID, in.Keys, in.identity(ctx, ""))
	if err != nil {
		return nil, settingValueProblem(err, map[string]string{fieldKey: locationQueryKeys})
	}
	items := make([]ExplicitSettingValue, 0, len(views))
	for _, v := range views {
		items = append(items, ExplicitSettingValue{
			Key: v.Key, Scope: v.Scope, ProfileID: ID(v.ProfileID), ClientFamily: v.ClientFamily, DeviceID: v.DeviceID,
			LibraryID: optionalIntID(v.LibraryID), SeriesID: v.SeriesID, IsSet: v.IsSet, Value: JSONValue(v.Value),
			Revision: v.Revision, UpdatedAt: storedInstant(v.UpdatedAt),
		})
	}
	return &SettingValueCollectionOutput{Body: SettingValueCollection{
		Collection: NewCollection(items), Revision: reg.deps.SettingValues.ContractRevision(),
	}}, nil
}

func (reg *Registry) listEffectiveSettings(ctx context.Context, in *EffectiveSettingsInput) (*EffectiveSettingCollectionOutput, error) {
	userID, p := reg.settingValuesReady(ctx)
	if p != nil {
		return nil, p
	}
	q, p := in.query(ctx, in.Keys)
	if p != nil {
		return nil, p
	}
	views, err := reg.deps.SettingValues.ResolveEffectiveSettings(ctx, userID, q)
	if err != nil {
		// The seam names the combined library_ids/series_ids bound after
		// its single-id field; this operation declares the lists.
		return nil, settingValueProblem(err, map[string]string{
			"library_id": locationQueryLibraryIDs, "series_id": locationQuerySeriesIDs,
		})
	}
	return &EffectiveSettingCollectionOutput{Body: EffectiveSettingCollection{
		Collection: NewCollection(effectiveSettingValuesOf(views)), Revision: reg.deps.SettingValues.ContractRevision(),
	}}, nil
}

func (reg *Registry) resolveEffectiveSettings(ctx context.Context, in *EffectiveSettingsBatchInput) (*EffectiveSettingContextCollectionOutput, error) {
	userID, p := reg.settingValuesReady(ctx)
	if p != nil {
		return nil, p
	}
	q := in.query(ctx, in.Body.Keys)
	contexts := make([]handlers.EffectiveContextRequest, 0, len(in.Body.Contexts))
	for _, c := range in.Body.Contexts {
		req := handlers.EffectiveContextRequest{ContextID: c.ContextID, SeriesID: c.SeriesID}
		if c.LibraryID != "" {
			// The seam accepts a numeric string, which is what an ID is.
			raw, _ := json.Marshal(string(c.LibraryID))
			req.LibraryID = raw
		}
		contexts = append(contexts, req)
	}
	views, err := reg.deps.SettingValues.ResolveEffectiveSettingContexts(ctx, userID, q, contexts)
	if err != nil {
		return nil, settingValueProblem(err, map[string]string{fieldKeys: locationBodyKeys})
	}
	items := make([]EffectiveSettingContext, 0, len(views))
	for _, v := range views {
		items = append(items, EffectiveSettingContext{ContextID: v.ContextID, Settings: effectiveSettingValuesOf(v.Settings)})
	}
	return &EffectiveSettingContextCollectionOutput{Body: EffectiveSettingContextCollection{
		Collection: NewCollection(items), Revision: reg.deps.SettingValues.ContractRevision(),
	}}, nil
}

// navigationShortcutConflictDescription documents updateNavigationShortcut's
// 409: the seam's compare-and-set loop lost every retry to concurrent
// writers, nothing was stored, and the same request may be sent again.
const navigationShortcutConflictDescription = "retryable conflict: concurrent shortcut updates exhausted the compare-and-set retries; retry the request"

func (reg *Registry) updateNavigationShortcut(ctx context.Context, in *NavigationShortcutMutationInput) (*SettingValueOutput, error) {
	userID, p := reg.settingValuesReady(ctx)
	if p != nil {
		return nil, p
	}
	view, err := reg.deps.SettingValues.SetNavigationShortcut(ctx, userID, profileFrom(ctx), json.RawMessage(in.Body.Item), in.Body.Present)
	if err != nil {
		return nil, settingValueProblem(err, nil)
	}
	return &SettingValueOutput{Body: settingValueOf(view)}, nil
}

func (reg *Registry) updatePluginSettings(ctx context.Context, in *PluginSettingsWriteInput) (*PluginSettingsOutput, error) {
	if reg.deps.PluginSettings == nil {
		return nil, unavailable("plugin settings")
	}
	claims := claimsFrom(ctx)
	if claims == nil {
		return nil, NewProblem(TypeAuthenticationRequired, "Authentication is required.")
	}
	id, err := intOfID(in.InstallationID)
	if err != nil || id <= 0 {
		return nil, NewProblem(TypeNotFound, "Plugin installation not found")
	}
	if err := reg.deps.PluginSettings.SetUserPluginSettings(ctx, claims.UserID, id, NonNilMap(map[string]string(in.Body.Values))); err != nil {
		return nil, settingValueProblem(err, map[string]string{fieldValues: locationBodyValues})
	}
	view, err := reg.deps.PluginSettings.GetUserPluginSettings(ctx, claims.UserID, id)
	if err != nil {
		return nil, serviceProblem(err)
	}
	return &PluginSettingsOutput{Body: PluginSettings{
		Installation: pluginSettingsInstallationFromView(view.Installation),
		Values:       PluginSettingValues(NonNilMap(view.Values)),
	}}, nil
}

func settingValueOf(v handlers.SettingValueView) SettingValue {
	return SettingValue{
		Key: v.Key, Scope: v.Scope, ProfileID: ID(v.ProfileID), ClientFamily: v.ClientFamily, DeviceID: v.DeviceID,
		LibraryID: optionalIntID(v.LibraryID), SeriesID: v.SeriesID, Value: JSONValue(v.Value),
		Revision: v.Revision, UpdatedAt: storedInstant(v.UpdatedAt),
	}
}

func effectiveSettingValuesOf(views []handlers.EffectiveSettingValueView) []EffectiveSettingValue {
	out := make([]EffectiveSettingValue, 0, len(views))
	for _, v := range views {
		e := EffectiveSettingValue{
			Key: v.Key, Value: JSONValue(v.Value), Source: v.Source, StoredValue: JSONValue(v.StoredValue),
			Constrained: v.Constrained, ConstraintKind: v.ConstraintKind, RequestedValue: JSONValue(v.RequestedValue),
			SuggestedValues: v.SuggestedValues, DefinitionRevision: v.DefinitionRevision, UpdatedAt: storedInstant(v.UpdatedAt),
			Scope: v.Scope, ProfileID: ID(v.ProfileID), ClientFamily: v.ClientFamily, DeviceID: v.DeviceID,
			LibraryID: optionalIntID(v.LibraryID), SeriesID: v.SeriesID,
		}
		if v.ConstrainedBy != nil {
			e.ConstrainedBy = &SettingConstraint{PolicyInput: v.ConstrainedBy.PolicyInput, Constraint: string(v.ConstrainedBy.Constraint)}
		}
		if len(v.PermittedValues) > 0 {
			e.PermittedValues = make([]JSONValue, 0, len(v.PermittedValues))
			for _, pv := range v.PermittedValues {
				e.PermittedValues = append(e.PermittedValues, JSONValue(pv))
			}
		}
		if v.SourceContext != nil {
			e.SourceContext = &SettingSourceContext{
				ProfileID: ID(v.SourceContext.ProfileID), ClientFamily: v.SourceContext.ClientFamily, DeviceID: v.SourceContext.DeviceID,
				LibraryID: optionalIntID(v.SourceContext.LibraryID), SeriesID: v.SourceContext.SeriesID,
			}
		}
		out = append(out, e)
	}
	return out
}

// optionalIntID renders a v1 "0 means none" integer id as an absent ID.
func optionalIntID(n int) ID {
	if n == 0 {
		return ""
	}
	return IDFromInt(int64(n))
}
