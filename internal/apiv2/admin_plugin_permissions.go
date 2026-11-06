package apiv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

// AdminPluginPermissionsService is the slice of *handlers.PluginHandler the
// plugin permission operations use. Every method refuses the reserved builtin
// row and reports an unknown installation as plugins.ErrInstallationNotFound.
// ApproveAdminPluginPermissions runs check inside the approval's transaction
// and returns its error unchanged.
type AdminPluginPermissionsService interface {
	GetAdminPluginPermissions(context.Context, int) (*plugins.PluginPermissions, error)
	ApproveAdminPluginPermissions(context.Context, int, plugins.ApprovalCheck) (*plugins.PluginPermissions, error)
	AdminPluginPermissionsPending(context.Context) (map[int]bool, error)
}

// AdminPluginEventAccess is the event access an installation asks for and
// has approved.
type AdminPluginEventAccess struct {
	Required []string `json:"required" enum:"admin,user_data" doc:"Access classes of the events the plugin subscribes to that need approval: admin (server operations such as playback activity, scans and jobs) and user_data (one profile's personal data, such as its notifications). Public and plugin events need none. The vocabulary is open: ignore a class you don't know."`
	Approved []string `json:"approved" enum:"admin,user_data" doc:"Access classes an administrator approved. The plugin receives events of these classes and of no others that need approval."`
}

// AdminPluginHostAPIScopes is the API key scopes an installation asks for
// and has approved.
type AdminPluginHostAPIScopes struct {
	Requested   []string `json:"requested" doc:"The manifest's host_api_scopes"`
	Required    []string `json:"required" doc:"The requested scopes this server can issue"`
	Unsupported []string `json:"unsupported" doc:"The requested scopes this server doesn't know; they are never granted"`
	Approved    []string `json:"approved" doc:"Scopes the plugin's usable host API key carries; empty while none is usable, including after the key was revoked or its account lost the admin role"`
}

// AdminPluginHostAPIKey describes the key issued to a plugin, never the key
// itself.
type AdminPluginHostAPIKey struct {
	Issued      bool `json:"issued" doc:"True while a host API key is issued to the plugin"`
	Usable      bool `json:"usable" doc:"True while the issued key can act for the plugin. False when none is issued, when the account it acts as is no longer an enabled administrator, or when this server can't read it (it was stored under another SECRET_KEY). An unusable key grants nothing: the plugin is pending and an approval issues a replacement"`
	ID          *ID  `json:"id,omitempty" doc:"The key on the API keys page, where it can be revoked; omitted while none is issued"`
	OwnerUserID *ID  `json:"owner_user_id,omitempty" doc:"The account the key acts as: the administrator whose approval issued it. It stops working if that account loses the admin role or is disabled, and is deleted with the account. Omitted while none is issued"`
}

// AdminPluginPermissions is what one installation receives and may call: what
// it asks for, what an administrator approved, and the key it was issued.
type AdminPluginPermissions struct {
	InstallationID   ID                       `json:"installation_id"`
	EventAccess      AdminPluginEventAccess   `json:"event_access"`
	HostAPIScopes    AdminPluginHostAPIScopes `json:"host_api_scopes"`
	Pending          bool                     `json:"pending" doc:"True while the plugin asks for event access or host API scopes that aren't approved. It keeps what was approved before until an administrator approves the rest"`
	ApprovedAt       *Instant                 `json:"approved_at,omitempty" doc:"The last explicit approval; omitted when none was given, such as when event access was approved by installing the plugin"`
	ApprovedByUserID *ID                      `json:"approved_by_user_id,omitempty" doc:"The administrator who gave the last explicit approval; omitted when none was given or the account was deleted"`
	HostAPIKey       AdminPluginHostAPIKey    `json:"host_api_key"`
}
type AdminPluginPermissionsOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   AdminPluginPermissions
}

// AdminPluginPermissionsInput is the input of the conditional read and the
// guarded approval.
type AdminPluginPermissionsInput struct {
	AdminPluginInstallationIDInput
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}

// adminPluginPermissionsTag validates the whole permissions representation,
// so an approval sent with it approves exactly what the administrator read:
// a plugin update that asks for more, or another administrator's approval,
// changes it. A digest rather than a revision: the same requirements and
// grant read again are the same thing to approve.
func adminPluginPermissionsTag(ctx context.Context, body AdminPluginPermissions) (EntityTag, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return EntityTag{}, err
	}
	digest := sha256.Sum256(raw)
	return RenderETag("admin-plugin-permissions:"+strconv.Itoa(claimsFrom(ctx).UserID)+":"+profileFrom(ctx)+":"+viewerScopeDigest(ctx),
		string(body.InstallationID)+":"+hex.EncodeToString(digest[:]), 1), nil
}

func adminPluginPermissionsOf(p *plugins.PluginPermissions) AdminPluginPermissions {
	out := AdminPluginPermissions{
		InstallationID: IDFromInt(int64(p.InstallationID)),
		EventAccess:    AdminPluginEventAccess{Required: NonNil(p.Required.EventAccess), Approved: []string{}},
		HostAPIScopes: AdminPluginHostAPIScopes{Requested: NonNil(p.Required.RequestedScopes), Required: NonNil(p.Required.RequiredScopes),
			Unsupported: NonNil(p.Required.UnsupportedScopes), Approved: NonNil(p.Grant.EffectiveHostAPIScopes())},
		Pending: p.Pending,
	}
	grant := p.Grant
	if grant == nil {
		return out
	}
	out.EventAccess.Approved = NonNil(grant.ApprovedEventAccess)
	if grant.ApprovedAt != nil {
		out.ApprovedAt = new(NewInstant(*grant.ApprovedAt))
	}
	if grant.ApprovedByUserID != nil {
		out.ApprovedByUserID = new(IDFromInt(int64(*grant.ApprovedByUserID)))
	}
	if grant.APIKeyID != nil {
		out.HostAPIKey.Issued = true
		out.HostAPIKey.Usable = grant.HostAPIKeyUsable()
		out.HostAPIKey.ID = new(IDFromInt(*grant.APIKeyID))
		if grant.APIKeyOwnerUserID != nil {
			out.HostAPIKey.OwnerUserID = new(IDFromInt(int64(*grant.APIKeyOwnerUserID)))
		}
	}
	return out
}

func adminPluginPermissionsOutput(ctx context.Context, p *plugins.PluginPermissions) (*AdminPluginPermissionsOutput, EntityTag, error) {
	body := adminPluginPermissionsOf(p)
	tag, err := adminPluginPermissionsTag(ctx, body)
	if err != nil {
		return nil, EntityTag{}, err
	}
	return &AdminPluginPermissionsOutput{ETag: tag.String(), Body: body}, tag, nil
}

// adminPluginPermissionsPending reports whether one installation asks for
// permissions that aren't approved; false when the service isn't wired.
func (reg *Registry) adminPluginPermissionsPending(ctx context.Context, installationID int) (bool, error) {
	if reg.deps.AdminPluginPermissions == nil {
		return false, nil
	}
	p, err := reg.deps.AdminPluginPermissions.GetAdminPluginPermissions(ctx, installationID)
	if err != nil {
		return false, err
	}
	return p.Pending, nil
}

func registerAdminPluginPermissions(reg *Registry) {
	op := func(method, path, id, summary string, safety RetrySafety) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/plugins/installations/{id}/"+path, id, "admin-plugins", summary), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, RetrySafety: safety}
		o.Errors = []int{http.StatusNotFound, http.StatusConflict}
		return o
	}
	read := op(http.MethodGet, "permissions", "getAdminPluginInstallationPermissions", "Read what one installation asks to receive and call, and what an administrator approved: the event access classes its subscriptions need, the host API scopes its manifest requests, and the host API key issued for them. The key itself is never returned. The ETag validates the whole representation; approving requires it. The reserved builtin row is 409.", "")
	read.Conditional = true
	Register(reg, read, func(ctx context.Context, in *AdminPluginPermissionsInput) (*AdminPluginPermissionsOutput, error) {
		if reg.deps.AdminPluginPermissions == nil {
			return nil, unavailable("plugin permissions")
		}
		id, p := adminPluginInstallationID(in.ID)
		if p != nil {
			return nil, p
		}
		permissions, err := reg.deps.AdminPluginPermissions.GetAdminPluginPermissions(ctx, id)
		if err != nil {
			return nil, adminPluginMutationProblem(err)
		}
		out, tag, err := adminPluginPermissionsOutput(ctx, permissions)
		if err != nil {
			return nil, serviceProblem(err)
		}
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
	approve := op(http.MethodPut, "permissions/approval", "approveAdminPluginInstallationPermissions", "Approve everything the installation asks for now, as the acting administrator. If-Match must carry the ETag of the permissions read the administrator approved; the server compares it with the permissions inside the approval's transaction, so a plugin update that asks for more, or another approval, landing in between is 412 with the current ETag and changes nothing. When the installation requests host API scopes this server knows and has no usable key with exactly those, a key acting as the acting administrator is issued and the previous key revoked in the same transaction; when it requests none, its key is revoked. The event subscriber index rebuilds on every server; the plugin is not restarted.", RetrySafetyNaturalIdempotent)
	approve.Guarded = true
	Register(reg, approve, func(ctx context.Context, in *AdminPluginPermissionsInput) (*AdminPluginPermissionsOutput, error) {
		if reg.deps.AdminPluginPermissions == nil {
			return nil, unavailable("plugin permissions")
		}
		id, p := adminPluginInstallationID(in.ID)
		if p != nil {
			return nil, p
		}
		check := func(current *plugins.PluginPermissions) error {
			tag, err := adminPluginPermissionsTag(ctx, adminPluginPermissionsOf(current))
			if err != nil {
				return err
			}
			if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
				return p
			}
			return nil
		}
		permissions, err := reg.deps.AdminPluginPermissions.ApproveAdminPluginPermissions(ctx, id, check)
		if err != nil {
			return nil, adminPluginMutationProblem(err)
		}
		out, _, err := adminPluginPermissionsOutput(ctx, permissions)
		if err != nil {
			return nil, serviceProblem(err)
		}
		return out, nil
	})
}
