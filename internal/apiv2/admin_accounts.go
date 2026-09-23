package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
)

type AdminAccountService interface {
	AdminAccountCapabilities() (bool, bool)
	GetAdminAccount(context.Context, int) (handlers.AdminAccountView, error)
	CreateAdminAccount(context.Context, auth.CreateAccountInput) (int, error)
	UpdateAdminAccount(context.Context, int, int64, int64, models.UpdateUserInput) (int64, error)
	DeleteAdminAccount(context.Context, int, int64, int64) error
	ImpersonateAdminAccount(context.Context, int, string, string) (handlers.TokenPairView, error)
	ListAdminAccountProfiles(context.Context, int) ([]handlers.AdminProfileView, error)
}
type AdminAccountInput struct {
	ID          ID     `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminAccountOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   AdminUser
}
type AdminAccountSaved struct {
	ETag string `header:"ETag"`
}
type AdminAccountCreated struct {
	Location string `header:"Location"`
	Body     struct {
		ID ID `json:"id"`
	}
}
type AdminAccountCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminAccountCapabilitiesOutputBody
}

type AdminAccountCapabilitiesOutputBody struct {
	Capability
	Available            bool `json:"available"`
	GuardedConfiguration bool `json:"guarded_configuration"`
	DefaultProfile       bool `json:"default_profile"`
	ExactIdentityFilter  bool `json:"exact_identity_filter"`
	AccessGroups         bool `json:"access_groups"`
}

type AdminAccountPolicyInput struct {
	LibraryIDs                 []ID    `json:"library_ids,omitempty" nullable:"true"`
	MaxPlaybackQuality         *string `json:"max_playback_quality,omitempty" nullable:"true"`
	MaxStreams                 *int    `json:"max_streams,omitempty" nullable:"true" minimum:"0"`
	MaxTranscodes              *int    `json:"max_transcodes,omitempty" nullable:"true" minimum:"0"`
	MaxRemoteStreamBitrateKbps *int    `json:"max_remote_stream_bitrate_kbps,omitempty" nullable:"true" minimum:"0"`
	MaxLocalStreamBitrateKbps  *int    `json:"max_local_stream_bitrate_kbps,omitempty" nullable:"true" minimum:"0"`
	TranscodeAllowed           *bool   `json:"transcode_allowed,omitempty" nullable:"true"`
	AudioTranscodeAllowed      *bool   `json:"audio_transcode_allowed,omitempty" nullable:"true"`
	DownloadAllowed            *bool   `json:"download_allowed,omitempty" nullable:"true"`
	DownloadTranscodeAllowed   *bool   `json:"download_transcode_allowed,omitempty" nullable:"true"`
	RequestsAllowed            *bool   `json:"requests_allowed,omitempty" nullable:"true"`
	AccessGroupID              *ID     `json:"access_group_id,omitempty" nullable:"true"`
}
type AdminAccountCreateBody struct {
	AdminAccountPolicyInput
	Username             string       `json:"username" minLength:"1" maxLength:"255"`
	Email                string       `json:"email" minLength:"1" maxLength:"320"`
	Password             string       `json:"password" minLength:"8" maxLength:"72"`
	Role                 string       `json:"role" enum:"admin,user"`
	Permissions          []Permission `json:"permissions,omitempty"`
	MaxProfiles          *int         `json:"max_profiles,omitempty" minimum:"1"`
	CreateDefaultProfile bool         `json:"create_default_profile"`
	DefaultProfileName   string       `json:"default_profile_name,omitempty" maxLength:"100"`
}
type AdminAccountUpdateBody struct {
	AdminAccountPolicyInput
	Username    *string      `json:"username,omitempty" minLength:"1" maxLength:"255"`
	Email       *string      `json:"email,omitempty" minLength:"1" maxLength:"320"`
	Password    *string      `json:"password,omitempty" minLength:"8" maxLength:"72"`
	Role        *string      `json:"role,omitempty" enum:"admin,user"`
	Permissions []Permission `json:"permissions,omitempty"`
	MaxProfiles *int         `json:"max_profiles,omitempty" minimum:"1"`
	Enabled     *bool        `json:"enabled,omitempty"`
}
type AdminAccountCreateInput struct {
	Body    AdminAccountCreateBody
	RawBody []byte
}
type AdminAccountUpdateInput struct {
	ID          ID     `path:"id"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        AdminAccountUpdateBody
	RawBody     []byte
}
type AdminAccountImpersonateInput struct {
	ID        ID     `path:"id"`
	UserAgent string `header:"User-Agent"`
}
type AdminAccountProfile struct {
	ID   ID     `json:"id"`
	Name string `json:"name"`
}
type AdminAccountProfilesOutput struct {
	Body Collection[AdminAccountProfile]
}

var adminAccountNullable = map[string]bool{groupLibraryIDsField: true, "max_playback_quality": true, "max_streams": true, "max_transcodes": true, "max_remote_stream_bitrate_kbps": true, "max_local_stream_bitrate_kbps": true, "transcode_allowed": true, "audio_transcode_allowed": true, "download_allowed": true, "download_transcode_allowed": true, "requests_allowed": true, "access_group_id": true}

func adminAccountID(id ID) (int, *Problem) {
	n, err := strconv.Atoi(string(id))
	if err != nil || n <= 0 || strconv.Itoa(n) != string(id) {
		return 0, NewProblem(TypeValidationFailed, "Expected a positive account identifier.")
	}
	return n, nil
}
func adminAccountTag(ctx context.Context, id int, revision, groupRevision int64) EntityTag {
	return collectionEditorTag(ctx, "admin-account/group/"+strconv.FormatInt(groupRevision, 10), strconv.Itoa(id), revision)
}
func adminAccountError(err error) *Problem {
	switch {
	case errors.Is(err, auth.ErrNotFound):
		return NewProblem(TypeNotFound, "Account not found.")
	case errors.Is(err, auth.ErrTransactionalProfileUnavailable):
		return NewProblem(TypeCapabilityUnsupported, "The selected profile store cannot provision atomically.")
	case errors.Is(err, auth.ErrDuplicate):
		return NewProblem(TypeConflict, "Username or email already exists.")
	case errors.Is(err, auth.ErrAdminUserRevision):
		return NewProblem(TypePreconditionFailed, "The account changed. Reload before saving.")
	case errors.Is(err, auth.ErrAlreadyImpersonating):
		return NewProblem(TypeConflict, "Already impersonating an account.")
	case errors.Is(err, auth.ErrImpersonationNotAllowed):
		return NewProblem(TypePermissionDenied, "Impersonation is not allowed.")
	}
	return serviceProblem(err)
}
func adminAccountOperation(method, path, id string, guard bool) Operation {
	op := Operation{Operation: humaOp(method, Prefix+"/admin/users"+path, id, "admin-users", "Manage login accounts and their household configuration."), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, Guarded: guard}
	if method != http.MethodGet {
		op.RetrySafety = RetrySafetyNonRetryable
	}
	return op
}
func (reg *Registry) adminAccounts() (AdminAccountService, *Problem) {
	if reg.deps.AdminAccounts == nil {
		return nil, unavailable("account administration")
	}
	return reg.deps.AdminAccounts, nil
}
func (reg *Registry) adminAccountGuard(ctx context.Context, in AdminAccountInput) (int, int64, int64, *Problem) {
	svc, p := reg.adminAccounts()
	if p != nil {
		return 0, 0, 0, p
	}
	id, p := adminAccountID(in.ID)
	if p != nil {
		return 0, 0, 0, p
	}
	snapshot, err := svc.GetAdminAccount(ctx, id)
	if err != nil {
		return 0, 0, 0, adminAccountError(err)
	}
	if p = EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, adminAccountTag(ctx, id, snapshot.Revision, snapshot.GroupRevision)); p != nil {
		return 0, 0, 0, p
	}
	revision := snapshot.Revision
	if strings.TrimSpace(in.IfMatch) == "*" {
		revision = -1
	}
	return id, revision, snapshot.GroupRevision, nil
}
func (b AdminAccountPolicyInput) model(raw []byte) (models.UpdateUserInput, *Problem) {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	var libraries *[]int
	if b.LibraryIDs != nil {
		values := make([]int, 0, len(b.LibraryIDs))
		for _, id := range b.LibraryIDs {
			n, p := adminAccountID(id)
			if p != nil {
				return models.UpdateUserInput{}, p
			}
			values = append(values, n)
		}
		libraries = &values
	}
	var group *int64
	if b.AccessGroupID != nil {
		n, p := adminAccountID(*b.AccessGroupID)
		if p != nil {
			return models.UpdateUserInput{}, p
		}
		group = new(int64(n))
	}
	present := func(k string) bool { _, ok := m[k]; return ok }
	return models.UpdateUserInput{
		LibraryIDs:                 models.Optional[[]int]{Set: present(groupLibraryIDsField), Value: libraries},
		MaxPlaybackQuality:         models.Optional[string]{Set: present("max_playback_quality"), Value: b.MaxPlaybackQuality},
		MaxStreams:                 models.Optional[int]{Set: present("max_streams"), Value: b.MaxStreams},
		MaxTranscodes:              models.Optional[int]{Set: present("max_transcodes"), Value: b.MaxTranscodes},
		MaxRemoteStreamBitrateKbps: models.Optional[int]{Set: present("max_remote_stream_bitrate_kbps"), Value: b.MaxRemoteStreamBitrateKbps},
		MaxLocalStreamBitrateKbps:  models.Optional[int]{Set: present("max_local_stream_bitrate_kbps"), Value: b.MaxLocalStreamBitrateKbps},
		TranscodeAllowed:           models.Optional[bool]{Set: present("transcode_allowed"), Value: b.TranscodeAllowed},
		AudioTranscodeAllowed:      models.Optional[bool]{Set: present("audio_transcode_allowed"), Value: b.AudioTranscodeAllowed},
		DownloadAllowed:            models.Optional[bool]{Set: present("download_allowed"), Value: b.DownloadAllowed},
		DownloadTranscodeAllowed:   models.Optional[bool]{Set: present("download_transcode_allowed"), Value: b.DownloadTranscodeAllowed},
		RequestsAllowed:            models.Optional[bool]{Set: present("requests_allowed"), Value: b.RequestsAllowed},
		AccessGroupID:              models.Optional[int64]{Set: present("access_group_id"), Value: group},
	}, nil
}
func registerAdminAccounts(reg *Registry) {
	Register(reg, adminAccountOperation(http.MethodGet, "/capabilities", "getAdminAccountCapabilities", false), func(_ context.Context, _ *CapabilityInput) (*AdminAccountCapabilitiesOutput, error) {
		out := new(AdminAccountCapabilitiesOutput)
		if svc := reg.deps.AdminAccounts; svc != nil {
			out.Body.Available, out.Body.DefaultProfile = svc.AdminAccountCapabilities()
			out.Body.GuardedConfiguration = out.Body.Available
		}
		out.Body.AccessGroups = reg.deps.AdminAccessGroups != nil
		out.Body.ExactIdentityFilter = reg.deps.AdminUsers != nil
		return out, nil
	})
	get := adminAccountOperation(http.MethodGet, "/{id}", "getAdminUser", false)
	get.Conditional = true
	Register(reg, get, func(ctx context.Context, in *AdminAccountInput) (*AdminAccountOutput, error) {
		svc, p := reg.adminAccounts()
		if p != nil {
			return nil, p
		}
		id, p := adminAccountID(in.ID)
		if p != nil {
			return nil, p
		}
		snapshot, err := svc.GetAdminAccount(ctx, id)
		if err != nil {
			return nil, adminAccountError(err)
		}
		tag := adminAccountTag(ctx, id, snapshot.Revision, snapshot.GroupRevision)
		out := &AdminAccountOutput{ETag: tag.String(), Body: adminUserFromView(snapshot.User)}
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
	create := adminAccountOperation(http.MethodPost, "", "createAdminUser", false)
	create.DefaultStatus = 201
	Register(reg, create, reg.createAdminAccount)
	update := adminAccountOperation(http.MethodPut, "/{id}", "updateAdminUser", true)
	update.DefaultStatus = 204
	Register(reg, update, reg.updateAdminAccount)
	del := adminAccountOperation(http.MethodDelete, "/{id}", "deleteAdminUser", true)
	del.DefaultStatus = 204
	Register(reg, del, func(ctx context.Context, in *AdminAccountInput) (*struct{}, error) {
		id, rev, groupRev, p := reg.adminAccountGuard(ctx, *in)
		if p != nil {
			return nil, p
		}
		if err := reg.deps.AdminAccounts.DeleteAdminAccount(ctx, id, rev, groupRev); err != nil {
			return nil, adminAccountError(err)
		}
		return &struct{}{}, nil
	})
	Register(reg, adminAccountOperation(http.MethodPost, "/{id}/impersonate", "impersonateAdminUser", false), func(ctx context.Context, in *AdminAccountImpersonateInput) (*TokenPairOutput, error) {
		svc, p := reg.adminAccounts()
		if p != nil {
			return nil, p
		}
		id, p := adminAccountID(in.ID)
		if p != nil {
			return nil, p
		}
		view, err := svc.ImpersonateAdminAccount(ctx, id, in.UserAgent, clientip.FromContext(ctx))
		if err != nil {
			return nil, adminAccountError(err)
		}
		return &TokenPairOutput{Body: tokenPairFromView(view)}, nil
	})
	Register(reg, adminAccountOperation(http.MethodGet, "/{id}/profiles", "listAdminUserProfiles", false), func(ctx context.Context, in *AdminAccountInput) (*AdminAccountProfilesOutput, error) {
		svc, p := reg.adminAccounts()
		if p != nil {
			return nil, p
		}
		id, p := adminAccountID(in.ID)
		if p != nil {
			return nil, p
		}
		rows, err := svc.ListAdminAccountProfiles(ctx, id)
		if err != nil {
			return nil, adminAccountError(err)
		}
		items := make([]AdminAccountProfile, 0, len(rows))
		for _, row := range rows {
			items = append(items, AdminAccountProfile{ID: ID(row.ID), Name: row.Name})
		}
		return &AdminAccountProfilesOutput{Body: Paginated(items, "")}, nil
	})
}
func (reg *Registry) createAdminAccount(ctx context.Context, in *AdminAccountCreateInput) (*AdminAccountCreated, error) {
	svc, p := reg.adminAccounts()
	if p != nil {
		return nil, p
	}
	if p = rejectNonNullableNulls(in.RawBody, adminAccountNullable); p != nil {
		return nil, p
	}
	policy, p := in.Body.model(in.RawBody)
	if p != nil {
		return nil, p
	}
	if p = reg.rejectUnknownLibraries(ctx, policy.LibraryIDs.Value); p != nil {
		return nil, p
	}
	b := in.Body
	if p := invalidEmailProblem(b.Email); p != nil {
		return nil, p
	}
	var libraries []int
	if policy.LibraryIDs.Value != nil {
		libraries = *policy.LibraryIDs.Value
	}
	permissions := make([]string, 0, len(b.Permissions))
	for _, v := range b.Permissions {
		permissions = append(permissions, string(v))
	}
	if b.Permissions == nil {
		permissions = nil
	}
	id, err := svc.CreateAdminAccount(ctx, auth.CreateAccountInput{User: models.CreateUserInput{Username: b.Username, Email: b.Email, Password: b.Password, Role: b.Role, Permissions: permissions, MaxProfiles: b.MaxProfiles, LibraryIDs: libraries, AccessGroupID: policy.AccessGroupID.Value, MaxPlaybackQuality: b.MaxPlaybackQuality, MaxStreams: b.MaxStreams, MaxTranscodes: b.MaxTranscodes, MaxRemoteStreamBitrateKbps: b.MaxRemoteStreamBitrateKbps, MaxLocalStreamBitrateKbps: b.MaxLocalStreamBitrateKbps, TranscodeAllowed: b.TranscodeAllowed, AudioTranscodeAllowed: b.AudioTranscodeAllowed, DownloadAllowed: b.DownloadAllowed, DownloadTranscodeAllowed: b.DownloadTranscodeAllowed, RequestsAllowed: b.RequestsAllowed}, DefaultProfile: auth.DefaultProfileOptions{Enabled: b.CreateDefaultProfile, Name: b.DefaultProfileName}})
	if err != nil {
		return nil, adminAccountError(err)
	}
	out := &AdminAccountCreated{Location: Prefix + "/admin/users/" + strconv.Itoa(id)}
	out.Body.ID = ID(strconv.Itoa(id))
	return out, nil
}
func (reg *Registry) updateAdminAccount(ctx context.Context, in *AdminAccountUpdateInput) (*AdminAccountSaved, error) {
	id, revision, groupRevision, p := reg.adminAccountGuard(ctx, AdminAccountInput{ID: in.ID, IfMatch: in.IfMatch, IfNoneMatch: in.IfNoneMatch})
	if p != nil {
		return nil, p
	}
	if p = rejectNonNullableNulls(in.RawBody, adminAccountNullable); p != nil {
		return nil, p
	}
	input, p := in.Body.model(in.RawBody)
	if p != nil {
		return nil, p
	}
	if p = reg.rejectUnknownLibraries(ctx, input.LibraryIDs.Value); p != nil {
		return nil, p
	}
	b := in.Body
	if b.Email != nil {
		if p := invalidEmailProblem(*b.Email); p != nil {
			return nil, p
		}
	}
	input.Username = b.Username
	input.Email = b.Email
	input.Password = b.Password
	input.Role = b.Role
	input.Enabled = b.Enabled
	input.MaxProfiles = b.MaxProfiles
	var members map[string]json.RawMessage
	_ = json.Unmarshal(in.RawBody, &members)
	if _, ok := members["permissions"]; ok {
		values := make([]string, 0, len(b.Permissions))
		for _, v := range b.Permissions {
			values = append(values, string(v))
		}
		input.Permissions = &values
	}
	_, err := reg.deps.AdminAccounts.UpdateAdminAccount(ctx, id, revision, groupRevision, input)
	if err != nil {
		return nil, adminAccountError(err)
	}
	return &AdminAccountSaved{}, nil
}

type AdminUserAPIKeysInput struct {
	ID ID `path:"id"`
	LimitParam
	Cursor string `query:"cursor"`
}

func registerAdminUserAPIKeys(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, adminAccountOperation(http.MethodGet, "/{id}/api-keys", "listAdminUserAPIKeys", false), func(ctx context.Context, in *AdminUserAPIKeysInput) (*AdminAPIKeyCollectionOutput, error) {
		svc, p := reg.adminAccounts()
		if p != nil {
			return nil, p
		}
		id, p := adminAccountID(in.ID)
		if p != nil {
			return nil, p
		}
		if _, err := svc.GetAdminAccount(ctx, id); err != nil {
			return nil, adminAccountError(err)
		}
		keys, ok := reg.deps.AdminAPIKeys.(interface {
			ListAdminUserAPIKeysPage(context.Context, int, *auth.APIKeyPageKey, int) ([]handlers.AdminAPIKeyListItem, bool, error)
		})
		if !ok {
			return nil, unavailable("account API keys")
		}
		scope := adminPolicyListScope(ctx, "listAdminUserAPIKeys", strconv.Itoa(id)+"/"+strconv.Itoa(in.Limit), "-created_at,-id", "id")
		var after *auth.APIKeyPageKey
		if in.Cursor != "" {
			after = new(auth.APIKeyPageKey)
			if p := cursors.Decode(scope, in.Cursor, after); p != nil {
				return nil, p
			}
		}
		rows, more, err := keys.ListAdminUserAPIKeysPage(ctx, id, after, in.Limit)
		if err != nil {
			return nil, adminAPIKeyProblem(ctx, err)
		}
		items := make([]AdminAPIKeyListItem, 0, len(rows))
		for _, row := range rows {
			item := AdminAPIKeyListItem{AdminAPIKey: adminAPIKeyOf(&row.APIKeyConfiguration), Username: row.Username}
			if row.LastUsedAt != nil {
				item.LastUsedAt = new(NewInstant(*row.LastUsedAt))
			}
			items = append(items, item)
		}
		next := ""
		if more && len(rows) > 0 {
			last := rows[len(rows)-1]
			next, err = cursors.Encode(scope, auth.APIKeyPageKey{ID: last.ID, CreatedAt: last.CreatedAt})
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &AdminAPIKeyCollectionOutput{Body: Paginated(items, next)}, nil
	})
}

func (c AdminAccountCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}
