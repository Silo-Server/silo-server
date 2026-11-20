package plugins

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func ownedWithBinding(ownership ProvisionOwnership, binding *ProvisionAuthBinding) ProvisionOwnership {
	if binding == nil {
		return ownership
	}
	fields := make([]string, 0, 5)
	if binding.AutoProvision != nil {
		fields = append(fields, "auto_provision")
	}
	if binding.Callback != "" {
		fields = append(fields, "callback_mode")
	}
	if binding.DefaultLogin != nil {
		fields = append(fields, "default_login")
	}
	if binding.DisplayOrder != nil {
		fields = append(fields, "display_order")
	}
	if binding.Enabled != nil {
		fields = append(fields, "enabled")
	}
	if len(fields) != 0 {
		ownership.BindingFields[binding.CapabilityID] = fields
	}
	return ownership
}

func applyProvisionBinding(ctx context.Context, tx pgx.Tx, item preparedProvision, installation *Installation) (bool, error) {
	input := item.desired.AuthBinding
	if input == nil {
		return false, nil
	}

	current := AuthBinding{InstallationID: installation.ID, CapabilityID: input.CapabilityID}
	err := tx.QueryRow(ctx, `
		SELECT enabled, display_order, auto_provision, default_login, callback_mode
		FROM plugin_auth_bindings
		WHERE plugin_installation_id = $1 AND capability_id = $2
	`, installation.ID, input.CapabilityID).Scan(&current.Enabled, &current.DisplayOrder, &current.AutoProvision, &current.DefaultLogin, &current.CallbackMode)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	next := current
	if errors.Is(err, pgx.ErrNoRows) {
		next.AutoProvision = true
		next.CallbackMode = AuthCallbackInstallation
		capability := authProviderCapability(item.pkg.Manifest, input.CapabilityID)
		if ValidateAuthCallbackMode(AuthCallbackStable, capability.GetAuthModes()) == nil {
			next.CallbackMode = AuthCallbackStable
		}
	}
	if input.Enabled != nil {
		next.Enabled = *input.Enabled
	}
	if input.DisplayOrder != nil {
		next.DisplayOrder = int(*input.DisplayOrder)
	}
	if input.AutoProvision != nil {
		next.AutoProvision = *input.AutoProvision
	}
	if input.DefaultLogin != nil {
		next.DefaultLogin = *input.DefaultLogin
	}
	if input.Callback != "" {
		next.CallbackMode = input.Callback
	}
	if next == current && err == nil {
		return false, nil
	}
	return true, upsertAuthBindingInTx(ctx, tx, next)
}
