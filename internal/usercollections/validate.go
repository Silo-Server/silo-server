package usercollections

import (
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// AllowedSyncSchedules maps the bounded cadence labels available to regular
// accounts to the equivalent server-collection presets.
var AllowedSyncSchedules = map[string]string{
	"daily":   "0 3 * * *",
	"weekly":  "0 3 * * 0",
	"monthly": "0 3 1 * *",
}

// AdminSyncSchedulePresets mirrors the preset cron values offered for admin
// library collections. Admins may also submit any other valid five-field cron.
var AdminSyncSchedulePresets = []string{
	"0 * * * *",
	"0 */6 * * *",
	"0 3 * * *",
	"0 3 * * 1",
	"0 3 * * 0",
	"0 3 1 * *",
}

// ResolveSyncSchedule validates a personal-collection schedule for the
// authenticated account. Regular accounts use the bounded cadence labels;
// server admins may use the same cron schedules as admin library collections.
func ResolveSyncSchedule(value string, allowAdminCron bool) (*string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if expr, ok := AllowedSyncSchedules[strings.ToLower(value)]; ok {
		return &expr, nil
	}
	if !allowAdminCron {
		return nil, fmt.Errorf("invalid sync_schedule %q: must be one of daily, weekly, monthly", value)
	}
	if err := catalog.ParseCronExpression(value); err != nil {
		return nil, err
	}
	return &value, nil
}

func InitialNextSyncAt(schedule *string) *time.Time {
	if schedule == nil || *schedule == "" {
		return nil
	}
	return catalog.ComputeNextSyncAtFrom(*schedule, time.Now())
}
