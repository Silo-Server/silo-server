package apiv2

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

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
					Type:                 "object",
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
