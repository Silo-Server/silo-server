package auth

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
)

type Permission string

const (
	PermissionMarkerEdit       Permission = "marker_edit"
	PermissionMetadataCuration Permission = "metadata_curation"
)

var assignablePermissions = map[Permission]struct{}{
	PermissionMarkerEdit:       {},
	PermissionMetadataCuration: {},
}

func isAssignablePermission(permission Permission) bool {
	_, ok := assignablePermissions[permission]
	return ok
}

func NormalizePermissions(values []string) ([]string, error) {
	if len(values) == 0 {
		return []string{}, nil
	}

	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		permission := Permission(key)
		if !isAssignablePermission(permission) {
			return nil, fmt.Errorf("unknown permission %q", key)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	sort.Strings(out)
	return out, nil
}

func DefaultUserPermissions() []string {
	return []string{string(PermissionMarkerEdit)}
}

// PolicyPermissions reports the account's permissions after the access-group
// mask: the resolved policy's permission list, which the route gates pass to
// the policy PDP, restricted to assignable permissions. An admin's role grant
// is not added: it applies only while the admin acts through the primary
// profile, which clients resolve per profile, so an admin reports its
// explicitly assigned permissions (admins are never masked by a group).
// Whether the account is enabled is not considered; the caller decides what a
// disabled account sees.
func PolicyPermissions(effective access.EffectiveUserPolicy) []string {
	out := make([]string, 0, len(effective.Permissions))
	for _, value := range effective.Permissions {
		if isAssignablePermission(Permission(value)) && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
