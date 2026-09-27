package bridgeimport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ImportAccount imports a verified standalone source while every application
// writer is fenced by the caller. It does not create source files, upgrade a
// source, merge target data, or activate a provider. Only synthetic callers are
// wired in this milestone; operational backup/fencing gates remain separate.
func ImportAccount(ctx context.Context, pool *pgxpool.Pool, path string, identity BackupIdentity, transition ProgressTransition) (ImportResult, error) {
	empty := ImportResult{}
	if pool == nil || transition == nil || identity.AccountID <= 0 {
		return empty, errors.New("import dependencies or account are invalid")
	}
	installation, err := uuid.Parse(identity.InstallationID)
	if err != nil || installation == uuid.Nil {
		return empty, errors.New("installation identity is invalid")
	}
	if digest, err := hex.DecodeString(identity.SourceSHA256); err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != identity.SourceSHA256 {
		return empty, errors.New("source digest is invalid")
	}
	if _, err := Inspect(ctx, path, int64(identity.AccountID)); err != nil {
		return empty, err
	}
	if err := verifySourceDigest(ctx, path, identity.SourceSHA256); err != nil {
		return empty, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return empty, errors.New("cannot resolve source")
	}
	uri := url.URL{Scheme: sourceFileScheme, Path: absolute, RawQuery: sourceReadOnlyQuery}
	source, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return empty, errors.New("cannot open source")
	}
	defer source.Close() //nolint:errcheck
	source.SetMaxOpenConns(1)
	snapshot, err := source.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, errors.New("cannot read source")
	}
	defer snapshot.Rollback() //nolint:errcheck
	if err := validateSourceSchema(ctx, snapshot); err != nil {
		return empty, err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return empty, importFailure(ctx, "begin target transaction")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", "userdb-import:"+strconv.Itoa(identity.AccountID)); err != nil {
		return empty, importFailure(ctx, "lock target account")
	}
	var account int
	if err := tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", identity.AccountID).Scan(&account); err != nil {
		return empty, importFailure(ctx, "target account is unavailable")
	}
	var targetInstallation string
	if err := tx.QueryRow(ctx, "SELECT value FROM server_settings WHERE key='diagnostics.server_instance_id' FOR SHARE").Scan(&targetInstallation); err != nil {
		return empty, importFailure(ctx, "target installation identity is unavailable")
	}
	targetID, err := uuid.Parse(targetInstallation)
	if err != nil || targetID != installation {
		return empty, errors.New("source installation does not match target")
	}
	result, found, err := replayReceipt(ctx, tx, identity, installation.String())
	if err != nil {
		return empty, err
	}
	if found {
		return result, nil
	}
	if err := refuseTargetState(ctx, tx, identity.AccountID); err != nil {
		return empty, err
	}
	if err := validateSourceOwnership(ctx, snapshot); err != nil {
		return empty, err
	}
	result = ImportResult{Status: accountImported, ProviderSwitch: providerSwitchBlocked, Tables: map[string]TableVerification{}}
	for _, mapping := range Manifest() {
		if mapping.Target == "" {
			result.Tables[mapping.Source] = TableVerification{SHA256: hex.EncodeToString(sha256.New().Sum(nil))}
			continue
		}
		if mapping.Source == sourceSectionOverrides {
			continue
		}
		verification, err := importTable(ctx, snapshot, tx, identity.AccountID, mapping)
		if err != nil {
			return empty, err
		}
		result.Tables[mapping.Source] = verification
	}
	sections, err := importSections(ctx, snapshot, tx, identity.AccountID)
	if err != nil {
		return empty, err
	}
	result.Tables[sourceSectionOverrides] = sections
	if err := validateTargetReferences(ctx, tx, identity.AccountID); err != nil {
		return empty, err
	}
	generation, err := transition(ctx, tx, identity.AccountID)
	if err != nil {
		return empty, importFailure(ctx, "progress transition failed")
	}
	parsed, err := uuid.Parse(generation)
	if err != nil || parsed == uuid.Nil {
		return empty, errors.New("progress transition returned invalid generation")
	}
	result.ProgressGeneration = parsed.String()
	if err := checkSidecars(path); err != nil {
		return empty, err
	}
	if err := verifySourceDigest(ctx, path, identity.SourceSHA256); err != nil {
		return empty, err
	}
	verification, err := json.Marshal(result.Tables)
	if err != nil {
		return empty, errors.New("cannot encode import verification")
	}
	if err := tx.QueryRow(ctx, `INSERT INTO userdb_import_receipts(user_id,installation_id,source_sha256,schema_version,mapping_version,progress_generation,verification) VALUES($1,$2,$3,22,$4,$5,$6) RETURNING imported_at`, identity.AccountID, installation.String(), identity.SourceSHA256, MappingVersion, result.ProgressGeneration, verification).Scan(&result.ImportedAt); err != nil {
		return empty, importFailure(ctx, "cannot record import receipt")
	}
	if err := tx.Commit(ctx); err != nil {
		return empty, importFailure(ctx, "import commit acknowledgement unavailable; check receipt before retry")
	}
	return result, nil
}

func importFailure(ctx context.Context, phase string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New(phase)
}

func verifySourceDigest(ctx context.Context, path, want string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("source is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("cannot hash source")
	}
	defer file.Close() //nolint:errcheck
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("source changed")
	}
	digest := sha256.New()
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := file.Read(buffer)
		if n > 0 {
			_, _ = digest.Write(buffer[:n])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("cannot hash source")
		}
	}
	if hex.EncodeToString(digest.Sum(nil)) != want {
		return errors.New("source digest mismatch")
	}
	return nil
}

func replayReceipt(ctx context.Context, tx pgx.Tx, identity BackupIdentity, installation string) (ImportResult, bool, error) {
	result := ImportResult{Status: accountImported, ProviderSwitch: providerSwitchBlocked, Replayed: true}
	var previousInstallation, digest string
	var version, schema int
	var verification []byte
	err := tx.QueryRow(ctx, `SELECT installation_id::text,source_sha256,mapping_version,schema_version,progress_generation::text,verification,imported_at FROM userdb_import_receipts WHERE user_id=$1`, identity.AccountID).Scan(&previousInstallation, &digest, &version, &schema, &result.ProgressGeneration, &verification, &result.ImportedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, importFailure(ctx, "cannot read import receipt")
	}
	if previousInstallation != installation || digest != identity.SourceSHA256 || version != MappingVersion || schema != 22 {
		return result, false, errors.New("existing import receipt conflicts with source identity")
	}
	if err := json.Unmarshal(verification, &result.Tables); err != nil {
		return result, false, errors.New("invalid import receipt")
	}
	// No target rewrite or generation rotation, even after legitimate later writes.
	return result, true, nil
}

func refuseTargetState(ctx context.Context, tx pgx.Tx, userID int) error {
	for _, mapping := range Manifest() {
		if mapping.Target == "" || mapping.Source == sourceSectionOverrides {
			continue
		}
		predicate := "user_id=$1"
		if mapping.Source == sourceOrderRevision {
			predicate += " AND revision<>1"
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+pgx.Identifier{mapping.Target}.Sanitize()+" WHERE "+predicate+")", userID).Scan(&exists); err != nil {
			return importFailure(ctx, "cannot inspect target state")
		}
		if exists {
			return fmt.Errorf("target data conflicts in %s", mapping.Target)
		}
	}
	// PostgreSQL-only collection state must never be silently combined with an
	// imported account's source membership/order witnesses.
	for _, table := range []string{"user_collection_groups"} {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+pgx.Identifier{table}.Sanitize()+" WHERE user_id=$1)", userID).Scan(&exists); err != nil {
			return importFailure(ctx, "cannot inspect target collection state")
		}
		if exists {
			return errors.New("target PostgreSQL collection state conflicts")
		}
	}
	return nil
}
