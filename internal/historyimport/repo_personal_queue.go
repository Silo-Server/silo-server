package historyimport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func personalCredentialAAD(runID string) string {
	return secret.RowAAD("history_import_run_credentials", "payload", runID)
}

func validatePersonalCredentials(sourceType string, credential personalRunCredentials) error {
	if validateSource(Source{Name: "Personal import", SourceType: sourceType, BaseURL: credential.BaseURL}) != nil || credential.ServerToken == "" {
		return ErrPersonalCredentialsUnavailable
	}
	switch sourceType {
	case SourceTypeEmby, SourceTypeJellyfin:
		if credential.ExternalUserID == "" || credential.AccountToken != "" {
			return ErrPersonalCredentialsUnavailable
		}
	case SourceTypePlex:
		if credential.AccountToken == "" || credential.ExternalUserID != "" {
			return ErrPersonalCredentialsUnavailable
		}
	default:
		return ErrPersonalCredentialsUnavailable
	}
	return nil
}

// enqueuePersonalRun accepts already-authenticated credentials. Network effects
// must finish before this call: the transaction only revalidates immutable
// snapshots, consumes authorization, and persists reconstructible work.
func (r *Repository) enqueuePersonalRun(ctx context.Context, in personalRunAdmission) (*Run, error) {
	if r.cipher == nil {
		return nil, ErrPersonalCredentialsUnavailable
	}
	if err := validatePersonalCredentials(in.SourceType, in.Credentials); err != nil {
		return nil, err
	}
	if in.SourceID < 0 || (in.SourceID == 0) != (in.SourceRevision == 0) || (in.SourceID > 0 && in.SourceRevision < 1) {
		return nil, ErrInvalidInput
	}
	if in.ConnectSession != nil && in.PlexSession != nil {
		return nil, ErrInvalidInput
	}
	switch in.ConnectionMode {
	case ConnectionModePredefined:
		if in.SourceID == 0 || in.ConnectSession != nil || in.PlexSession != nil {
			return nil, ErrInvalidInput
		}
	case ConnectionModeConnect:
		if in.SourceType != SourceTypeEmby || in.ConnectSession == nil || in.SourceID != 0 {
			return nil, ErrInvalidInput
		}
	case ConnectionModePlexOAuth:
		if in.SourceType != SourceTypePlex || in.ConnectSession != nil || in.SourceID != 0 {
			return nil, ErrInvalidInput
		}
	case ConnectionModeCustom:
		if in.SourceType != SourceTypeJellyfin || in.ConnectSession != nil || in.PlexSession != nil || in.SourceID != 0 {
			return nil, ErrInvalidInput
		}
	default:
		return nil, ErrInvalidInput
	}
	id := uuid.NewString()
	payload, err := json.Marshal(in.Credentials)
	if err != nil {
		return nil, ErrPersonalCredentialsUnavailable
	}
	ciphertext, err := r.cipher.Encrypt(string(payload), personalCredentialAAD(id))
	if err != nil {
		return nil, ErrPersonalCredentialsUnavailable
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if in.SourceID > 0 {
		var sourceType, baseURL string
		var revision int64
		var enabled bool
		err = tx.QueryRow(ctx, `SELECT source_type,base_url,revision,enabled FROM history_import_sources WHERE id=$1 FOR SHARE`, in.SourceID).Scan(&sourceType, &baseURL, &revision, &enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSourceNotFound
		}
		if err != nil {
			return nil, err
		}
		if !enabled {
			return nil, ErrSourceDisabled
		}
		if revision != in.SourceRevision || sourceType != in.SourceType || !personalAuthenticatedBaseMatches(in.SourceType, baseURL, in.Credentials.BaseURL) {
			return nil, ErrRunConfigurationChanged
		}
	}
	if err = lockMappingTarget(ctx, tx, in.UserID, in.ProfileID); err != nil {
		return nil, err
	}
	if err = r.consumePersonalSession(ctx, tx, in); err != nil {
		return nil, err
	}
	run := &Run{ID: id, UserID: in.UserID, ProfileID: in.ProfileID, SourceType: in.SourceType, ConnectionMode: in.ConnectionMode, Status: RunStatusQueued, Warnings: []string{}, UnmatchedSamples: []UnmatchedSample{}}
	err = tx.QueryRow(ctx, `INSERT INTO history_import_runs(id,user_id,profile_id,source_type,connection_mode,status,dispatch_kind,dispatch_version,dispatch_source_id,dispatch_source_revision)
 VALUES($1,$2,$3,$4,$5,'queued','personal',$8,NULLIF($6,0),NULLIF($7,0)) RETURNING created_at`, id, in.UserID, in.ProfileID, in.SourceType, in.ConnectionMode, in.SourceID, in.SourceRevision, personalDispatchVersion).Scan(&run.CreatedAt)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO history_import_run_credentials(run_id,envelope_version,payload) VALUES($1,$2,$3)`, id, personalCredentialVersion, ciphertext); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		// A lost COMMIT response does not prove rollback. Never report this as a
		// safe retry, nor attempt compensating consumption or deletion afterward.
		return nil, errors.Join(ErrPersonalAdmissionUncertain, err)
	}
	return run, nil
}

func (r *Repository) consumePersonalSession(ctx context.Context, tx pgx.Tx, in personalRunAdmission) error {
	if expected := in.ConnectSession; expected != nil {
		current, err := r.scanConnectSession(tx.QueryRow(ctx, `SELECT id,user_id,connect_user_id,connect_access_token,servers_json,expires_at,consumed_at,created_at,updated_at FROM history_import_connect_sessions WHERE id=$1 AND user_id=$2 FOR UPDATE`, expected.ID, in.UserID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConnectSessionNotFound
		}
		if err != nil {
			return ErrPersonalSessionChanged
		}
		if current.ConsumedAt != nil {
			return ErrConnectSessionUsed
		}
		if err = personalSessionUnexpired(ctx, tx, current.ExpiresAt); err != nil {
			return err
		}
		if current.UserID != expected.UserID || current.ConnectUserID != expected.ConnectUserID || current.ConnectAccessToken != expected.ConnectAccessToken || !slices.Equal(current.Servers, expected.Servers) || !current.ExpiresAt.Equal(expected.ExpiresAt) || !current.UpdatedAt.Equal(expected.UpdatedAt) {
			return ErrPersonalSessionChanged
		}
		selected := slices.IndexFunc(current.Servers, func(s ConnectServer) bool { return s.ID == in.SelectedServerID })
		if selected < 0 || !personalAuthenticatedBaseMatches(in.SourceType, firstNonEmpty(current.Servers[selected].URL, current.Servers[selected].LocalAddress), in.Credentials.BaseURL) {
			return ErrPersonalSessionChanged
		}
		_, err = tx.Exec(ctx, `UPDATE history_import_connect_sessions SET consumed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, current.ID)
		return err
	}
	if expected := in.PlexSession; expected != nil {
		current, err := r.scanPlexSession(tx.QueryRow(ctx, `SELECT id,user_id,pin_id,pin_code,auth_token,servers_json,expires_at,consumed_at,created_at,updated_at FROM history_import_plex_sessions WHERE id=$1 AND user_id=$2 FOR UPDATE`, expected.ID, in.UserID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPlexSessionNotFound
		}
		if err != nil {
			return ErrPersonalSessionChanged
		}
		if current.ConsumedAt != nil {
			return ErrPlexSessionUsed
		}
		if err = personalSessionUnexpired(ctx, tx, current.ExpiresAt); err != nil {
			return err
		}
		if current.UserID != expected.UserID || current.PinID != expected.PinID || current.PinCode != expected.PinCode || current.AuthToken != expected.AuthToken || !slices.Equal(current.Servers, expected.Servers) || !current.ExpiresAt.Equal(expected.ExpiresAt) || !current.UpdatedAt.Equal(expected.UpdatedAt) {
			return ErrPersonalSessionChanged
		}
		selected := slices.IndexFunc(current.Servers, func(s PlexServer) bool { return s.ClientIdentifier == in.SelectedServerID })
		if selected < 0 {
			return ErrPersonalSessionChanged
		}
		server := current.Servers[selected]
		if firstNonEmpty(server.RemoteURL, server.LocalURL) != in.Credentials.BaseURL || server.AccessToken != in.Credentials.ServerToken || current.AuthToken != in.Credentials.AccountToken {
			return ErrPersonalSessionChanged
		}
		_, err = tx.Exec(ctx, `UPDATE history_import_plex_sessions SET consumed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, current.ID)
		return err
	}
	return nil
}

func personalSessionUnexpired(ctx context.Context, tx pgx.Tx, expiresAt time.Time) error {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	if !expiresAt.After(now) {
		return ErrPersonalSessionChanged
	}
	return nil
}

// readPersonalRunCredentials is private; execution must hold and validate a run
// claim before using its result. Strict decoding never accepts legacy plaintext.
func (r *Repository) readPersonalRunCredentials(ctx context.Context, claim RunClaim) (personalRunCredentials, error) {
	var credential personalRunCredentials
	if claim.DispatchKind != dispatchKindPersonal || claim.Generation < 1 {
		return credential, ErrRunClaimLost
	}
	if err := r.validateRunClaim(ctx, claim); err != nil {
		return credential, err
	}
	if r.cipher == nil {
		return credential, ErrPersonalCredentialsUnavailable
	}
	var version int
	var ciphertext, sourceType string
	err := r.pool.QueryRow(ctx, `SELECT c.envelope_version,c.payload,r.source_type FROM history_import_run_credentials c JOIN history_import_runs r ON r.id=c.run_id WHERE c.run_id=$1 AND r.dispatch_kind='personal' AND r.dispatch_version=2 AND r.status='running' AND r.claim_generation=$2 AND r.cancel_requested_at IS NULL`, claim.RunID, claim.Generation).Scan(&version, &ciphertext, &sourceType)
	if err != nil || version != personalCredentialVersion {
		return credential, ErrPersonalCredentialsUnavailable
	}
	plaintext, err := r.cipher.Decrypt(ciphertext, personalCredentialAAD(claim.RunID))
	if err != nil {
		return credential, ErrPersonalCredentialsUnavailable
	}
	decoder := json.NewDecoder(strings.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&credential); err != nil {
		return personalRunCredentials{}, ErrPersonalCredentialsUnavailable
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return personalRunCredentials{}, ErrPersonalCredentialsUnavailable
	}
	if err = validatePersonalCredentials(sourceType, credential); err != nil {
		return personalRunCredentials{}, err
	}
	if err := r.validateRunClaim(ctx, claim); err != nil {
		return personalRunCredentials{}, err
	}
	return credential, nil
}

func personalAuthenticatedBaseMatches(sourceType, selected, authenticated string) bool {
	if sourceType == SourceTypeEmby {
		return slices.Contains(baseCandidates(selected), authenticated)
	}
	return selected == authenticated
}
