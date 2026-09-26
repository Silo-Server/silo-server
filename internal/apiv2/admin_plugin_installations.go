package apiv2

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// AdminPluginInventoryService is the slice of *handlers.PluginHandler the
// catalog and installation reads use. Both listeners share the projection,
// so global configuration reaches v2 through the same secret redaction.
type AdminPluginInventoryService interface {
	ListAdminPluginCatalog(context.Context) ([]handlers.PluginCatalogEntryView, error)
	ListAdminPluginInstallations(context.Context) ([]handlers.PluginInstallationView, error)
}

// PluginJSONValue is a plugin-defined JSON object the contract does not
// shape: manifest metadata, a redacted configuration value, or a task
// trigger. The extension-bag marker is what lets the spec lint accept it.
type PluginJSONValue map[string]any

func (PluginJSONValue) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, AdditionalProperties: true, Extensions: map[string]any{extExtensionBag: "plugin-json-value"}}
}

// PluginConfigSchemaValue is a plugin's configuration JSON Schema document as
// the plugin wrote it; the server validates values against it and does not
// interpret it here.
type PluginConfigSchemaValue json.RawMessage

func (v PluginConfigSchemaValue) MarshalJSON() ([]byte, error) {
	if len(v) == 0 {
		return []byte("null"), nil
	}
	return json.RawMessage(v).MarshalJSON()
}
func (v *PluginConfigSchemaValue) UnmarshalJSON(data []byte) error {
	*v = append((*v)[:0], data...)
	return nil
}
func (PluginConfigSchemaValue) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Description: "Plugin-defined JSON Schema document; null when the plugin gives none.", Extensions: map[string]any{extExtensionBag: "plugin-config-json-schema"}}
}

type AdminPluginPresentation struct {
	DisplayName         string `json:"display_name"`
	Summary             string `json:"summary"`
	DescriptionMarkdown string `json:"description_markdown"`
	SetupMarkdown       string `json:"setup_markdown"`
	HomepageURL         string `json:"homepage_url"`
	SourceURL           string `json:"source_url"`
	SupportURL          string `json:"support_url"`
	ChangelogURL        string `json:"changelog_url"`
	PublisherName       string `json:"publisher_name"`
	PublisherURL        string `json:"publisher_url"`
	LicenseSPDX         string `json:"license_spdx"`
}
type AdminFormOption struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}
type AdminFormCondition struct {
	Field  string   `json:"field"`
	Equals []string `json:"equals"`
}
type AdminPluginFormValidation struct {
	HasMin    bool    `json:"has_min"`
	Min       float64 `json:"min"`
	HasMax    bool    `json:"has_max"`
	Max       float64 `json:"max"`
	Pattern   string  `json:"pattern,omitempty"`
	MinLength int32   `json:"min_length"`
	MaxLength int32   `json:"max_length"`
}
type AdminPluginFormField struct {
	Key                 string                     `json:"key"`
	Label               string                     `json:"label"`
	Description         string                     `json:"description,omitempty"`
	Control             string                     `json:"control"`
	Placeholder         string                     `json:"placeholder,omitempty"`
	Required            bool                       `json:"required"`
	Secret              bool                       `json:"secret"`
	Multiline           bool                       `json:"multiline"`
	DefaultValue        PluginConfigSchemaValue    `json:"default_value,omitempty"`
	Options             []AdminFormOption          `json:"options"`
	Rows                int32                      `json:"rows"`
	DynamicOptions      bool                       `json:"dynamic_options"`
	ShowWhen            []AdminFormCondition       `json:"show_when"`
	Validation          *AdminPluginFormValidation `json:"validation,omitempty"`
	ExclusiveGroupField string                     `json:"exclusive_group_field,omitempty"`
}
type AdminPluginFormSection struct {
	Key              string               `json:"key"`
	Title            string               `json:"title"`
	Description      string               `json:"description,omitempty"`
	Collapsible      bool                 `json:"collapsible"`
	CollapsedDefault bool                 `json:"collapsed_default"`
	FieldKeys        []string             `json:"field_keys"`
	ShowWhen         []AdminFormCondition `json:"show_when"`
}
type AdminPluginForm struct {
	Fields      []AdminPluginFormField   `json:"fields"`
	SubmitLabel string                   `json:"submit_label,omitempty"`
	Sections    []AdminPluginFormSection `json:"sections"`
}
type AdminPluginConfigSchema struct {
	Key         string           `json:"key"`
	Title       string           `json:"title"`
	Description string           `json:"description"`
	JSONSchema  string           `json:"json_schema"`
	Required    bool             `json:"required"`
	AdminForm   *AdminPluginForm `json:"admin_form,omitempty"`
}
type AdminPluginCapability struct {
	Type          string                    `json:"type"`
	ID            string                    `json:"id"`
	DisplayName   string                    `json:"display_name"`
	Description   string                    `json:"description"`
	Subscriptions []string                  `json:"subscriptions"`
	ConfigSchema  []AdminPluginConfigSchema `json:"config_schema"`
	Metadata      PluginJSONValue           `json:"metadata"`
}
type AdminPluginConfigValue struct {
	Key               string          `json:"key"`
	Value             PluginJSONValue `json:"value" doc:"Public fields only; manifest-declared secrets are redacted"`
	ConfiguredSecrets []string        `json:"configured_secrets" doc:"Secret fields that hold a value on the server"`
}
type AdminPluginAuthBinding struct {
	CapabilityID  string  `json:"capability_id"`
	Enabled       bool    `json:"enabled"`
	DisplayOrder  int     `json:"display_order"`
	AutoProvision bool    `json:"auto_provision"`
	DefaultLogin  bool    `json:"default_login"`
	CreatedAt     Instant `json:"created_at"`
	UpdatedAt     Instant `json:"updated_at"`
}
type AdminPluginTaskBinding struct {
	CapabilityID string          `json:"capability_id"`
	Enabled      bool            `json:"enabled"`
	Trigger      PluginJSONValue `json:"trigger"`
	CreatedAt    Instant         `json:"created_at"`
	UpdatedAt    Instant         `json:"updated_at"`
}

// AdminPluginCatalogEntry is one discoverable plugin version.
type AdminPluginCatalogEntry struct {
	RepositoryID       ID                        `json:"repository_id"`
	PluginID           string                    `json:"plugin_id"`
	Version            string                    `json:"version"`
	ArchiveURL         string                    `json:"archive_url"`
	SourceKind         string                    `json:"source_kind" enum:"silo,approved_community,external"`
	RepositoryName     string                    `json:"repository_name"`
	RepoURL            string                    `json:"repo_url,omitempty"`
	Presentation       *AdminPluginPresentation  `json:"presentation,omitempty"`
	Capabilities       []AdminPluginCapability   `json:"capabilities"`
	GlobalConfigSchema []AdminPluginConfigSchema `json:"global_config_schema"`
	UserConfigSchema   []AdminPluginConfigSchema `json:"user_config_schema"`
	Routes             []PluginRoute             `json:"routes"`
	Assets             []PluginAsset             `json:"assets"`
	Metadata           PluginJSONValue           `json:"metadata"`
}

// AdminPluginRuntime is one installation's process state. A resident plugin
// (one declaring a capability the server keeps running, such as a network
// access provider) is supervised: started at boot, restarted after a crash
// with exponential backoff, and parked as failed after repeated failures
// until an administrator restarts or reconfigures it. Other plugins start on
// first use and report only running or stopped.
type AdminPluginRuntime struct {
	Resident      bool     `json:"resident" doc:"True when the server supervises this plugin's process"`
	State         string   `json:"state" enum:"stopped,starting,running,backoff,failed" doc:"Process state; backoff and failed occur only for resident plugins"`
	RestartCount  int      `json:"restart_count" doc:"Automatic restarts since the plugin last ran stably or was restarted by an administrator"`
	LastError     string   `json:"last_error,omitempty" doc:"Why the process last stopped or failed to start"`
	LastStartedAt *Instant `json:"last_started_at,omitempty"`
	NextRestartAt *Instant `json:"next_restart_at,omitempty" doc:"Scheduled automatic restart while in backoff"`
}

// AdminPluginInstallation is one manageable installation with its redacted
// configuration and bindings.
type AdminPluginInstallation struct {
	ID                 ID                        `json:"id"`
	RepositoryID       *ID                       `json:"repository_id,omitempty"`
	PluginID           string                    `json:"plugin_id"`
	Version            string                    `json:"version"`
	InstallPath        string                    `json:"install_path"`
	Enabled            bool                      `json:"enabled"`
	Kind               string                    `json:"kind"`
	UpdatePolicy       string                    `json:"update_policy"`
	AvailableVersion   string                    `json:"available_version,omitempty"`
	SourceKind         string                    `json:"source_kind" enum:"silo,approved_community,external"`
	RepositoryName     string                    `json:"repository_name,omitempty"`
	RepoURL            string                    `json:"repo_url,omitempty"`
	Presentation       *AdminPluginPresentation  `json:"presentation,omitempty"`
	UpdatesPaused      bool                      `json:"updates_paused"`
	Capabilities       []AdminPluginCapability   `json:"capabilities"`
	GlobalConfigSchema []AdminPluginConfigSchema `json:"global_config_schema"`
	UserConfigSchema   []AdminPluginConfigSchema `json:"user_config_schema"`
	Routes             []PluginRoute             `json:"routes"`
	Assets             []PluginAsset             `json:"assets"`
	Metadata           PluginJSONValue           `json:"metadata"`
	GlobalConfigs      []AdminPluginConfigValue  `json:"global_configs"`
	AuthBindings       []AdminPluginAuthBinding  `json:"auth_bindings"`
	TaskBindings       []AdminPluginTaskBinding  `json:"task_bindings"`
	Runtime            AdminPluginRuntime        `json:"runtime"`
	CreatedAt          Instant                   `json:"created_at"`
	UpdatedAt          Instant                   `json:"updated_at"`
}

type AdminPluginCatalogInput struct {
	LimitParam
	Cursor string `query:"cursor" maxLength:"8192"`
}
type AdminPluginCatalogOutput struct {
	Body Collection[AdminPluginCatalogEntry]
}
type AdminPluginInstallationsInput struct {
	LimitParam
	Cursor string `query:"cursor" maxLength:"8192"`
}
type AdminPluginInstallationsOutput struct {
	Body Collection[AdminPluginInstallation]
}

func adminPluginPresentationOf(p *handlers.PluginPresentationView) *AdminPluginPresentation {
	if p == nil {
		return nil
	}
	return &AdminPluginPresentation{DisplayName: p.DisplayName, Summary: p.Summary, DescriptionMarkdown: p.DescriptionMarkdown, SetupMarkdown: p.SetupMarkdown, HomepageURL: p.HomepageURL, SourceURL: p.SourceURL, SupportURL: p.SupportURL, ChangelogURL: p.ChangelogURL, PublisherName: p.PublisherName, PublisherURL: p.PublisherURL, LicenseSPDX: p.LicenseSPDX}
}
func adminPluginFormConditionsOf(cs []plugins.AdminFormConditionView) []AdminFormCondition {
	out := make([]AdminFormCondition, 0, len(cs))
	for _, c := range cs {
		out = append(out, AdminFormCondition{Field: c.Field, Equals: NonNil(c.Equals)})
	}
	return out
}
func adminPluginFormOf(f *plugins.AdminFormView) (*AdminPluginForm, error) {
	if f == nil {
		return nil, nil
	}
	out := &AdminPluginForm{Fields: make([]AdminPluginFormField, 0, len(f.Fields)), SubmitLabel: f.SubmitLabel, Sections: make([]AdminPluginFormSection, 0, len(f.Sections))}
	for _, fld := range f.Fields {
		item := AdminPluginFormField{Key: fld.Key, Label: fld.Label, Description: fld.Description, Control: fld.Control, Placeholder: fld.Placeholder, Required: fld.Required, Secret: fld.Secret, Multiline: fld.Multiline, Options: make([]AdminFormOption, 0, len(fld.Options)), Rows: fld.Rows, DynamicOptions: fld.DynamicOptions, ShowWhen: adminPluginFormConditionsOf(fld.ShowWhen), ExclusiveGroupField: fld.ExclusiveGroupField}
		if fld.DefaultValue != nil {
			raw, err := json.Marshal(fld.DefaultValue)
			if err != nil {
				return nil, err
			}
			item.DefaultValue = raw
		}
		for _, o := range fld.Options {
			item.Options = append(item.Options, AdminFormOption{Value: o.Value, Label: o.Label, Description: o.Description})
		}
		if v := fld.Validation; v != nil {
			item.Validation = &AdminPluginFormValidation{HasMin: v.HasMin, Min: v.Min, HasMax: v.HasMax, Max: v.Max, Pattern: v.Pattern, MinLength: v.MinLength, MaxLength: v.MaxLength}
		}
		out.Fields = append(out.Fields, item)
	}
	for _, s := range f.Sections {
		out.Sections = append(out.Sections, AdminPluginFormSection{Key: s.Key, Title: s.Title, Description: s.Description, Collapsible: s.Collapsible, CollapsedDefault: s.CollapsedDefault, FieldKeys: NonNil(s.FieldKeys), ShowWhen: adminPluginFormConditionsOf(s.ShowWhen)})
	}
	return out, nil
}
func adminPluginConfigSchemasOf(views []plugins.ConfigSchemaView) ([]AdminPluginConfigSchema, error) {
	out := make([]AdminPluginConfigSchema, 0, len(views))
	for _, v := range views {
		form, err := adminPluginFormOf(v.AdminForm)
		if err != nil {
			return nil, err
		}
		out = append(out, AdminPluginConfigSchema{Key: v.Key, Title: v.Title, Description: v.Description, JSONSchema: v.JSONSchema, Required: v.Required, AdminForm: form})
	}
	return out, nil
}
func adminPluginCapabilitiesOf(views []handlers.PluginCapabilityView) ([]AdminPluginCapability, error) {
	out := make([]AdminPluginCapability, 0, len(views))
	for _, v := range views {
		schemas, err := adminPluginConfigSchemasOf(v.ConfigSchema)
		if err != nil {
			return nil, err
		}
		out = append(out, AdminPluginCapability{Type: v.Type, ID: v.ID, DisplayName: v.DisplayName, Description: v.Description, Subscriptions: NonNil(v.Subscriptions), ConfigSchema: schemas, Metadata: NonNilMap(PluginJSONValue(v.Metadata))})
	}
	return out, nil
}
func pluginRoutesOf(views []handlers.PluginRouteView) []PluginRoute {
	out := make([]PluginRoute, 0, len(views))
	for _, r := range views {
		out = append(out, PluginRoute{ID: r.ID, Method: r.Method, Path: r.Path, Access: r.Access, Navigable: r.Navigable, NavigationLabel: r.NavigationLabel, NavigationKind: r.NavigationKind, StaticAsset: r.StaticAsset})
	}
	return out
}
func pluginAssetsOf(views []handlers.PluginAssetView) []PluginAsset {
	out := make([]PluginAsset, 0, len(views))
	for _, a := range views {
		out = append(out, PluginAsset{Path: a.Path, ContentType: a.ContentType, Integrity: a.Integrity})
	}
	return out
}

func adminPluginCatalogEntryOf(v handlers.PluginCatalogEntryView) (AdminPluginCatalogEntry, error) {
	caps, err := adminPluginCapabilitiesOf(v.Capabilities)
	if err != nil {
		return AdminPluginCatalogEntry{}, err
	}
	global, err := adminPluginConfigSchemasOf(v.GlobalConfigSchema)
	if err != nil {
		return AdminPluginCatalogEntry{}, err
	}
	user, err := adminPluginConfigSchemasOf(v.UserConfigSchema)
	if err != nil {
		return AdminPluginCatalogEntry{}, err
	}
	return AdminPluginCatalogEntry{RepositoryID: IDFromInt(int64(v.RepositoryID)), PluginID: v.PluginID, Version: v.Version, ArchiveURL: v.ArchiveURL, SourceKind: v.SourceKind, RepositoryName: v.RepositoryName, RepoURL: v.RepoURL, Presentation: adminPluginPresentationOf(v.Presentation), Capabilities: caps, GlobalConfigSchema: global, UserConfigSchema: user, Routes: pluginRoutesOf(v.Routes), Assets: pluginAssetsOf(v.Assets), Metadata: NonNilMap(PluginJSONValue(v.Metadata))}, nil
}

func adminPluginRuntimeOf(v *handlers.PluginRuntimeView) AdminPluginRuntime {
	if v == nil {
		return AdminPluginRuntime{State: string(plugins.ResidentStopped)}
	}
	out := AdminPluginRuntime{Resident: v.Resident, State: v.State, RestartCount: v.RestartCount, LastError: v.LastError}
	if out.State == "" {
		out.State = string(plugins.ResidentStopped)
	}
	if v.LastStartedAt != nil {
		out.LastStartedAt = new(NewInstant(*v.LastStartedAt))
	}
	if v.NextRestartAt != nil {
		out.NextRestartAt = new(NewInstant(*v.NextRestartAt))
	}
	return out
}

func adminPluginInstallationOf(v handlers.PluginInstallationView) (AdminPluginInstallation, error) {
	caps, err := adminPluginCapabilitiesOf(v.Capabilities)
	if err != nil {
		return AdminPluginInstallation{}, err
	}
	global, err := adminPluginConfigSchemasOf(v.GlobalConfigSchema)
	if err != nil {
		return AdminPluginInstallation{}, err
	}
	user, err := adminPluginConfigSchemasOf(v.UserConfigSchema)
	if err != nil {
		return AdminPluginInstallation{}, err
	}
	out := AdminPluginInstallation{ID: IDFromInt(int64(v.ID)), PluginID: v.PluginID, Version: v.Version, InstallPath: v.InstallPath, Enabled: v.Enabled, Kind: v.Kind, UpdatePolicy: v.UpdatePolicy, SourceKind: v.SourceKind, RepositoryName: v.RepositoryName, RepoURL: v.RepoURL, Presentation: adminPluginPresentationOf(v.Presentation), UpdatesPaused: v.UpdatesPaused, Capabilities: caps, GlobalConfigSchema: global, UserConfigSchema: user, Routes: pluginRoutesOf(v.Routes), Assets: pluginAssetsOf(v.Assets), Metadata: NonNilMap(PluginJSONValue(v.Metadata)), GlobalConfigs: make([]AdminPluginConfigValue, 0, len(v.GlobalConfigs)), AuthBindings: make([]AdminPluginAuthBinding, 0, len(v.AuthBindings)), TaskBindings: make([]AdminPluginTaskBinding, 0, len(v.TaskBindings)), Runtime: adminPluginRuntimeOf(v.Runtime), CreatedAt: NewInstant(v.CreatedAt), UpdatedAt: NewInstant(v.UpdatedAt)}
	if v.RepositoryID != nil {
		out.RepositoryID = new(IDFromInt(int64(*v.RepositoryID)))
	}
	if v.AvailableVersion != nil {
		out.AvailableVersion = *v.AvailableVersion
	}
	for _, c := range v.GlobalConfigs {
		out.GlobalConfigs = append(out.GlobalConfigs, AdminPluginConfigValue{Key: c.Key, Value: NonNilMap(PluginJSONValue(c.Value)), ConfiguredSecrets: NonNil(c.ConfiguredSecrets)})
	}
	for _, b := range v.AuthBindings {
		out.AuthBindings = append(out.AuthBindings, AdminPluginAuthBinding{CapabilityID: b.CapabilityID, Enabled: b.Enabled, DisplayOrder: b.DisplayOrder, AutoProvision: b.AutoProvision, DefaultLogin: b.DefaultLogin, CreatedAt: NewInstant(b.CreatedAt), UpdatedAt: NewInstant(b.UpdatedAt)})
	}
	for _, b := range v.TaskBindings {
		out.TaskBindings = append(out.TaskBindings, AdminPluginTaskBinding{CapabilityID: b.CapabilityID, Enabled: b.Enabled, Trigger: NonNilMap(PluginJSONValue(b.Trigger)), CreatedAt: NewInstant(b.CreatedAt), UpdatedAt: NewInstant(b.UpdatedAt)})
	}
	return out, nil
}

type adminPluginCatalogPosition struct{ PluginID, Version string }

func compareAdminPluginCatalogPosition(a, b adminPluginCatalogPosition) int {
	return cmp.Or(cmp.Compare(a.PluginID, b.PluginID), cmp.Compare(a.Version, b.Version))
}

func registerAdminPluginInventory(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := func(method, path, id, summary string) Operation {
		return Operation{Operation: humaOp(method, Prefix+"/admin/plugins/"+path, id, "admin-plugins", summary), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true}
	}
	Register(reg, op("GET", "catalog", "listAdminPluginCatalog", "Read the discoverable plugin catalog. Each page fetches every enabled repository index live and records the fetch time; continuation enumerates that fetch's result and is not a snapshot."), func(ctx context.Context, in *AdminPluginCatalogInput) (*AdminPluginCatalogOutput, error) {
		if reg.deps.AdminPluginInventory == nil {
			return nil, unavailable("plugin inventory")
		}
		scope := CursorScope{OperationID: "listAdminPluginCatalog", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx) + "/" + viewerScopeDigest(ctx), Filter: strconv.Itoa(in.Limit), Sort: "plugin_id", Tiebreaker: "version"}
		var after adminPluginCatalogPosition
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
		}
		rows, err := reg.deps.AdminPluginInventory.ListAdminPluginCatalog(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		rows = slices.Clone(rows)
		slices.SortFunc(rows, func(a, b handlers.PluginCatalogEntryView) int {
			return compareAdminPluginCatalogPosition(adminPluginCatalogPosition{a.PluginID, a.Version}, adminPluginCatalogPosition{b.PluginID, b.Version})
		})
		for i := 1; i < len(rows); i++ {
			if rows[i].PluginID == rows[i-1].PluginID && rows[i].Version == rows[i-1].Version {
				return nil, serviceProblem(errors.New("duplicate catalog identity"))
			}
		}
		items := make([]AdminPluginCatalogEntry, 0, in.Limit)
		next := ""
		var last adminPluginCatalogPosition
		for _, row := range rows {
			pos := adminPluginCatalogPosition{row.PluginID, row.Version}
			if in.Cursor != "" && compareAdminPluginCatalogPosition(pos, after) <= 0 {
				continue
			}
			if len(items) == in.Limit {
				next, err = cursors.Encode(scope, last)
				if err != nil {
					return nil, serviceProblem(err)
				}
				break
			}
			item, err := adminPluginCatalogEntryOf(row)
			if err != nil {
				return nil, serviceProblem(err)
			}
			items = append(items, item)
			last = pos
		}
		return &AdminPluginCatalogOutput{Body: Paginated(items, next)}, nil
	})
	Register(reg, op("GET", "installations", "listAdminPluginInstallations", "Read manageable installations with manifest surface, redacted global configuration and bindings. The reserved builtin row is excluded. Every page enumerates the full stored list; continuation is live, not a snapshot."), func(ctx context.Context, in *AdminPluginInstallationsInput) (*AdminPluginInstallationsOutput, error) {
		if reg.deps.AdminPluginInventory == nil {
			return nil, unavailable("plugin inventory")
		}
		scope := CursorScope{OperationID: "listAdminPluginInstallations", Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx) + "/" + viewerScopeDigest(ctx), Filter: strconv.Itoa(in.Limit), Sort: "id", Tiebreaker: "id"}
		var after int
		if in.Cursor != "" {
			if p := cursors.Decode(scope, in.Cursor, &after); p != nil {
				return nil, p
			}
		}
		rows, err := reg.deps.AdminPluginInventory.ListAdminPluginInstallations(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		rows = slices.Clone(rows)
		seen := map[int]bool{}
		for _, r := range rows {
			if r.ID <= 0 || seen[r.ID] || r.Kind == plugins.KindBuiltin {
				return nil, serviceProblem(errors.New("invalid installation identity"))
			}
			seen[r.ID] = true
		}
		slices.SortFunc(rows, func(a, b handlers.PluginInstallationView) int { return cmp.Compare(a.ID, b.ID) })
		items := make([]AdminPluginInstallation, 0, in.Limit)
		next := ""
		last := 0
		for _, r := range rows {
			if r.ID <= after {
				continue
			}
			if len(items) == in.Limit {
				next, err = cursors.Encode(scope, last)
				if err != nil {
					return nil, serviceProblem(err)
				}
				break
			}
			item, err := adminPluginInstallationOf(r)
			if err != nil {
				return nil, serviceProblem(err)
			}
			items = append(items, item)
			last = r.ID
		}
		return &AdminPluginInstallationsOutput{Body: Paginated(items, next)}, nil
	})
}
