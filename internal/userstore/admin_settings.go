package userstore

import "context"

// AdminSettingValuePager is the bounded administrator inspection projection.
// It is optional so transaction-only stores do not advertise unsupported reads.
type AdminSettingValuePager interface {
	ListAdminSettingValuesPage(context.Context, SettingIdentity, int) ([]SettingValue, bool, error)
}
