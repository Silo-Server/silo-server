package plugins

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5"
)

// ProvisionOwnership stores field names and the requested package version. It contains no config values.
type ProvisionOwnership struct {
	Managed       bool
	VersionPin    string
	ConfigFields  map[string][]string
	BindingFields map[string][]string
}

const (
	managedInstallationField = "installation"
	managedVersionField      = "version"
)

var ErrManagedByEnvironment = errors.New("plugin setting is managed by the environment")

// ManagedByEnvironmentError identifies a field that cannot be changed by an ordinary write.
type ManagedByEnvironmentError struct {
	Field string
}

func (e *ManagedByEnvironmentError) Error() string {
	return e.Field + ": " + ErrManagedByEnvironment.Error()
}
func (e *ManagedByEnvironmentError) Unwrap() error { return ErrManagedByEnvironment }

func (s *InstallationStore) GetProvisionOwnership(ctx context.Context, installationID int) (ProvisionOwnership, error) {
	return loadProvisionOwnership(ctx, s.pool, installationID)
}

// ListProvisionOwnership returns one snapshot of the managed installations for list views and update checks.
func (s *InstallationStore) ListProvisionOwnership(ctx context.Context) (map[int]ProvisionOwnership, error) {
	rows, err := s.pool.Query(ctx, `SELECT plugin_installation_id, version_pin, config_fields, binding_fields FROM plugin_provision_ownership`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[int]ProvisionOwnership)
	for rows.Next() {
		var id int
		ownership := ProvisionOwnership{Managed: true}
		if err := rows.Scan(&id, &ownership.VersionPin, &ownership.ConfigFields, &ownership.BindingFields); err != nil {
			return nil, err
		}
		byID[id] = ownership
	}
	return byID, rows.Err()
}

// LoadProvisionOwnership reads ownership using the caller's transaction.
func LoadProvisionOwnership(ctx context.Context, tx pgx.Tx, installationID int) (ProvisionOwnership, error) {
	return loadProvisionOwnership(ctx, tx, installationID)
}

func loadProvisionOwnership(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, installationID int) (ProvisionOwnership, error) {
	var ownership ProvisionOwnership
	err := db.QueryRow(ctx, `SELECT version_pin, config_fields, binding_fields
		FROM plugin_provision_ownership WHERE plugin_installation_id = $1`, installationID).
		Scan(&ownership.VersionPin, &ownership.ConfigFields, &ownership.BindingFields)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProvisionOwnership{}, nil
	}
	if err != nil {
		return ProvisionOwnership{}, fmt.Errorf("reading plugin ownership: %w", err)
	}
	ownership.Managed = true
	return ownership, nil
}

// PutProvisionOwnership is for the provisioning writer. The caller holds the
// installation row lock and writes the owned values in the same transaction.
func PutProvisionOwnership(ctx context.Context, tx pgx.Tx, installationID int, ownership ProvisionOwnership) error {
	if !ownership.Managed && ownership.VersionPin == "" && len(ownership.ConfigFields) == 0 && len(ownership.BindingFields) == 0 {
		_, err := tx.Exec(ctx, `DELETE FROM plugin_provision_ownership WHERE plugin_installation_id = $1`, installationID)
		return err
	}
	if ownership.ConfigFields == nil {
		ownership.ConfigFields = map[string][]string{}
	}
	if ownership.BindingFields == nil {
		ownership.BindingFields = map[string][]string{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO plugin_provision_ownership
		(plugin_installation_id, version_pin, config_fields, binding_fields) VALUES ($1, $2, $3, $4)
		ON CONFLICT (plugin_installation_id) DO UPDATE SET version_pin = EXCLUDED.version_pin,
		config_fields = EXCLUDED.config_fields, binding_fields = EXCLUDED.binding_fields
		WHERE (plugin_provision_ownership.version_pin, plugin_provision_ownership.config_fields, plugin_provision_ownership.binding_fields)
			IS DISTINCT FROM (EXCLUDED.version_pin, EXCLUDED.config_fields, EXCLUDED.binding_fields)`,
		installationID, ownership.VersionPin, ownership.ConfigFields, ownership.BindingFields)
	if err != nil {
		return fmt.Errorf("writing plugin ownership: %w", err)
	}
	return nil
}

func lockPluginInstallation(ctx context.Context, tx pgx.Tx, installationID int) error {
	var id int
	err := tx.QueryRow(ctx, `SELECT id FROM plugin_installations WHERE id = $1 FOR NO KEY UPDATE`, installationID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInstallationNotFound
	}
	if err != nil {
		return fmt.Errorf("locking plugin installation: %w", err)
	}
	return nil
}

func declaredProvisionOwnership(desired ProvisionPlugin) ProvisionOwnership {
	owned := ProvisionOwnership{
		Managed: true, VersionPin: desired.Version,
		ConfigFields: make(map[string][]string), BindingFields: make(map[string][]string),
	}
	for key, fields := range desired.Config {
		owned.ConfigFields[key] = slices.Sorted(maps.Keys(fields))
	}
	return ownedWithBinding(owned, desired.AuthBinding)
}

// Release omitted ownership while a disabled installation waits for its
// desired release. Fields and pins we have yet to apply stay unclaimed.
func releaseOmittedProvisionOwnership(ctx context.Context, tx pgx.Tx, installationID int, desired ProvisionPlugin) error {
	current, err := LoadProvisionOwnership(ctx, tx, installationID)
	if err != nil || !current.Managed {
		return err
	}
	wanted := declaredProvisionOwnership(desired)
	if wanted.VersionPin == "" {
		current.VersionPin = ""
	}
	retainProvisionFields(current.ConfigFields, wanted.ConfigFields)
	retainProvisionFields(current.BindingFields, wanted.BindingFields)
	return PutProvisionOwnership(ctx, tx, installationID, current)
}

func retainProvisionFields(current, wanted map[string][]string) {
	for key, fields := range current {
		retained := slices.DeleteFunc(fields, func(field string) bool {
			return !slices.Contains(wanted[key], field)
		})
		if len(retained) == 0 {
			delete(current, key)
			continue
		}
		current[key] = retained
	}
}

func preserveOwnedConfig(fields []string, stored, proposed map[string]any) map[string]any {
	if len(fields) == 0 {
		return proposed
	}
	proposed = maps.Clone(proposed)
	if proposed == nil {
		proposed = make(map[string]any)
	}
	for _, field := range fields {
		if value, exists := stored[field]; exists {
			proposed[field] = value
		} else {
			delete(proposed, field)
		}
	}
	return proposed
}

func requireUnchangedOwnedConfig(key string, fields []string, stored, proposed map[string]any) error {
	for _, field := range fields {
		before, beforeExists := stored[field]
		after, afterExists := proposed[field]
		if beforeExists != afterExists || (beforeExists && !SameConfig(map[string]any{field: before}, map[string]any{field: after})) {
			return &ManagedByEnvironmentError{Field: "config." + key + "." + field}
		}
	}
	return nil
}

// guardInstallationUpdate runs with the parent locked. A pinned installation
// keeps its release and policy. An unpinned declaration can follow its
// existing catalog only when the replacement still accepts the owned settings.
func (s *InstallationStore) guardInstallationUpdate(ctx context.Context, tx pgx.Tx, installation *Installation, input UpdateInstallationInput) error {
	ownership, err := LoadProvisionOwnership(ctx, tx, installation.ID)
	if err != nil {
		return err
	}
	if err := guardManagedPackageUpdate(installation, input, ownership); err != nil {
		return err
	}
	if input.Archive == nil {
		return nil
	}
	manifest, err := LoadManifestBytes(input.Archive.ManifestJSON)
	if err != nil {
		return err
	}
	if err := s.guardReplacementCredentials(ctx, tx, installation, manifest); err != nil {
		return err
	}
	if !ownership.Managed {
		return nil
	}
	return s.validateManagedReplacement(ctx, tx, installation.ID, manifest, ownership)
}

func guardManagedPackageUpdate(installation *Installation, input UpdateInstallationInput, ownership ProvisionOwnership) error {
	if !ownership.Managed {
		return nil
	}
	replacing := input.Version != nil || input.InstallPath != nil || input.SetRepository || input.Capabilities != nil || input.Archive != nil || input.Signature != nil
	if ownership.VersionPin != "" {
		if input.UpdatePolicy != nil && *input.UpdatePolicy != installation.UpdatePolicy {
			return &ManagedByEnvironmentError{Field: "update_policy"}
		}
		if replacing || input.AvailableVersion != nil || input.UpdateFailure != nil {
			return &ManagedByEnvironmentError{Field: managedVersionField}
		}
	}
	if replacing && !matchesManagedPackageSource(installation, input) {
		return &ManagedByEnvironmentError{Field: managedInstallationField}
	}
	return nil
}

func matchesManagedPackageSource(installation *Installation, input UpdateInstallationInput) bool {
	return input.SetRepository && installation.RepositoryID != nil && input.RepositoryID != nil &&
		*installation.RepositoryID == *input.RepositoryID && input.Signature != nil &&
		input.Signature.State == SignatureStateVerified && input.Signature.SourceRepo != nil &&
		installation.Signature.SourceRepo != nil && *input.Signature.SourceRepo == *installation.Signature.SourceRepo &&
		input.Archive != nil
}

func (s *InstallationStore) validateManagedReplacement(ctx context.Context, tx pgx.Tx, installationID int, manifest *pluginv1.PluginManifest, ownership ProvisionOwnership) error {

	for key, fields := range ownership.ConfigFields {
		public, secret := GlobalConfigFieldSets(manifest, key)
		declared := stringSet(append(public, secret...))
		for _, field := range fields {
			if _, ok := declared[field]; !ok {
				return &ManagedByEnvironmentError{Field: "config." + key + "." + field}
			}
		}
		current, err := readGlobalConfigInTx(ctx, tx, s.cipher, installationID, key)
		if err != nil {
			return err
		}
		if err := validateMergedGlobalConfig(manifest, key, current, nil); err != nil {
			return &ManagedByEnvironmentError{Field: "config." + key}
		}
	}
	for capabilityID, fields := range ownership.BindingFields {
		capability := authProviderCapability(manifest, capabilityID)
		if capability == nil {
			return &ManagedByEnvironmentError{Field: "auth_binding." + capabilityID}
		}
		var callbackMode string
		err := tx.QueryRow(ctx, `SELECT callback_mode FROM plugin_auth_bindings
			WHERE plugin_installation_id = $1 AND capability_id = $2`, installationID, capabilityID).Scan(&callbackMode)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		ownsCallback := slices.Contains(fields, "callback_mode") || slices.Contains(fields, "callback")
		if (callbackMode == AuthCallbackStable || ownsCallback) &&
			ValidateAuthCallbackMode(callbackMode, capability.GetAuthModes()) != nil {
			return &ManagedByEnvironmentError{Field: "auth_binding.callback_mode"}
		}
	}
	return nil
}

func (s *InstallationStore) declaredPluginIDs(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.pool.Query(ctx, `SELECT plugin_id FROM plugin_provision_status`)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	return stringSet(ids), nil
}
