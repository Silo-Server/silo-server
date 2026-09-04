package usercollections

import (
	"fmt"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

const (
	syncScheduleHourly        = "0 * * * *"
	syncScheduleEverySixHours = "0 */6 * * *"
	syncScheduleDaily         = "0 3 * * *"
	syncScheduleMonday        = "0 3 * * 1"
	syncScheduleSunday        = "0 3 * * 0"
	syncScheduleMonthly       = "0 3 1 * *"
)

// AllowedSyncSchedules maps the bounded cadence labels available to regular
// accounts to the equivalent server-collection presets.
var AllowedSyncSchedules = map[string]string{
	"daily":   syncScheduleDaily,
	"weekly":  syncScheduleSunday,
	"monthly": syncScheduleMonthly,
}

// AdminSyncSchedulePresets mirrors the preset cron values offered for admin
// library collections. Admins may also submit any other valid five-field cron.
var AdminSyncSchedulePresets = []string{
	syncScheduleHourly,
	syncScheduleEverySixHours,
	syncScheduleDaily,
	syncScheduleMonday,
	syncScheduleSunday,
	syncScheduleMonthly,
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

func isBoundedSyncSchedule(schedule string) bool {
	for _, allowed := range AllowedSyncSchedules {
		if schedule == allowed {
			return true
		}
	}
	return false
}

func InitialNextSyncAt(schedule *string) *time.Time {
	if schedule == nil || *schedule == "" {
		return nil
	}
	return catalog.ComputeNextSyncAtFrom(*schedule, time.Now())
}
