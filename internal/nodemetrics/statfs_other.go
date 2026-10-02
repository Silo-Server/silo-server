//go:build !linux && !darwin

package nodemetrics

import "errors"

// osStatfs has no implementation on platforms without statfs(2). Sampling is
// Linux-only anyway, so this only has to keep the package building.
func osStatfs(string) (fsStats, error) {
	return fsStats{}, errors.ErrUnsupported
}
