package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/jackc/pgx/v5"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

type preparedProvision struct {
	pluginID  string
	desired   ProvisionPlugin
	pkg       *PreparedProvisionPackage
	config    map[string]map[string]any
	installed *Installation
	held      bool
	skip      bool
}

type provisionRefusalError struct {
	pluginID string
	detail   string
	local    bool
}

func (e *provisionRefusalError) Error() string {
	return fmt.Sprintf("plugin %s: %s", e.pluginID, e.detail)
}

func (w *ProvisionWriter) apply(ctx context.Context, offline bool) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if w.heldForRestore {
		return w.recordRestoreHold(ctx)
	}
	hold, err := ProvisionRestoreHold(ctx, w.pool)
	if err != nil {
		return true, nil
	}
	if hold != "" {
		return w.recordRestoreHold(ctx)
	}

	staged, needsFetch, err := w.prepareWithAuthority(ctx, offline)
	defer func() {
		for _, item := range staged {
			if item.pkg == nil {
				continue
			}
			if abortErr := item.pkg.Abort(); abortErr != nil {
				slog.WarnContext(ctx, "could not remove staged plugin package", "component", "plugins", "plugin_id", item.pluginID)
			}
		}
	}()
	if err == nil && !needsFetch {
		var statuses []ProvisionStatus
		statuses, err = w.commit(ctx, staged)
		if err == nil {
			return slices.ContainsFunc(statuses, func(status ProvisionStatus) bool { return status.State == "held" }), nil
		}
	}
	if errors.Is(err, errProvisionAuthorityLost) || ctx.Err() != nil {
		return true, err
	}
	if refusal, ok := errors.AsType[*provisionRefusalError](err); ok {
		if offline && refusal.local {
			return false, refusal
		}
		err := w.saveFailureStatus(ctx, "refused", "The declaration contains a plugin that was refused.", refusal)
		return err != nil, err
	}
	if errors.Is(err, errProvisionRestoreHeld) {
		return w.recordRestoreHold(ctx)
	}
	return true, w.saveFailureStatus(ctx, "pending", "Waiting for the current catalog, package, or database state.", nil)
}

// Acknowledgment releases a reviewed deployment on its next startup. Even if
// this writer reconnects, its original declaration predates that review.
func (w *ProvisionWriter) recordRestoreHold(ctx context.Context) (bool, error) {
	w.heldForRestore = true
	w.placementPending = false
	ctx, cancel := context.WithTimeout(ctx, provisionDBTimeout)
	defer cancel()
	err := w.saveFailureStatus(ctx, "held", "Review the declaration, acknowledge the restore hold and restart the provisioning instance.", nil)
	return err != nil, err
}

// prepareWithAuthority keeps all uses of the advisory lock's connection on one
// goroutine. The worker can use the service pool, while this goroutine tests
// the lock and cancels preparation as soon as the session is lost.
func (w *ProvisionWriter) prepareWithAuthority(ctx context.Context, offline bool) ([]preparedProvision, bool, error) {
	if offline {
		return w.prepare(ctx, true)
	}
	type result struct {
		items      []preparedProvision
		needsFetch bool
		err        error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		items, needsFetch, err := w.prepare(ctx, false)
		done <- result{items, needsFetch, err}
	}()
	tick := time.NewTicker(provisionHeartbeat)
	defer tick.Stop()
	for {
		select {
		case r := <-done:
			return r.items, r.needsFetch, r.err
		case <-ctx.Done():
			r := <-done
			return r.items, r.needsFetch, ctx.Err()
		case <-tick.C:
			if err := w.checkAuthority(ctx); err != nil {
				cancel()
				r := <-done
				return r.items, r.needsFetch, err
			}
		}
	}
}

func (w *ProvisionWriter) prepare(ctx context.Context, offline bool) ([]preparedProvision, bool, error) {
	var staged []preparedProvision
	needsFetch := false
	reconciled := false
	var stagedBytes int
	for _, pluginID := range slices.Sorted(maps.Keys(w.document.Plugins)) {
		desired := w.document.Plugins[pluginID]
		installations, err := w.installations.ListByPluginID(ctx, pluginID)
		if err != nil {
			return staged, false, err
		}
		var installed *Installation
		version := desired.Version
		if len(installations) != 0 {
			installed = installations[0]
			if !installed.Enabled {
				// The admin switch holds the installed release in place.
				version = installed.Version
			}
		}
		if installed == nil && !reconciled {
			if _, err := w.repositories.ReconcileManaged(ctx); err != nil {
				return staged, false, err
			}
			reconciled = true
		}
		item := preparedProvision{pluginID: pluginID, desired: desired, installed: installed, held: installed != nil && !installed.Enabled}
		pkg, err := w.catalog.PrepareProvisionPackage(ctx, w.installer, ProvisionPackageRequest{
			PluginID: pluginID, Version: version, Existing: installed, Offline: offline,
		})
		if errors.Is(err, ErrProvisionPackageNeedsFetch) {
			needsFetch = true
			continue
		}
		pkgErr, packageError := AsPackageError(err)
		if item.held && (errors.Is(err, ErrProvisionPackageUntrusted) || (packageError && !pkgErr.Transient)) {
			// A disabled package with no usable manifest remains held. An
			// operator can address its source before enabling it again.
			item.skip = true
			staged = append(staged, item)
			continue
		}
		if packageError && !pkgErr.Transient {
			return staged, false, &provisionRefusalError{pluginID: pluginID, detail: pkgErr.Message}
		}
		if errors.Is(err, ErrProvisionPackageUntrusted) {
			return staged, false, &provisionRefusalError{pluginID: pluginID, detail: "Replace the installed plugin with a verified release from the official catalog."}
		}
		if err != nil {
			return staged, false, err
		}
		item.pkg = pkg
		staged = append(staged, item)
		stagedBytes += len(pkg.create.Archive.Bytes)
		const maxProvisionArchiveBytes = 512 << 20
		if stagedBytes > maxProvisionArchiveBytes {
			return staged, false, &provisionRefusalError{pluginID: pluginID, detail: "The declaration exceeds the combined plugin package size limit."}
		}
		entry := &staged[len(staged)-1]
		if err := w.preflightValues(ctx, entry); err != nil {
			if _, refusal := errors.AsType[*provisionRefusalError](err); refusal && item.held && version != desired.Version && desired.Version != "" {
				// Desired fields may belong to the eventual version. Defer
				// writing values until that version can apply.
				entry.skip = true
				continue
			}
			return staged, false, err
		}
	}
	return staged, needsFetch, nil
}

func (w *ProvisionWriter) preflightValues(ctx context.Context, item *preparedProvision) error {
	validate := func(q provisionConfigQuery, installationID int) error {
		config, err := w.document.ResolveConfig(item.pkg.Manifest, item.pluginID)
		if err != nil {
			return &provisionRefusalError{pluginID: item.pluginID, detail: err.Error(), local: true}
		}
		item.config = config
		if _, err := w.planConfigValues(ctx, q, *item, installationID); err != nil {
			return err
		}
		if binding := item.desired.AuthBinding; binding != nil {
			capability := authProviderCapability(item.pkg.Manifest, binding.CapabilityID)
			if capability == nil {
				return &provisionRefusalError{pluginID: item.pluginID, detail: "auth_binding.capability_id does not name a sign-in capability.", local: true}
			}
			if err := ValidateAuthCallbackMode(binding.Callback, capability.GetAuthModes()); err != nil {
				return &provisionRefusalError{pluginID: item.pluginID, detail: "auth_binding.callback is invalid for this sign-in capability.", local: true}
			}
		}
		return nil
	}
	if item.installed == nil {
		return validate(w.pool, 0)
	}
	return pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		current, err := item.pkg.lockCurrent(ctx, w.installations.WithTx(tx), tx)
		if err != nil {
			return err
		}
		return validate(tx, current.ID)
	})
}

func (w *ProvisionWriter) commit(ctx context.Context, staged []preparedProvision) ([]ProvisionStatus, error) {
	statuses := make([]ProvisionStatus, 0, len(staged))
	queuedPlacement := false
	changed := false
	err := pgx.BeginFunc(ctx, w.conn, func(tx pgx.Tx) error {
		if err := verifyProvisionAuthority(ctx, tx); err != nil {
			return err
		}
		if err := lockAuthBindings(ctx, tx); err != nil {
			return err
		}
		if err := checkPluginRestoreHold(ctx, tx); err != nil {
			return err
		}
		if err := w.lockTargetsAndReleaseOwnership(ctx, tx); err != nil {
			return err
		}
		bindingIDs := make(map[string]int)
		for _, item := range staged {
			result, err := w.applyItem(ctx, tx, item)
			if err != nil {
				return err
			}
			statuses = append(statuses, result.status)
			if result.bindingInstallationID != 0 {
				bindingIDs[item.pluginID] = result.bindingInstallationID
			}
			changed = changed || result.runtimeChanged
			queuedPlacement = queuedPlacement || result.placementQueued
		}
		if err := validateProvisionBindingSet(ctx, tx, bindingIDs); err != nil {
			return err
		}
		if err := logProvisionSignIn(ctx, tx); err != nil {
			return err
		}
		if err := writeProvisionStatuses(ctx, tx, statuses); err != nil {
			return err
		}
		return verifyProvisionAuthority(ctx, tx)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range staged {
		if item.pkg == nil {
			continue
		}
		if err := item.pkg.Finish(ctx); err != nil {
			slog.WarnContext(ctx, "could not finalize committed plugin cache files", "component", "plugins", "plugin_id", item.pluginID)
		}
	}
	if queuedPlacement && w.placeMetadata != nil {
		w.placementPending = true
	}
	if changed && w.service != nil {
		w.service.OnLifecycleChange(ctx)
	}
	return statuses, nil
}

func (w *ProvisionWriter) lockTargetsAndReleaseOwnership(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `
		SELECT i.id, i.plugin_id
		FROM plugin_installations i
		WHERE i.plugin_id = ANY($1::text[])
			OR EXISTS (SELECT 1 FROM plugin_provision_ownership o WHERE o.plugin_installation_id = i.id)
		ORDER BY i.id
		FOR NO KEY UPDATE OF i
	`, append([]string{}, slices.Collect(maps.Keys(w.document.Plugins))...))
	if err != nil {
		return err
	}
	type lockedInstallation struct {
		ID       int
		PluginID string
	}
	locked, err := pgx.CollectRows(rows, pgx.RowToStructByPos[lockedInstallation])
	if err != nil {
		return err
	}
	for _, row := range locked {
		if _, wanted := w.document.Plugins[row.PluginID]; !wanted {
			if err := PutProvisionOwnership(ctx, tx, row.ID, ProvisionOwnership{}); err != nil {
				return err
			}
		}
	}

	return nil
}

type provisionedItem struct {
	status                ProvisionStatus
	bindingInstallationID int
	runtimeChanged        bool
	placementQueued       bool
}

func (w *ProvisionWriter) applyItem(ctx context.Context, tx pgx.Tx, item preparedProvision) (provisionedItem, error) {
	if item.skip {
		current, err := w.installations.WithTx(tx).GetByID(ctx, item.installed.ID)
		if err != nil {
			return provisionedItem{}, err
		}
		if current.Enabled || current.Version != item.installed.Version || current.InstallPath != item.installed.InstallPath {
			return provisionedItem{}, ErrInstallationChanged
		}
		if err := releaseOmittedProvisionOwnership(ctx, tx, current.ID, item.desired); err != nil {
			return provisionedItem{}, err
		}
		return provisionedItem{status: disabledProvisionStatus(item.pluginID, current.Version)}, nil
	}
	installation, err := item.pkg.Persist(ctx, w.installations, tx)
	if err != nil {
		return provisionedItem{}, err
	}
	configChanges, err := w.planConfigValues(ctx, tx, item, installation.ID)
	if err != nil {
		return provisionedItem{}, err
	}
	runtimeChange, err := w.applyValues(ctx, tx, item, installation, configChanges)
	if err != nil {
		return provisionedItem{}, err
	}
	result := provisionedItem{
		status:         ProvisionStatus{PluginID: item.pluginID, Version: installation.Version, State: "applied"},
		runtimeChanged: runtimeChange || item.pkg.ReplacesPackage() || item.installed == nil,
	}
	if result.runtimeChanged {
		if err := advanceProvisionRuntime(ctx, tx, installation.ID); err != nil {
			return provisionedItem{}, err
		}
	}
	if item.desired.AuthBinding != nil || item.pkg.ReplacesPackage() {
		result.bindingInstallationID = installation.ID
	}
	if item.installed == nil && slices.ContainsFunc(item.pkg.Manifest.GetCapabilities(), isMetadataCapability) {
		if _, err := tx.Exec(ctx, `INSERT INTO plugin_provision_placement (plugin_installation_id) VALUES ($1)`, installation.ID); err != nil {
			return provisionedItem{}, err
		}
		result.placementQueued = true
	}
	if item.held {
		result.status = disabledProvisionStatus(item.pluginID, installation.Version)
	}
	return result, nil
}

// Check after all writes so a declaration can switch providers atomically,
// regardless of the order of its installations.
func validateProvisionBindingSet(ctx context.Context, tx pgx.Tx, bindingIDs map[string]int) error {
	for _, pluginID := range slices.Sorted(maps.Keys(bindingIDs)) {
		err := validateInstallationAuthBindings(ctx, tx, bindingIDs[pluginID])
		switch {
		case errors.Is(err, ErrAuthProviderAlreadyEnabled):
			return &provisionRefusalError{pluginID: pluginID, detail: "The sign-in binding conflicts with another active binding. Disable the other binding, then restart the provisioning instance to apply this declaration."}
		case errors.Is(err, ErrAuthBindingIneligible):
			return &provisionRefusalError{pluginID: pluginID, detail: "An enabled sign-in binding is incompatible with the plugin release. Disable that binding or choose a compatible release."}
		case err != nil:
			return err
		}
	}
	return nil
}

func isMetadataCapability(descriptor *pluginv1.CapabilityDescriptor) bool {
	return descriptor.GetType() == "metadata_provider.v1"
}

func disabledProvisionStatus(pluginID, version string) ProvisionStatus {
	return ProvisionStatus{PluginID: pluginID, Version: version, State: "held", Detail: "An administrator disabled this installation."}
}

func advanceProvisionRuntime(ctx context.Context, tx pgx.Tx, id int) error {
	_, err := tx.Exec(ctx, `UPDATE plugin_installations SET runtime_generation = runtime_generation + 1, updated_at = NOW() WHERE id = $1`, id)
	return err
}

func (w *ProvisionWriter) applyValues(ctx context.Context, tx pgx.Tx, item preparedProvision, installation *Installation, changes map[string]map[string]any) (bool, error) {
	owned := declaredProvisionOwnership(item.desired)
	for _, key := range slices.Sorted(maps.Keys(changes)) {
		if err := upsertGlobalConfig(ctx, tx, w.configs.cipher, installation.ID, key, changes[key]); err != nil {
			return false, err
		}
	}
	if err := PutProvisionOwnership(ctx, tx, installation.ID, owned); err != nil {
		return false, err
	}
	if owned.VersionPin != "" && (installation.AvailableVersion != nil || installation.UpdateFailure != nil) {
		if _, err := tx.Exec(ctx, `UPDATE plugin_installations SET available_version = NULL, update_failed_version = NULL,
			update_failure_reason = NULL, update_failed_at = NULL, updated_at = NOW() WHERE id = $1`, installation.ID); err != nil {
			return false, err
		}
	}
	bindingChanged, err := applyProvisionBinding(ctx, tx, item, installation)
	return len(changes) != 0 || bindingChanged, err
}

type provisionConfigQuery interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Validate the prospective configuration both before starting the transaction
// and while holding the installation lock. A replacement also validates the
// declared projection of saved entries the operator left out.
func (w *ProvisionWriter) planConfigValues(ctx context.Context, q provisionConfigQuery, item preparedProvision, id int) (map[string]map[string]any, error) {
	if item.pkg.ReplacesPackage() {
		if err := w.validateRetainedConfig(ctx, q, item, id); err != nil {
			return nil, err
		}
	}
	changes := make(map[string]map[string]any)
	for _, key := range slices.Sorted(maps.Keys(item.config)) {
		existing := map[string]any{}
		if id != 0 {
			var err error
			existing, err = w.readConfig(ctx, q, id, key, item.pluginID)
			if err != nil {
				return nil, err
			}
		}
		fields := item.config[key]
		next := mergeProvisionConfig(item.pkg.Manifest, key, existing, fields)
		if err := item.validateConfigChange(key, existing, next, fields); err != nil {
			return nil, err
		}
		if err := item.protectSavedConfig(key, existing, next); err != nil {
			return nil, err
		}

		if !SameConfig(existing, next) {
			changes[key] = next
		}
	}
	return changes, nil
}

func (w *ProvisionWriter) validateRetainedConfig(ctx context.Context, q provisionConfigQuery, item preparedProvision, id int) error {
	keys := make(map[string]struct{})
	for _, manifest := range []*pluginv1.PluginManifest{item.pkg.previousManifest, item.pkg.Manifest} {
		for _, schema := range manifest.GetGlobalConfigSchema() {
			if _, submitted := item.config[schema.GetKey()]; !submitted {
				keys[schema.GetKey()] = struct{}{}
			}
		}
	}
	if len(keys) == 0 {
		return nil
	}

	rows, err := q.Query(ctx, `SELECT config_key, config_value FROM plugin_runtime_configs
		WHERE plugin_installation_id = $1 AND config_key = ANY($2::text[]) ORDER BY config_key`, id, slices.Collect(maps.Keys(keys)))
	if err != nil {
		return err
	}
	type savedConfig struct {
		Key string
		Raw []byte
	}
	saved, err := pgx.CollectRows(rows, pgx.RowToStructByPos[savedConfig])
	if err != nil {
		return err
	}
	for _, entry := range saved {
		current, err := w.decodeConfig(id, entry.Key, item.pluginID, entry.Raw)
		if err != nil {
			return err
		}
		if err := item.protectSavedConfig(entry.Key, current, current); err != nil {
			return err
		}

		if HasGlobalConfigSchema(item.pkg.Manifest, entry.Key) {
			if err := validateProvisionConfig(item.pkg.Manifest, entry.Key, current, nil); err != nil {
				return invalidProvisionConfig(item.pluginID, entry.Key, err, false)
			}
		}
	}
	return nil
}

func (item preparedProvision) protectSavedConfig(key string, current, prospective map[string]any) error {
	protected := GlobalConfigSecretPaths(item.pkg.Manifest, key)
	paths := removedCredentialPaths(item.pkg.previousManifest, key, protected)
	if err := protectRetainedCredentials(key, paths, protected, current, prospective); err != nil {
		return &provisionRefusalError{pluginID: item.pluginID, detail: unsafeReplacementConfigMessage(err)}
	}
	return nil
}

func (w *ProvisionWriter) readConfig(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id int, key, pluginID string) (map[string]any, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT config_value FROM plugin_runtime_configs WHERE plugin_installation_id = $1 AND config_key = $2`, id, key).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return w.decodeConfig(id, key, pluginID, raw)
}

func (w *ProvisionWriter) decodeConfig(id int, key, pluginID string, raw []byte) (map[string]any, error) {
	config, err := decodeRuntimeConfigValue(w.configs.cipher, id, key, raw)
	if err != nil {
		return nil, &provisionRefusalError{pluginID: pluginID, detail: "The saved config entry " + key + " could not be read."}
	}
	return config, nil
}

func (item preparedProvision) validateConfigChange(key string, existing, next, fields map[string]any) error {
	if err := checkProvisionNumbers("", fields); err != nil {
		return invalidProvisionConfig(item.pluginID, key, err, true)
	}
	err := validateProvisionConfig(item.pkg.Manifest, key, next, fields)
	if err == nil {
		return nil
	}
	local := item.installed == nil
	if validation, ok := errors.AsType[*jsonschema.ValidationError](err); ok {
		if violation := declaredSchemaViolation(validation, fields); violation != nil {
			return invalidProvisionConfig(item.pluginID, key, violation, true)
		}
		// For constraints on the whole entry, a previously valid projection
		// makes the declaration the source of the new failure.
		local = local || validateMergedGlobalConfig(item.pkg.Manifest, key, existing, nil) == nil
	}
	return invalidProvisionConfig(item.pluginID, key, err, local)
}

func declaredSchemaViolation(failure *jsonschema.ValidationError, fields map[string]any) *jsonschema.ValidationError {
	if len(failure.InstanceLocation) > 0 {
		if _, supplied := fields[failure.InstanceLocation[0]]; supplied {
			return failure
		}
		return nil
	}
	supplied := func(field string) bool {
		_, ok := fields[field]
		return ok
	}
	switch constraint := failure.ErrorKind.(type) {
	case *kind.Required:
		if slices.ContainsFunc(constraint.Missing, supplied) {
			return failure
		}
	case *kind.AnyOf, *kind.OneOf:
		// A branch can fail on saved values even when it accepts the
		// declaration. Attribute the error only if every branch rejects it.
		if len(failure.Causes) > 0 && !slices.ContainsFunc(failure.Causes, func(cause *jsonschema.ValidationError) bool {
			return declaredSchemaViolation(cause, fields) == nil
		}) {
			return failure
		}
	default:
		for _, cause := range failure.Causes {
			if violation := declaredSchemaViolation(cause, fields); violation != nil {
				return violation
			}
		}
	}
	return nil
}

func invalidProvisionConfig(pluginID, key string, err error, local bool) error {
	path := "config." + key
	if number, ok := errors.AsType[*provisionNumberError](err); ok {
		return &provisionRefusalError{pluginID: pluginID, detail: path + number.path + " contains a number the plugin runtime cannot represent.", local: local}
	}
	if validation, ok := errors.AsType[*jsonschema.ValidationError](err); ok {
		for len(validation.InstanceLocation) == 0 && len(validation.Causes) > 0 {
			validation = validation.Causes[0]
		}
		if len(validation.InstanceLocation) > 0 {
			path += "." + strings.Join(validation.InstanceLocation, ".")
		}
	}
	return &provisionRefusalError{pluginID: pluginID, detail: path + " does not satisfy the plugin configuration schema.", local: local}
}

func mergeProvisionConfig(manifest *pluginv1.PluginManifest, key string, existing, fields map[string]any) map[string]any {
	next := maps.Clone(existing)
	maps.Copy(next, fields)
	public, _ := GlobalConfigFieldSets(manifest, key)
	for _, field := range public {
		if incoming, isString := fields[field].(string); isString && incoming == "" {
			delete(next, field)
		}
	}
	return next
}
