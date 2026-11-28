package models

// ProviderNumbers are the episode's season and episode numbers in its
// provider's default order: the stored provider numbers when the series shows
// another order, else the shown ones.
func (e *Episode) ProviderNumbers() (season, episode int) {
	if e.ProviderSeasonNumber != nil && e.ProviderEpisodeNumber != nil {
		return *e.ProviderSeasonNumber, *e.ProviderEpisodeNumber
	}
	return e.SeasonNumber, e.EpisodeNumber
}
