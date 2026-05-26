package auth

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
)

type Permission string

const PermissionMetadataCuration Permission = "metadata_curation"

var assignablePermissions = map[Permission]struct{}{
	PermissionMetadataCuration: {},
}

var effectiveAdminPermissions = []string{
	string(PermissionMetadataCuration),
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
		if _, ok := assignablePermissions[permission]; !ok {
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

func HasAssignedPermission(user *models.User, permission Permission) bool {
	if user == nil {
		return false
	}
	for _, value := range user.Permissions {
		if value == string(permission) {
			return true
		}
	}
	return false
}

func HasEffectivePermission(user *models.User, permission Permission) bool {
	if user == nil || !user.Enabled {
		return false
	}
	if user.Role == "admin" {
		return true
	}
	return HasAssignedPermission(user, permission)
}

func EffectivePermissions(user *models.User) []string {
	if user == nil || !user.Enabled {
		return []string{}
	}
	if user.Role == "admin" {
		return append([]string(nil), effectiveAdminPermissions...)
	}
	permissions, err := NormalizePermissions(user.Permissions)
	if err != nil {
		return []string{}
	}
	return permissions
}
