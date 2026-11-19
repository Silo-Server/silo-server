package recommendations

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/lang"
)

const (
	// externalMaxSeeds is how many of a profile's titles seed its pool.
	externalMaxSeeds = 100
	// externalLanguageShare is the share of a profile's seeds a language
	// needs before the profile uses that language's sets.
	externalLanguageShare = 0.05
)

// ExternalSeed is a library title whose TMDB lists seed a profile's pool.
type ExternalSeed struct {
	// ContentID is the library title, a movie or series.
	ContentID string
	Key       ExternalTitleKey
	// Weight is the title's positive weight in the profile's taste vector.
	Weight float64
	// OriginalLanguage is the title's original language as the catalog
	// stores it (lang.Canonical), or "".
	OriginalLanguage string
}

// SourceKey is the source key the seed's lists are stored under.
func (s ExternalSeed) SourceKey() string { return externalSeedSourceKey(s.Key) }

func externalSeedSourceKey(k ExternalTitleKey) string {
	return "seed:" + k.MediaType + ":" + strconv.Itoa(k.TMDBID)
}

// ExternalProfile is what a profile's outside-title pool is built from: its
// seeds, heaviest first, and the set sources it uses.
type ExternalProfile struct {
	UserID     int
	ProfileID  string
	Seeds      []ExternalSeed
	SetSources []string
}

// SeedSources returns the profile's seed source keys, heaviest seed first.
func (p ExternalProfile) SeedSources() []string {
	keys := make([]string, len(p.Seeds))
	for i, s := range p.Seeds {
		keys[i] = s.SourceKey()
	}
	return keys
}

// SeedContentIDs maps each seed source key to the seed's library content ID,
// so a pool title's seeds can be named as reasons.
func (p ExternalProfile) SeedContentIDs() map[string]string {
	out := make(map[string]string, len(p.Seeds))
	for _, s := range p.Seeds {
		out[s.SourceKey()] = s.ContentID
	}
	return out
}

// PoolQuery is the store query for the profile's pool with the per-profile
// cap, the query a ranker reads stored candidates with.
func (p ExternalProfile) PoolQuery() ExternalPoolQuery {
	return ExternalPoolQuery{SeedSources: p.SeedSources(), SetSources: p.SetSources, Cap: externalProfileListCap}
}

// externalProfile returns a profile's seeds and the set sources it uses,
// from the weights its taste refresh computes, and whether the profile still
// exists. It reads only the database. A profile that no longer exists has no
// seeds and no sets.
func (e *Engine) externalProfile(ctx context.Context, userID int, profileID string) (ExternalProfile, bool, error) {
	p := ExternalProfile{UserID: userID, ProfileID: profileID}
	if e.storeProvider == nil || e.ratingsRepo == nil {
		return p, false, fmt.Errorf("outside-title seeds for user %d profile %s: no user store or ratings", userID, profileID)
	}
	store, err := e.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return p, false, fmt.Errorf("get user store for user %d: %w", userID, err)
	}
	if store == nil {
		return p, false, nil
	}
	profile, err := store.GetProfile(ctx, profileID)
	if err != nil {
		return p, false, fmt.Errorf("get profile %s: %w", profileID, err)
	}
	if profile == nil {
		return p, false, nil
	}
	signals, err := e.readTasteSignals(ctx, store, userID, profileID)
	if err != nil {
		return p, false, fmt.Errorf("read signals for user %d profile %s: %w", userID, profileID, err)
	}
	weights := make(map[string]float64, len(signals.titles))
	for id, s := range signals.titles {
		if w, ok := s.positiveWeight(); ok {
			weights[id] = w
		}
	}
	titles, err := e.repo.externalSeedTitles(ctx, slices.Collect(maps.Keys(weights)))
	if err != nil {
		return p, false, err
	}
	p.Seeds = selectExternalSeeds(weights, titles)
	p.SetSources = externalProfileSetSources(p.Seeds)
	return p, true, nil
}

// selectExternalSeeds picks a profile's seeds: its positively weighted titles
// that the catalog knows by TMDB ID, heaviest first (ties by content ID), one
// per outside-title key, at most externalMaxSeeds.
func selectExternalSeeds(weights map[string]float64, titles map[string]externalSeedTitle) []ExternalSeed {
	seeds := make([]ExternalSeed, 0, len(titles))
	for id, w := range weights {
		t, ok := titles[id]
		if !ok || w <= 0 {
			continue
		}
		seeds = append(seeds, ExternalSeed{ContentID: id, Key: t.key, Weight: w, OriginalLanguage: t.originalLanguage})
	}
	slices.SortFunc(seeds, func(a, b ExternalSeed) int {
		return cmp.Or(cmp.Compare(b.Weight, a.Weight), cmp.Compare(a.ContentID, b.ContentID))
	})
	seen := make(map[ExternalTitleKey]struct{}, len(seeds))
	out := seeds[:0]
	for _, s := range seeds {
		if _, dup := seen[s.Key]; dup {
			continue
		}
		seen[s.Key] = struct{}{}
		out = append(out, s)
		if len(out) == externalMaxSeeds {
			break
		}
	}
	return out
}

// externalProfileLanguages returns, sorted, the original languages other than
// English that make up at least externalLanguageShare of seeds.
func externalProfileLanguages(seeds []ExternalSeed) []string {
	if len(seeds) == 0 {
		return nil
	}
	counts := map[string]int{}
	for _, s := range seeds {
		if l := lang.Canonical(s.OriginalLanguage); l != "" && l != "en" {
			counts[l]++
		}
	}
	var out []string
	for l, n := range counts {
		if float64(n) >= externalLanguageShare*float64(len(seeds)) {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out
}

// externalProfileSetSources returns the set sources a profile uses: the
// recent sets, and the sets of each language that is at least
// externalLanguageShare of its seeds. A profile with no seeds (a taste vector
// built from titles without a TMDB ID) still gets the recent sets: ranked by
// its taste vector they remain useful to it.
func externalProfileSetSources(seeds []ExternalSeed) []string {
	var keys []string
	for _, set := range externalSetSpecs(externalProfileLanguages(seeds)) {
		keys = append(keys, set.key)
	}
	return keys
}

// externalSeedTitle is a library title's outside-title key and original
// language.
type externalSeedTitle struct {
	key              ExternalTitleKey
	originalLanguage string
}

// externalSeedTitles returns the movies and series among contentIDs that
// carry a TMDB ID, keyed by content ID.
func (r *Repo) externalSeedTitles(ctx context.Context, contentIDs []string) (map[string]externalSeedTitle, error) {
	out := make(map[string]externalSeedTitle, len(contentIDs))
	if len(contentIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT content_id, type, tmdb_id, COALESCE(original_language, '')
		FROM   media_items
		WHERE  content_id = ANY($1)
		  AND  type IN ('movie', 'series')
		  AND  tmdb_id ~ '^[0-9]{1,9}$'`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("read outside-title seed titles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, mediaType, tmdbID, language string
		if err := rows.Scan(&id, &mediaType, &tmdbID, &language); err != nil {
			return nil, fmt.Errorf("scan outside-title seed title: %w", err)
		}
		n, err := strconv.Atoi(tmdbID)
		if err != nil || n <= 0 {
			continue
		}
		out[id] = externalSeedTitle{key: ExternalTitleKey{MediaType: mediaType, TMDBID: n}, originalLanguage: language}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read outside-title seed titles: %w", err)
	}
	return out, nil
}

// profilesWithTasteVector lists the profiles whose taste row has a vector.
func (r *Repo) profilesWithTasteVector(ctx context.Context) ([]StaleProfile, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, profile_id FROM user_taste_profiles
		WHERE  embedding IS NOT NULL
		ORDER  BY user_id, profile_id`)
	if err != nil {
		return nil, fmt.Errorf("list profiles with a taste vector: %w", err)
	}
	profiles, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (StaleProfile, error) {
		var p StaleProfile
		err := row.Scan(&p.UserID, &p.ProfileID)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("list profiles with a taste vector: %w", err)
	}
	return profiles, nil
}
