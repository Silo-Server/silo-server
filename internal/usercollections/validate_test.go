package usercollections

import "testing"

func TestCadenceOf(t *testing.T) {
	cases := []struct {
		schedule, want string
	}{
		{"", ""},
		{"  ", ""},
		{AllowedSyncSchedules["daily"], "daily"},
		{AllowedSyncSchedules["weekly"], "weekly"},
		{AllowedSyncSchedules["monthly"], "monthly"},
		{" 30  4 * * 0 ", "weekly"},
		// Expressions no cadence name produces, such as a sub-daily one.
		{"*/15 * * * *", "custom"},
		{"0 4 * * *", "custom"},
		{"daily", "custom"},
	}
	for _, tc := range cases {
		if got := CadenceOf(tc.schedule); got != tc.want {
			t.Errorf("CadenceOf(%q) = %q, want %q", tc.schedule, got, tc.want)
		}
	}
}
