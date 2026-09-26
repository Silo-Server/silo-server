package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/models"
)

type adminAccountRepository interface {
	GetAdminSnapshot(context.Context, int) (auth.AdminUserSnapshot, error)
	MutateAdminAccount(context.Context, int, int64, *models.UpdateUserInput, func(*models.User, pgx.Tx) (bool, error)) (auth.AdminUserSnapshot, error)
}
type AdminAccountView struct {
	User          AdminUserView
	Revision      int64
	GroupRevision int64
}
type AdminProfileView struct{ ID, Name string }

func (h *AdminHandler) AdminAccountCapabilities() (bool, bool) {
	_, ok := h.userRepo.(adminAccountRepository)
	return ok && h.pool != nil && h.accountProvisioner != nil, h.accountProvisioner != nil && h.accountProvisioner.SupportsTransactionalProfiles()
}
func (h *AdminHandler) GetAdminAccount(ctx context.Context, id int) (AdminAccountView, error) {
	repo, ok := h.userRepo.(adminAccountRepository)
	if !ok {
		return AdminAccountView{}, apiError(501, "capability_unsupported", "Guarded account management is unavailable")
	}
	snapshot, err := repo.GetAdminSnapshot(ctx, id)
	if err != nil {
		return AdminAccountView{}, err
	}
	group, groupRevision, err := h.adminAccountGroup(ctx, snapshot.User)
	if err != nil {
		return AdminAccountView{}, err
	}
	return AdminAccountView{User: toAdminUserResponse(snapshot.User, group), Revision: snapshot.Revision, GroupRevision: groupRevision}, nil
}
func (h *AdminHandler) adminAccountGroup(ctx context.Context, user *models.User) (*access.GroupPolicy, int64, error) {
	if user.Role == roleAdmin || user.AccessGroupID == nil {
		return nil, 0, nil
	}
	if h.AccessGroups == nil {
		return nil, 0, apiError(409, "capability_not_configured", "Access groups are not configured")
	}
	group, err := h.AccessGroups.Get(ctx, *user.AccessGroupID)
	if err != nil {
		return nil, 0, err
	}
	return new(group.Policy()), group.Revision, nil
}
func adminAccountTransactionGroup(ctx context.Context, tx pgx.Tx, user *models.User) (*access.GroupPolicy, int64, error) {
	if user.Role == roleAdmin || user.AccessGroupID == nil {
		return nil, 0, nil
	}
	group, err := access.ReadGroupInTransaction(ctx, tx, *user.AccessGroupID)
	if err != nil {
		return nil, 0, err
	}
	return new(group.Policy()), group.Revision, nil
}
func validateAdminPassword(password string) error {
	if utf8.RuneCountInString(password) < 8 || len(password) > 72 {
		return fieldError("password", "Use at least 8 characters and at most 72 UTF-8 bytes")
	}
	return nil
}

func validateAdminIdentity(username, email, role string) error {
	if username == "" || email == "" {
		return apiError(422, "validation_failed", "Username and email are required")
	}
	if role != roleAdmin && role != models.RoleUser {
		return fieldError("role", "Role must be admin or user")
	}
	return nil
}
func (h *AdminHandler) validateAdminGroup(ctx context.Context, tx pgx.Tx, id *int64, role string) error {
	if id == nil {
		return nil
	}
	if *id <= 0 || role == roleAdmin {
		return fieldError("access_group_id", "Only ordinary accounts may belong to a valid access group")
	}
	if h.AccessGroups == nil {
		return apiError(409, "capability_not_configured", "Access groups are not configured")
	}
	_, err := access.ReadGroupInTransaction(ctx, tx, *id)
	if errors.Is(err, access.ErrGroupNotFound) {
		return fieldError("access_group_id", "Access group does not exist")
	}
	return err
}
func (h *AdminHandler) CreateAdminAccount(ctx context.Context, input auth.CreateAccountInput) (int, error) {
	if ready, _ := h.AdminAccountCapabilities(); !ready {
		return 0, apiError(501, "capability_unsupported", "Transactional account management is unavailable")
	}
	input.User.Username = auth.NormalizeUsername(input.User.Username)
	input.User.Email = auth.NormalizeEmail(input.User.Email)
	if err := validateAdminIdentity(input.User.Username, input.User.Email, input.User.Role); err != nil {
		return 0, err
	}
	if err := validateAdminPassword(input.User.Password); err != nil {
		return 0, err
	}
	if actorIsScopedAPIKey(ctx) && input.User.Role == roleAdmin {
		return 0, apiError(403, "insufficient_scope", "A scoped API key may not create an admin account")
	}
	if input.User.MaxProfiles != nil && *input.User.MaxProfiles < 1 {
		return 0, fieldError("max_profiles", "Must be at least 1")
	}
	if err := validateStreamLimits(input.User.MaxStreams, input.User.MaxTranscodes); err != nil {
		return 0, fieldError("max_streams", err.Error())
	}
	if input.User.MaxRemoteStreamBitrateKbps != nil && *input.User.MaxRemoteStreamBitrateKbps < 0 {
		return 0, fieldError("max_remote_stream_bitrate_kbps", "Must be 0 (unlimited) or positive")
	}
	if input.User.MaxLocalStreamBitrateKbps != nil && *input.User.MaxLocalStreamBitrateKbps < 0 {
		return 0, fieldError("max_local_stream_bitrate_kbps", "Must be 0 (unlimited) or positive")
	}
	if input.User.MaxPlaybackQuality != nil {
		value, ok := access.ParsePlaybackQualityPreset(*input.User.MaxPlaybackQuality)
		if !ok {
			return 0, fieldError("max_playback_quality", "Invalid playback quality")
		}
		input.User.MaxPlaybackQuality = new(value)
	}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = tx.Exec(ctx, `LOCK TABLE access_groups IN SHARE MODE`); err != nil {
		return 0, err
	}
	if err = h.validateAdminGroup(ctx, tx, input.User.AccessGroupID, input.User.Role); err != nil {
		return 0, err
	}
	user, err := h.accountProvisioner.CreateAccountInTransaction(ctx, tx, input)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	h.invalidateStats(ctx, cache.ChannelAdmin, cache.EventAdminStatsInvalidated, strconv.Itoa(user.ID))
	return user.ID, nil
}
func (h *AdminHandler) UpdateAdminAccount(ctx context.Context, id int, revision, groupRevision int64, input models.UpdateUserInput) (int64, error) {
	repo, ok := h.userRepo.(adminAccountRepository)
	if !ok {
		return 0, apiError(501, "capability_unsupported", "Guarded account management is unavailable")
	}
	if input.Password != nil {
		if err := validateAdminPassword(*input.Password); err != nil {
			return 0, err
		}
	}
	if input.Username != nil {
		value := auth.NormalizeUsername(*input.Username)
		if value == "" {
			return 0, fieldError("username", "Username is required")
		}
		input.Username = new(value)
	}
	if input.Email != nil {
		value := auth.NormalizeEmail(*input.Email)
		if value == "" {
			return 0, fieldError("email", "Email is required")
		}
		input.Email = new(value)
	}
	if input.Role != nil && *input.Role != roleAdmin && *input.Role != models.RoleUser {
		return 0, fieldError("role", "Role must be admin or user")
	}
	if input.MaxProfiles != nil && *input.MaxProfiles < 1 {
		return 0, fieldError("max_profiles", "Must be at least 1")
	}
	if err := validateStreamLimits(input.MaxStreams.Value, input.MaxTranscodes.Value); err != nil {
		return 0, fieldError("max_streams", err.Error())
	}
	if input.MaxRemoteStreamBitrateKbps.Value != nil && *input.MaxRemoteStreamBitrateKbps.Value < 0 {
		return 0, fieldError("max_remote_stream_bitrate_kbps", "Must be 0 (unlimited) or positive")
	}
	if input.MaxLocalStreamBitrateKbps.Value != nil && *input.MaxLocalStreamBitrateKbps.Value < 0 {
		return 0, fieldError("max_local_stream_bitrate_kbps", "Must be 0 (unlimited) or positive")
	}
	if input.MaxPlaybackQuality.Value != nil {
		value, ok := access.ParsePlaybackQualityPreset(*input.MaxPlaybackQuality.Value)
		if !ok {
			return 0, fieldError("max_playback_quality", "Invalid playback quality")
		}
		input.MaxPlaybackQuality.Value = new(value)
	}
	revoked := false
	snapshot, err := repo.MutateAdminAccount(ctx, id, revision, &input, func(current *models.User, tx pgx.Tx) (bool, error) {
		if revision != -1 {
			_, actual, err := adminAccountTransactionGroup(ctx, tx, current)
			if err != nil {
				return false, err
			}
			if actual != groupRevision {
				return false, auth.ErrAdminUserRevision
			}
		}
		if err := auth.CheckOwnerUpdate(actorUserID(ctx), current, input); err != nil {
			return false, ownerError(err)
		}
		role := current.Role
		if input.Role != nil {
			role = *input.Role
		}
		if actorIsScopedAPIKey(ctx) && ((input.Role != nil && role == roleAdmin) || (current.Role == roleAdmin && (input.Password != nil || input.Role != nil))) {
			return false, apiError(403, "insufficient_scope", "A scoped API key may not change admin credentials or grant admin")
		}
		// Only local password sign-in can run the change a temporary password
		// demands; an externally managed account would be locked out.
		if input.PasswordChangeRequired && !current.LocalPasswordLoginEnabled {
			return false, apiError(409, "password_login_disabled", "This account does not use local password sign-in, so its password cannot be made temporary")
		}
		if input.AccessGroupID.Set {
			if err := h.validateAdminGroup(ctx, tx, input.AccessGroupID.Value, role); err != nil {
				return false, err
			}
		}
		revoked = updateRequiresSessionRevocation(current, input)
		return revoked, nil
	})
	if err != nil {
		return 0, err
	}
	if revoked && h.OnUserSessionsRevoked != nil {
		h.OnUserSessionsRevoked(ctx, id)
	}
	return snapshot.Revision, nil
}
func (h *AdminHandler) DeleteAdminAccount(ctx context.Context, id int, revision, groupRevision int64) error {
	repo, ok := h.userRepo.(adminAccountRepository)
	if !ok {
		return apiError(501, "capability_unsupported", "Guarded account management is unavailable")
	}
	_, err := repo.MutateAdminAccount(ctx, id, revision, nil, func(current *models.User, tx pgx.Tx) (bool, error) {
		if revision != -1 {
			_, actual, err := adminAccountTransactionGroup(ctx, tx, current)
			if err != nil {
				return false, err
			}
			if actual != groupRevision {
				return false, auth.ErrAdminUserRevision
			}
		}
		if err := auth.CheckOwnerDelete(actorUserID(ctx), current); err != nil {
			return false, ownerError(err)
		}
		return true, nil
	})
	if err != nil {
		return err
	}
	if h.OnUserSessionsRevoked != nil {
		h.OnUserSessionsRevoked(ctx, id)
	}
	h.invalidateStats(ctx, cache.ChannelAdmin, cache.EventAdminStatsInvalidated, strconv.Itoa(id))
	return nil
}
func (h *AdminHandler) ImpersonateAdminAccount(ctx context.Context, id int, deviceName, ip string) (TokenPairView, error) {
	claims := apimw.GetClaims(ctx)
	if claims == nil || claims.TokenType == auth.TokenTypeAPIKey || claims.SessionID == "" {
		return TokenPairView{}, apiError(http.StatusForbidden, "impersonation_not_allowed", "Impersonation is not allowed")
	}
	if h.ImpersonationService == nil {
		return TokenPairView{}, apiError(409, "capability_not_configured", "Impersonation is not configured")
	}
	pair, actor, user, err := h.ImpersonationService.StartImpersonation(auth.WithClaims(ctx, claims), claims.UserID, id, deviceName, ip)
	if err != nil {
		return TokenPairView{}, err
	}
	return TokenPairView(buildLoginResponse(pair, user, effectiveDownloadAllowed(ctx, user, h.groupPolicyProvider()), actor)), nil
}
func (h *AdminHandler) ListAdminAccountProfiles(ctx context.Context, id int) ([]AdminProfileView, error) {
	if _, err := h.userRepo.GetByID(ctx, id); err != nil {
		return nil, err
	}
	if h.storeProv == nil {
		return nil, apiError(409, "capability_not_configured", "Profile storage is not configured")
	}
	store, err := h.storeProv.ForUser(ctx, id)
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, auth.ErrNotFound
	}
	rows, err := store.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]AdminProfileView, 0, len(rows))
	for _, row := range rows {
		result = append(result, AdminProfileView{ID: row.ID, Name: strings.TrimSpace(row.Name)})
	}
	return result, nil
}
