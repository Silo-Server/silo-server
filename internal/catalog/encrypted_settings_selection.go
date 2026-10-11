package catalog

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/secret"
)

// UpdateValueAndSettingsAtomic retains the coordinated store's lock order and
// plaintext callback contract, including encryption of the credential bundle.
func (r *EncryptedSettingsRepo) UpdateValueAndSettingsAtomic(ctx context.Context, key string, update func(map[string]string) (map[string]string, error)) error {
	inner, ok := r.inner.(interface {
		UpdateValueAndSettingsAtomic(context.Context, string, func(map[string]string) (map[string]string, error)) error
	})
	if !ok {
		return fmt.Errorf("settings store does not support coordinated updates")
	}
	return inner.UpdateValueAndSettingsAtomic(ctx, key, func(current map[string]string) (map[string]string, error) {
		plain := make(map[string]string, len(current))
		for name, value := range current {
			value, err := r.cipher.DecryptIfEncrypted(value, secret.SettingsAAD(name))
			if err != nil {
				// Match GetAll: blocked storage recovery owns this receipt's
				// error. Omit it from the snapshot and retain its raw row.
				if name == config.StorageTransitionTargetKey {
					continue
				}
				return nil, fmt.Errorf("decrypt setting %q: %w", name, err)
			}
			plain[name] = value
		}
		writes, err := update(plain)
		if err != nil {
			return nil, err
		}
		for name, value := range writes {
			if SensitiveSettingKeys[name] && value != "" {
				value, err = r.cipher.Encrypt(value, secret.SettingsAAD(name))
				if err != nil {
					return nil, fmt.Errorf("encrypt setting %q: %w", name, err)
				}
				writes[name] = value
			}
		}
		return writes, nil
	})
}
