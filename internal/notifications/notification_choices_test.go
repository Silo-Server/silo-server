package notifications

import "testing"

// Every combination of email sends maps to a single mode that round-trips
// the sends it can express and is off exactly when nothing is on.
func TestEmailModeOfSends(t *testing.T) {
	for _, c := range []struct {
		instant, daily, weekly bool
		mode                   string
	}{
		{false, false, false, ChannelModeOff},
		{true, false, false, ChannelModePerEpisode},
		{false, true, false, ChannelModeDailyDigest},
		{false, false, true, ChannelModeWeeklyDigest},
		{true, true, false, ChannelModePerEpisodeAndDigest},
		{true, false, true, EmailModePerEpisodeAndWeeklyDigest},
		{false, true, true, ChannelModeDailyDigest},
		{true, true, true, ChannelModePerEpisodeAndDigest},
	} {
		mode := emailModeOfSends(c.instant, c.daily, c.weekly)
		if mode != c.mode {
			t.Errorf("emailModeOfSends(%t,%t,%t) = %s, want %s", c.instant, c.daily, c.weekly, mode, c.mode)
		}
		if !ValidEmailMode(mode) {
			t.Errorf("%s is not a valid email mode", mode)
		}
		i, d, w := emailSendsOfMode(mode)
		if (i || d || w) != (c.instant || c.daily || c.weekly) {
			t.Errorf("mode %s turns email on or off differently from its sends", mode)
		}
	}
}
