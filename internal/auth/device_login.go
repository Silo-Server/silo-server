package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

var (
	ErrDeviceLoginNotFound   = errors.New("device login request not found")
	ErrDeviceLoginExpired    = errors.New("device login request expired")
	ErrDeviceLoginDenied     = errors.New("device login request denied")
	ErrDeviceLoginConsumed   = errors.New("device login request already consumed")
	ErrDeviceLoginUnapproved = errors.New("device login request not approved")
	ErrDeviceLoginBadPurpose = errors.New("invalid device login purpose")
	ErrDeviceLoginPurpose    = errors.New("device login purpose mismatch")
	ErrDeviceLoginConflict   = errors.New("device login already approved by another identity")
	ErrDeviceLoginNoProfile  = errors.New("device login profile not found")
	ErrDeviceLoginCanceled   = errors.New("device login request canceled by the device")
)

const (
	DeviceLoginStatusPending  = "pending"
	DeviceLoginStatusApproved = "approved"
	DeviceLoginStatusDenied   = "denied"
	DeviceLoginStatusConsumed = "consumed"
	DeviceLoginStatusCanceled = "canceled"
	DeviceLoginStatusExpired  = "expired"
	DeviceLoginPurposeLogin   = "device_login"
	DeviceLoginPurposeRemote  = "remote_playback"

	deviceLoginTTL           = 15 * time.Minute
	legacyDeviceLoginTTL     = 10 * time.Minute
	deviceLoginPollInterval  = 3 * time.Second
	remotePlaybackSessionTTL = 24 * time.Hour
	deviceCodeBytes          = 32
	browserCodeBytes         = 32
	userCodeLength           = 8
	userCodeAttempts         = 5
	maxDeviceNameLen         = 120
	maxDevicePlatformLen     = 80
	maxUserAgentLen          = 256

	// An approver lookup holds a pending code at least this long, so the
	// device doesn't replace it while someone is approving it...
	deviceLoginOpenedHold = 5 * time.Minute
	// ...but never past this age, so repeated lookups can't keep a code
	// alive indefinitely.
	deviceLoginMaxLifetime = 30 * time.Minute
)

// User codes are digits: they are typed on phone keypads and read aloud.
// They are unique only among this server's requests; a code means nothing
// without the server that issued it.
var deviceCodeAlphabet = []byte("0123456789")

// withdrawDeviceSignInApprovalsInTransaction prevents an approved device from
// minting a fresh native session after account-wide revocation. Callers lock
// the approving accounts first, as approval and collection do, and commit this
// update together with session revocation. Already-issued playback grants and
// requests approved by other accounts are unchanged.
func withdrawDeviceSignInApprovalsInTransaction(ctx context.Context, tx pgx.Tx, userIDs []int) error {
	if _, err := tx.Exec(ctx, `
		UPDATE device_login_requests SET status = $2, updated_at = NOW()
		WHERE approved_by_user_id = ANY($1::int[]) AND status = $3`,
		userIDs, DeviceLoginStatusDenied, DeviceLoginStatusApproved); err != nil {
		return fmt.Errorf("withdrawing device sign-in approvals: %w", err)
	}
	return nil
}

type DeviceLoginStartInput struct {
	DeviceName     string
	DevicePlatform string
	IPAddress      string
	UserAgent      string
	BaseURL        string
	ClientPurpose  string
	Temporary      bool
	// Legacy keeps the frozen /api/v1 start answer: a 10-minute request
	// whose link carries the browser code (?token=) rather than the user
	// code. The user code is digits either way.
	Legacy bool
}

type DeviceLoginStartResult struct {
	DeviceCode              string
	UserCode                string
	MatchCode               string
	VerificationURI         string
	VerificationURIComplete string
	ExpiresAt               time.Time
	ExpiresIn               int
	Interval                int
	DeviceName              string
	DevicePlatform          string
	ClientPurpose           string
	Temporary               bool
}

type DeviceLoginLookupInput struct {
	BrowserCode string
	UserCode    string
	// MarkOpened lets a lookup of a pending request record opened_at and
	// hold its code (v2 getDeviceLogin). The frozen v1 lookup leaves the row
	// untouched. Only Lookup reads it.
	MarkOpened bool
}

type DeviceLoginInfo struct {
	Status         string
	UserCode       string
	MatchCode      string
	DeviceName     string
	DevicePlatform string
	IPAddressHint  string
	ExpiresAt      time.Time
	// RequestedAt is when the device opened the request.
	RequestedAt   time.Time
	ClientPurpose string
	Temporary     bool
}

type DeviceLoginPollResult struct {
	Status    string
	PollAfter int
	// Opened reports that an approver has looked the pending request up, so
	// the device can say "continue on your phone" and keep its code.
	Opened bool
	// ExpiresAt is the pending request's current expiry, which approver
	// lookups can move later than the start answer said. Zero otherwise.
	ExpiresAt        time.Time
	TokenPair        *TokenPair
	User             *models.User
	ProfileID        string
	ProfileToken     string
	Temporary        bool
	SessionExpiresAt time.Time
}

type deviceLoginRecord struct {
	ID                 string
	DeviceCodeHash     string
	BrowserCodeHash    string
	UserCodeHash       string
	MatchCode          string
	DeviceName         string
	DevicePlatform     string
	IPAddress          string
	RequestedUserAgent string
	Status             string
	ApprovedByUserID   *int
	ApprovedProfileID  *string
	AuthSessionID      *string
	ClientPurpose      string
	Temporary          bool
	ExpiresAt          time.Time
	ApprovedAt         *time.Time
	DeniedAt           *time.Time
	ConsumedAt         *time.Time
	OpenedAt           *time.Time
	CanceledAt         *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// ApprovedIdentityID and ApprovedProviderSince are the provider chain of
	// the session that approved the request, which the device's session
	// continues.
	ApprovedIdentityID    *int64
	ApprovedProviderSince *time.Time
}

type DeviceLoginService struct {
	pool     *pgxpool.Pool
	users    *UserRepository
	jwt      *JWTService
	sessions *SessionRepository
	stores   userstore.UserStoreProvider
	profiles *access.ProfileTokenService
	// newUserCode draws a user code; tests replace it to force a collision.
	newUserCode func() (string, error)
}

const deviceLoginSelectColumns = `
	SELECT id, device_code_hash, browser_code_hash, user_code_hash, match_code,
		device_name, device_platform, COALESCE(host(ip_address), '') AS ip_address, requested_user_agent,
		status, approved_by_user_id, approved_profile_id, auth_session_id,
		client_purpose, temporary, expires_at, approved_at,
		denied_at, consumed_at, opened_at, canceled_at, created_at, updated_at,
		approved_identity_id, approved_provider_since
	FROM device_login_requests
`

func NewDeviceLoginService(
	pool *pgxpool.Pool,
	users *UserRepository,
	jwt *JWTService,
	sessions *SessionRepository,
	stores userstore.UserStoreProvider,
	profiles *access.ProfileTokenService,
) *DeviceLoginService {
	if pool == nil || users == nil || jwt == nil || sessions == nil {
		return nil
	}
	return &DeviceLoginService{
		pool:        pool,
		users:       users,
		jwt:         jwt,
		sessions:    sessions,
		stores:      stores,
		profiles:    profiles,
		newUserCode: randomUserCode,
	}
}

func (s *DeviceLoginService) Start(ctx context.Context, input DeviceLoginStartInput) (*DeviceLoginStartResult, error) {
	purpose, temporary, err := normalizeDeviceLoginPurpose(input.ClientPurpose, input.Temporary)
	if err != nil {
		return nil, err
	}
	// Eight digits leave room for collisions, and user_code_hash is unique
	// across every stored request, so a clash draws fresh codes.
	var (
		deviceCode, userCode, matchCode string
		record                          deviceLoginRecord
	)
	ttl := deviceLoginTTL
	if input.Legacy {
		ttl = legacyDeviceLoginTTL
	}
	var browserCode string
	for attempt := 1; ; attempt++ {
		deviceCode, err = randomToken(deviceCodeBytes)
		if err != nil {
			return nil, fmt.Errorf("generate device code: %w", err)
		}
		// Only the frozen v1 link still carries the browser code; lookups by
		// it keep working for those links and for links issued before user
		// codes moved into the URL.
		browserCode, err = randomToken(browserCodeBytes)
		if err != nil {
			return nil, fmt.Errorf("generate browser code: %w", err)
		}
		userCode, err = s.newUserCode()
		if err != nil {
			return nil, fmt.Errorf("generate user code: %w", err)
		}
		matchCode, err = randomMatchCode()
		if err != nil {
			return nil, fmt.Errorf("generate match code: %w", err)
		}

		now := time.Now().UTC()
		record = deviceLoginRecord{
			ID:                 uuid.New().String(),
			DeviceCodeHash:     hashDeviceLoginSecret(deviceCode),
			BrowserCodeHash:    hashDeviceLoginSecret(browserCode),
			UserCodeHash:       hashDeviceLoginSecret(normalizeUserCode(userCode)),
			MatchCode:          matchCode,
			DeviceName:         fallbackDeviceName(input.DeviceName, input.UserAgent),
			DevicePlatform:     trimDeviceField(input.DevicePlatform, maxDevicePlatformLen),
			IPAddress:          trimDeviceField(input.IPAddress, 64),
			RequestedUserAgent: trimDeviceField(input.UserAgent, maxUserAgentLen),
			Status:             DeviceLoginStatusPending,
			ClientPurpose:      purpose,
			Temporary:          temporary,
			ExpiresAt:          now.Add(ttl),
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		err = s.create(ctx, record)
		if err == nil {
			break
		}
		if !isDuplicateKeyError(err) || attempt >= userCodeAttempts {
			return nil, err
		}
	}

	verificationURI := strings.TrimRight(strings.TrimSpace(input.BaseURL), "/") + "/activate"
	complete := verificationURI + "?code=" + normalizeUserCode(userCode)
	if input.Legacy {
		complete = verificationURI + "?token=" + browserCode
	}
	return &DeviceLoginStartResult{
		DeviceCode:              deviceCode,
		UserCode:                userCode,
		MatchCode:               matchCode,
		VerificationURI:         verificationURI,
		VerificationURIComplete: complete,
		ExpiresAt:               record.ExpiresAt,
		ExpiresIn:               int(ttl.Seconds()),
		Interval:                int(deviceLoginPollInterval.Seconds()),
		DeviceName:              record.DeviceName,
		DevicePlatform:          record.DevicePlatform,
		ClientPurpose:           record.ClientPurpose,
		Temporary:               record.Temporary,
	}, nil
}

func (s *DeviceLoginService) Lookup(ctx context.Context, input DeviceLoginLookupInput) (*DeviceLoginInfo, error) {
	record, userCode, err := s.findByLookup(ctx, input)
	if err != nil {
		return nil, err
	}

	if isDeviceLoginExpired(record) {
		return &DeviceLoginInfo{
			Status:         DeviceLoginStatusExpired,
			UserCode:       userCode,
			MatchCode:      record.MatchCode,
			DeviceName:     record.DeviceName,
			DevicePlatform: record.DevicePlatform,
			IPAddressHint:  maskDeviceIPAddress(record.IPAddress),
			ExpiresAt:      record.ExpiresAt,
			RequestedAt:    record.CreatedAt,
			ClientPurpose:  record.ClientPurpose,
			Temporary:      record.Temporary,
		}, nil
	}

	if input.MarkOpened && record.Status == DeviceLoginStatusPending {
		if err := s.markOpened(ctx, record); err != nil {
			return nil, err
		}
	}

	return &DeviceLoginInfo{
		Status:         record.Status,
		UserCode:       userCode,
		MatchCode:      record.MatchCode,
		DeviceName:     record.DeviceName,
		DevicePlatform: record.DevicePlatform,
		IPAddressHint:  maskDeviceIPAddress(record.IPAddress),
		ExpiresAt:      record.ExpiresAt,
		RequestedAt:    record.CreatedAt,
		ClientPurpose:  record.ClientPurpose,
		Temporary:      record.Temporary,
	}, nil
}

func (s *DeviceLoginService) Approve(ctx context.Context, input DeviceLoginLookupInput, approverUserID int) error {
	record, _, err := s.findByLookup(ctx, input)
	if err != nil {
		return err
	}
	if record.ClientPurpose != DeviceLoginPurposeLogin || record.Temporary {
		return ErrDeviceLoginPurpose
	}
	if err := validateDeviceLoginDecision(record); err != nil {
		return err
	}

	if err := s.validateApprovingUser(ctx, approverUserID); err != nil {
		return err
	}

	if record.Status == DeviceLoginStatusApproved && record.ApprovedByUserID != nil && *record.ApprovedByUserID == approverUserID {
		return nil
	}
	if record.Status == DeviceLoginStatusApproved {
		return ErrDeviceLoginConflict
	}

	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		chain, err := s.approvingProviderChain(ctx, tx, approverUserID)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		tag, err := tx.Exec(ctx, `
			UPDATE device_login_requests
			SET status = $2,
				approved_by_user_id = $3,
				approved_at = $4,
				approved_identity_id = $7,
				approved_provider_since = $8,
				denied_at = NULL,
				updated_at = $4
			WHERE id = $1
				AND status = $5
				AND client_purpose = $6
				AND temporary = FALSE
				AND expires_at > NOW()
		`,
			record.ID,
			DeviceLoginStatusApproved,
			approverUserID,
			now,
			DeviceLoginStatusPending,
			DeviceLoginPurposeLogin,
			chain.identityID,
			chain.providerSince,
		)
		if err != nil {
			return fmt.Errorf("approve device login: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return s.reloadApprovalState(ctx, tx, record.ID, approverUserID, "")
		}
		return nil
	})
}

// ApproveRemotePlayback approves a temporary device-login request for the
// caller's already-resolved viewer profile. The handler must obtain profileID
// from RequireViewerAccess rather than accepting an untrusted body field.
func (s *DeviceLoginService) ApproveRemotePlayback(
	ctx context.Context,
	input DeviceLoginLookupInput,
	approverUserID int,
	profileID string,
) error {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return ErrDeviceLoginNoProfile
	}
	record, _, err := s.findByLookup(ctx, input)
	if err != nil {
		return err
	}
	if record.ClientPurpose != DeviceLoginPurposeRemote || !record.Temporary {
		return ErrDeviceLoginPurpose
	}
	if err := validateDeviceLoginDecision(record); err != nil {
		return err
	}
	if err := s.validateApprovingProfile(ctx, approverUserID, profileID); err != nil {
		return err
	}

	if record.Status == DeviceLoginStatusApproved {
		if sameApprovedIdentity(record, approverUserID, profileID) {
			return nil
		}
		return ErrDeviceLoginConflict
	}

	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		chain, err := s.approvingProviderChain(ctx, tx, approverUserID)
		if err != nil {
			return err
		}

		now := time.Now().UTC()
		tag, err := tx.Exec(ctx, `
			UPDATE device_login_requests
			SET status = $2,
				approved_by_user_id = $3,
				approved_profile_id = $4,
				approved_at = $5,
				approved_identity_id = $8,
				approved_provider_since = $9,
				denied_at = NULL,
				updated_at = $5
			WHERE id = $1
				AND status = $6
				AND client_purpose = $7
				AND temporary = TRUE
				AND expires_at > NOW()
		`,
			record.ID,
			DeviceLoginStatusApproved,
			approverUserID,
			profileID,
			now,
			DeviceLoginStatusPending,
			DeviceLoginPurposeRemote,
			chain.identityID,
			chain.providerSince,
		)
		if err != nil {
			return fmt.Errorf("approve remote playback login: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return s.reloadApprovalState(ctx, tx, record.ID, approverUserID, profileID)
		}
		return nil
	})
}

// approvingProviderChain locks the approving account and its current login
// session before the device row is changed. Provider re-checks take that
// account lock before revoking sessions and withdrawing approvals, so a
// request authenticated before a revocation cannot approve afterwards.
func (s *DeviceLoginService) approvingProviderChain(ctx context.Context, tx pgx.Tx, approverUserID int) (sessionProviderChain, error) {
	user, err := lockUser(ctx, tx, approverUserID)
	if err != nil {
		return sessionProviderChain{}, err
	}
	if !user.Enabled {
		return sessionProviderChain{}, ErrUserDisabled
	}
	claims := ClaimsFromContext(ctx)
	if claims == nil {
		// Trusted internal callers can approve directly as the account.
		return sessionProviderChain{}, nil
	}
	if claims.UserID != approverUserID || !claims.IsOwnLoginSession() {
		return sessionProviderChain{}, ErrSessionRevoked
	}
	return sessionProviderChainOf(ctx, tx, claims.SessionID, approverUserID)
}

// markOpened records the first approver lookup of a pending request and
// holds its code for at least deviceLoginOpenedHold, capped at
// deviceLoginMaxLifetime after creation. It updates record in place.
func (s *DeviceLoginService) markOpened(ctx context.Context, record *deviceLoginRecord) error {
	var expiresAt time.Time
	var openedAt *time.Time
	err := s.pool.QueryRow(ctx, `
		UPDATE device_login_requests
		SET opened_at = COALESCE(opened_at, NOW()),
			expires_at = GREATEST(expires_at, LEAST(NOW() + make_interval(secs => $2), created_at + make_interval(secs => $3))),
			updated_at = NOW()
		WHERE id = $1
			AND status = $4
			AND expires_at > NOW()
		RETURNING expires_at, opened_at
	`,
		record.ID,
		deviceLoginOpenedHold.Seconds(),
		deviceLoginMaxLifetime.Seconds(),
		DeviceLoginStatusPending,
	).Scan(&expiresAt, &openedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Decided, canceled or expired since it was read: report it as read.
		return nil
	}
	if err != nil {
		return fmt.Errorf("mark device login opened: %w", err)
	}
	record.ExpiresAt = expiresAt
	record.OpenedAt = openedAt
	return nil
}

// Cancel withdraws the device's own request, identified by its device code.
// A live pending or approved (not yet collected) request becomes canceled,
// so its code can no longer be approved or collected; any other request is
// left as it is. It returns the request's status afterwards: canceled,
// denied, consumed or expired.
func (s *DeviceLoginService) Cancel(ctx context.Context, deviceCode string) (string, error) {
	hash := hashDeviceLoginSecret(strings.TrimSpace(deviceCode))
	var status string
	err := s.pool.QueryRow(ctx, `
		UPDATE device_login_requests
		SET status = $2,
			canceled_at = NOW(),
			updated_at = NOW()
		WHERE device_code_hash = $1
			AND status IN ($3, $4)
			AND expires_at > NOW()
		RETURNING status
	`,
		hash,
		DeviceLoginStatusCanceled,
		DeviceLoginStatusPending,
		DeviceLoginStatusApproved,
	).Scan(&status)
	if err == nil {
		return status, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("cancel device login: %w", err)
	}
	row := s.pool.QueryRow(ctx, deviceLoginSelectColumns+`
		WHERE device_code_hash = $1
	`, hash)
	record, err := scanDeviceLogin(row)
	if err != nil {
		return "", err
	}
	if isDeviceLoginExpired(record) {
		return DeviceLoginStatusExpired, nil
	}
	return record.Status, nil
}

func (s *DeviceLoginService) Deny(ctx context.Context, input DeviceLoginLookupInput) error {
	record, _, err := s.findByLookup(ctx, input)
	if err != nil {
		return err
	}
	if isDeviceLoginExpired(record) {
		return ErrDeviceLoginExpired
	}
	if record.Status == DeviceLoginStatusConsumed {
		return ErrDeviceLoginConsumed
	}
	if record.Status == DeviceLoginStatusCanceled {
		return ErrDeviceLoginCanceled
	}
	if record.Status == DeviceLoginStatusDenied {
		return nil
	}

	now := time.Now().UTC()
	tag, err := s.pool.Exec(ctx, `
		UPDATE device_login_requests
		SET status = $2,
			approved_by_user_id = NULL,
			approved_profile_id = NULL,
			approved_identity_id = NULL,
			approved_provider_since = NULL,
			approved_at = NULL,
			denied_at = $3,
			updated_at = $3
		WHERE id = $1
			AND status IN ($4, $5)
			AND expires_at > NOW()
	`,
		record.ID,
		DeviceLoginStatusDenied,
		now,
		DeviceLoginStatusPending,
		DeviceLoginStatusApproved,
	)
	if err != nil {
		return fmt.Errorf("deny device login: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return s.reloadDenyState(ctx, record.ID)
	}
	return nil
}

// deviceLoginPollState answers a request that cannot be collected now.
// An approved, live request returns nil so Poll can collect under row locks.
func deviceLoginPollState(record *deviceLoginRecord) (*DeviceLoginPollResult, error) {
	if isDeviceLoginExpired(record) {
		return &DeviceLoginPollResult{Status: DeviceLoginStatusExpired, PollAfter: int(deviceLoginPollInterval.Seconds())}, nil
	}
	switch record.Status {
	case DeviceLoginStatusApproved:
		return nil, nil
	case DeviceLoginStatusPending, DeviceLoginStatusCanceled, DeviceLoginStatusDenied, DeviceLoginStatusConsumed:
		result := &DeviceLoginPollResult{Status: record.Status, PollAfter: int(deviceLoginPollInterval.Seconds())}
		if record.Status == DeviceLoginStatusPending {
			result.Opened = record.OpenedAt != nil
			result.ExpiresAt = record.ExpiresAt
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unexpected device login status %q", record.Status)
	}
}

func (s *DeviceLoginService) Poll(ctx context.Context, deviceCode string) (*DeviceLoginPollResult, error) {
	// The approver is immutable once assigned. Read it before taking the
	// device lock so collection and revocation both lock user -> device;
	// inserting a session while holding only the device row would invert
	// the user foreign-key lock against account-wide revocation.
	preview, err := scanDeviceLogin(s.pool.QueryRow(ctx, deviceLoginSelectColumns+`
		WHERE device_code_hash = $1`, hashDeviceLoginSecret(strings.TrimSpace(deviceCode))))
	if err != nil {
		return nil, err
	}
	if result, err := deviceLoginPollState(preview); result != nil || err != nil {
		return result, err
	}
	if preview.ApprovedByUserID == nil {
		return nil, ErrDeviceLoginUnapproved
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin device login poll: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	user, err := lockUser(ctx, tx, *preview.ApprovedByUserID)
	if err != nil {
		return nil, fmt.Errorf("load approved user: %w", err)
	}

	record, err := s.getByDeviceCodeTx(ctx, tx, deviceCode)
	if err != nil {
		return nil, err
	}

	if result, err := deviceLoginPollState(record); result != nil || err != nil {
		return result, err
	}
	if record.ApprovedByUserID == nil || *record.ApprovedByUserID != user.ID {
		return nil, ErrDeviceLoginUnapproved
	}

	if !user.Enabled {
		return nil, ErrUserDisabled
	}

	profileID := ""
	if record.ClientPurpose == DeviceLoginPurposeRemote {
		if !record.Temporary || record.ApprovedProfileID == nil || strings.TrimSpace(*record.ApprovedProfileID) == "" {
			return nil, ErrDeviceLoginNoProfile
		}
		profileID = strings.TrimSpace(*record.ApprovedProfileID)
		if err := s.validateProfileOwnership(ctx, user.ID, profileID); err != nil {
			return nil, err
		}
	}

	now := time.Now().UTC()
	sessionID := uuid.New().String()
	sessionExpiresAt := now.Add(s.jwt.RefreshExpiry())
	if record.Temporary {
		remoteExpiry := now.Add(remotePlaybackSessionTTL)
		if remoteExpiry.Before(sessionExpiresAt) {
			sessionExpiresAt = remoteExpiry
		}
	}
	session := models.AuthSession{
		ID:     sessionID,
		UserID: user.ID,
		// The name and platform the device started with. Device headers on
		// the poll that collects the session replace them and add the
		// device's id (applyClientDevice).
		DeviceName:     record.DeviceName,
		DevicePlatform: clampClientDeviceValue(record.DevicePlatform, maxClientDevicePlatformLen),
		IPAddress:      record.IPAddress,
		ExpiresAt:      sessionExpiresAt,
		// The device continues the approving session's provider chain, so a
		// provider that cannot re-check it ends the device's session when it
		// would have ended the approver's.
		IdentityID:    record.ApprovedIdentityID,
		ProviderSince: record.ApprovedProviderSince,
	}
	if err := s.sessions.createWithQuerier(ctx, tx, session); err != nil {
		return nil, err
	}

	pair, err := s.generateTokenPair(Claims{
		UserID:                 user.ID,
		Role:                   user.Role,
		SessionID:              sessionID,
		PasswordChangeRequired: user.PasswordChangeRequired,
	})
	if err != nil {
		return nil, err
	}

	profileToken := ""
	if record.ClientPurpose == DeviceLoginPurposeRemote {
		if s.profiles == nil {
			return nil, errors.New("profile token service is not configured")
		}
		profileToken, _, err = s.profiles.Mint(access.ProfileTokenClaims{
			UserID:         user.ID,
			SessionID:      sessionID,
			ProfileID:      profileID,
			PolicyRevision: user.AccessPolicyRevision,
		})
		if err != nil {
			return nil, fmt.Errorf("mint remote playback profile token: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE device_login_requests
		SET status = $2,
			auth_session_id = $3,
			consumed_at = $4,
			updated_at = $4
		WHERE id = $1
	`,
		record.ID,
		DeviceLoginStatusConsumed,
		sessionID,
		now,
	); err != nil {
		return nil, fmt.Errorf("consume device login: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit device login poll: %w", err)
	}

	return &DeviceLoginPollResult{
		Status:           "approved",
		PollAfter:        int(deviceLoginPollInterval.Seconds()),
		TokenPair:        pair,
		User:             user,
		ProfileID:        profileID,
		ProfileToken:     profileToken,
		Temporary:        record.Temporary,
		SessionExpiresAt: sessionExpiresAt,
	}, nil
}

func (s *DeviceLoginService) generateTokenPair(claims Claims) (*TokenPair, error) {
	accessToken, err := s.jwt.generateAccessToken(claims)
	if err != nil {
		return nil, fmt.Errorf("generating access token: %w", err)
	}
	refreshToken, err := s.jwt.generateRefreshToken(claims)
	if err != nil {
		return nil, fmt.Errorf("generating refresh token: %w", err)
	}
	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int(s.jwt.AccessExpiry().Seconds()),
	}, nil
}

func (s *DeviceLoginService) create(ctx context.Context, record deviceLoginRecord) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO device_login_requests (
			id, device_code_hash, browser_code_hash, user_code_hash, match_code,
			device_name, device_platform, ip_address, requested_user_agent,
			status, client_purpose, temporary, expires_at, created_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::inet, $9, $10, $11, $12, $13, $14, $15)
	`,
		record.ID,
		record.DeviceCodeHash,
		record.BrowserCodeHash,
		record.UserCodeHash,
		record.MatchCode,
		record.DeviceName,
		record.DevicePlatform,
		record.IPAddress,
		record.RequestedUserAgent,
		record.Status,
		record.ClientPurpose,
		record.Temporary,
		record.ExpiresAt,
		record.CreatedAt,
		record.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create device login request: %w", err)
	}
	return nil
}

func (s *DeviceLoginService) findByLookup(ctx context.Context, input DeviceLoginLookupInput) (*deviceLoginRecord, string, error) {
	if token := strings.TrimSpace(input.BrowserCode); token != "" {
		record, err := s.getByHash(ctx, "browser_code_hash", hashDeviceLoginSecret(token))
		return record, "", err
	}

	code := normalizeUserCode(input.UserCode)
	if code == "" {
		return nil, "", ErrDeviceLoginNotFound
	}
	record, err := s.getByHash(ctx, "user_code_hash", hashDeviceLoginSecret(code))
	return record, formatUserCode(code), err
}

func (s *DeviceLoginService) getByHash(ctx context.Context, column, hash string) (*deviceLoginRecord, error) {
	var query string
	switch column {
	case "browser_code_hash":
		query = deviceLoginSelectColumns + `
		WHERE browser_code_hash = $1
	`
	case "user_code_hash":
		query = deviceLoginSelectColumns + `
		WHERE user_code_hash = $1
	`
	default:
		return nil, fmt.Errorf("unsupported device login lookup column %q", column)
	}

	row := s.pool.QueryRow(ctx, query, hash)
	record, err := scanDeviceLogin(row)
	if err != nil {
		return nil, err
	}
	return record, nil
}

func (s *DeviceLoginService) getByID(ctx context.Context, db dbQuerier, id string) (*deviceLoginRecord, error) {
	row := db.QueryRow(ctx, deviceLoginSelectColumns+`
		WHERE id = $1
	`, id)
	return scanDeviceLogin(row)
}

func (s *DeviceLoginService) getByDeviceCodeTx(ctx context.Context, tx pgx.Tx, deviceCode string) (*deviceLoginRecord, error) {
	hash := hashDeviceLoginSecret(strings.TrimSpace(deviceCode))
	row := tx.QueryRow(ctx, deviceLoginSelectColumns+`
		WHERE device_code_hash = $1
		FOR UPDATE
	`, hash)
	return scanDeviceLogin(row)
}

func (s *DeviceLoginService) reloadApprovalState(
	ctx context.Context,
	db dbQuerier,
	recordID string,
	approverUserID int,
	profileID string,
) error {
	record, err := s.getByID(ctx, db, recordID)
	if err != nil {
		return err
	}
	if isDeviceLoginExpired(record) {
		return ErrDeviceLoginExpired
	}
	switch record.Status {
	case DeviceLoginStatusConsumed:
		return ErrDeviceLoginConsumed
	case DeviceLoginStatusDenied:
		return ErrDeviceLoginDenied
	case DeviceLoginStatusCanceled:
		return ErrDeviceLoginCanceled
	case DeviceLoginStatusApproved:
		if sameApprovedIdentity(record, approverUserID, profileID) {
			return nil
		}
		return ErrDeviceLoginConflict
	default:
		return ErrDeviceLoginConflict
	}
}

func (s *DeviceLoginService) reloadDenyState(ctx context.Context, recordID string) error {
	record, err := s.getByID(ctx, s.pool, recordID)
	if err != nil {
		return err
	}
	if isDeviceLoginExpired(record) {
		return ErrDeviceLoginExpired
	}
	switch record.Status {
	case DeviceLoginStatusConsumed:
		return ErrDeviceLoginConsumed
	case DeviceLoginStatusDenied:
		return nil
	case DeviceLoginStatusCanceled:
		return ErrDeviceLoginCanceled
	default:
		return ErrDeviceLoginConflict
	}
}

func (s *DeviceLoginService) validateApprovingUser(ctx context.Context, userID int) error {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("load approving user: %w", err)
	}
	if !user.Enabled {
		return ErrUserDisabled
	}
	return nil
}

func (s *DeviceLoginService) validateApprovingProfile(ctx context.Context, userID int, profileID string) error {
	if err := s.validateApprovingUser(ctx, userID); err != nil {
		return err
	}
	return s.validateProfileOwnership(ctx, userID, profileID)
}

func (s *DeviceLoginService) validateProfileOwnership(ctx context.Context, userID int, profileID string) error {
	if s.stores == nil {
		return errors.New("user store provider is not configured")
	}
	store, err := s.stores.ForUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("load approving user store: %w", err)
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil {
		return fmt.Errorf("load approving profile: %w", err)
	}
	if profile == nil {
		return ErrDeviceLoginNoProfile
	}
	return nil
}

func validateDeviceLoginDecision(record *deviceLoginRecord) error {
	if isDeviceLoginExpired(record) {
		return ErrDeviceLoginExpired
	}
	switch record.Status {
	case DeviceLoginStatusConsumed:
		return ErrDeviceLoginConsumed
	case DeviceLoginStatusDenied:
		return ErrDeviceLoginDenied
	case DeviceLoginStatusCanceled:
		return ErrDeviceLoginCanceled
	case DeviceLoginStatusPending, DeviceLoginStatusApproved:
		return nil
	default:
		return ErrDeviceLoginConflict
	}
}

func sameApprovedIdentity(record *deviceLoginRecord, userID int, profileID string) bool {
	if record.ApprovedByUserID == nil || *record.ApprovedByUserID != userID {
		return false
	}
	if profileID == "" {
		return record.ApprovedProfileID == nil || strings.TrimSpace(*record.ApprovedProfileID) == ""
	}
	return record.ApprovedProfileID != nil && strings.TrimSpace(*record.ApprovedProfileID) == profileID
}

func normalizeDeviceLoginPurpose(value string, temporary bool) (string, bool, error) {
	purpose := strings.TrimSpace(value)
	if purpose == "" {
		purpose = DeviceLoginPurposeLogin
	}
	switch purpose {
	case DeviceLoginPurposeLogin:
		if temporary {
			return "", false, ErrDeviceLoginBadPurpose
		}
		return purpose, false, nil
	case DeviceLoginPurposeRemote:
		if !temporary {
			return "", false, ErrDeviceLoginBadPurpose
		}
		return purpose, true, nil
	default:
		return "", false, ErrDeviceLoginBadPurpose
	}
}

func scanDeviceLogin(row pgx.Row) (*deviceLoginRecord, error) {
	var record deviceLoginRecord
	if err := row.Scan(
		&record.ID,
		&record.DeviceCodeHash,
		&record.BrowserCodeHash,
		&record.UserCodeHash,
		&record.MatchCode,
		&record.DeviceName,
		&record.DevicePlatform,
		&record.IPAddress,
		&record.RequestedUserAgent,
		&record.Status,
		&record.ApprovedByUserID,
		&record.ApprovedProfileID,
		&record.AuthSessionID,
		&record.ClientPurpose,
		&record.Temporary,
		&record.ExpiresAt,
		&record.ApprovedAt,
		&record.DeniedAt,
		&record.ConsumedAt,
		&record.OpenedAt,
		&record.CanceledAt,
		&record.CreatedAt,
		&record.UpdatedAt,
		&record.ApprovedIdentityID,
		&record.ApprovedProviderSince,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDeviceLoginNotFound
		}
		return nil, fmt.Errorf("scan device login request: %w", err)
	}
	return &record, nil
}

func isDeviceLoginExpired(record *deviceLoginRecord) bool {
	return record == nil || !record.ExpiresAt.After(time.Now())
}

func fallbackDeviceName(deviceName, userAgent string) string {
	value := trimDeviceField(deviceName, maxDeviceNameLen)
	if value != "" {
		return value
	}
	if value = trimDeviceField(userAgent, maxDeviceNameLen); value != "" {
		return value
	}
	return "This device"
}

func trimDeviceField(value string, limit int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func maskDeviceIPAddress(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	if strings.Count(ip, ".") == 3 {
		parts := strings.Split(ip, ".")
		return parts[0] + "." + parts[1] + ".*.*"
	}
	if strings.Contains(ip, ":") {
		parts := strings.Split(ip, ":")
		if len(parts) >= 2 {
			return parts[0] + ":" + parts[1] + ":*"
		}
	}
	return ""
}

func hashDeviceLoginSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func randomToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomUserCode() (string, error) {
	// Rejection sampling keeps every digit equally likely: bytes at or above
	// the largest multiple of the alphabet size are drawn again.
	limit := 256 - 256%len(deviceCodeAlphabet)
	code := make([]byte, 0, userCodeLength)
	buf := make([]byte, userCodeLength)
	for len(code) < userCodeLength {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			code = append(code, deviceCodeAlphabet[int(b)%len(deviceCodeAlphabet)])
			if len(code) == userCodeLength {
				break
			}
		}
	}
	return formatUserCode(string(code)), nil
}

func normalizeUserCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, " ", "")
	return value
}

func formatUserCode(value string) string {
	value = normalizeUserCode(value)
	if len(value) != userCodeLength {
		return value
	}
	return value[:4] + "-" + value[4:]
}

// deviceLoginRetention is how long a request is kept after it expires, so a
// lookup of a recently expired code still says expired rather than unknown.
const deviceLoginRetention = 24 * time.Hour

// DeviceLoginRetention deletes device sign-in requests long past their
// expiry. Nothing else removes them, and user codes are unique across every
// stored request, so without it the eight-digit code space slowly fills.
type DeviceLoginRetention struct {
	pool *pgxpool.Pool
}

// NewDeviceLoginRetention returns the retention step for device sign-in
// requests.
func NewDeviceLoginRetention(pool *pgxpool.Pool) *DeviceLoginRetention {
	return &DeviceLoginRetention{pool: pool}
}

// DeleteExpired removes requests that expired more than a day ago, whatever
// their status.
func (r *DeviceLoginRetention) DeleteExpired(ctx context.Context) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM device_login_requests
		WHERE expires_at < NOW() - make_interval(secs => $1)
	`, deviceLoginRetention.Seconds())
	if err != nil {
		return 0, fmt.Errorf("deleting expired device sign-in requests: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
