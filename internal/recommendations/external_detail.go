package recommendations

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations/embeddings"
)

// errNoExternalLock is why a run stores no outside vectors before library
// vectors have locked the embedding model.
var errNoExternalLock = errors.New("no embedding lock yet; library embeddings create it")

// refreshTitles fetches the detail of each chosen title that has none, an old
// one, or no vector for the locked model, stores its card fields, and embeds
// its canonical text unless the stored vector's text is the same. Titles come
// in chosen's order, so sets go first. Without a usable embedding lock it
// fetches nothing: a detail is only worth its request when the title can be
// embedded from it.
func (r *externalRun) refreshTitles(ctx context.Context, chosen []ExternalTitleKey, embedBudget int) error {
	e := r.e
	lock, reason, err := e.externalEmbeddingLock(ctx)
	if err != nil {
		return err
	}
	if lock == nil {
		r.counts.VectorsSkipped = reason
		return nil
	}
	stale, err := e.repo.ExternalTitlesNeedingDetail(ctx, chosen, r.now.Add(-externalDetailRefetch))
	if err != nil {
		return err
	}
	texts, err := e.repo.ExternalEmbeddingTexts(ctx, chosen, lock.Model)
	if err != nil {
		return err
	}
	needed := make(map[ExternalTitleKey]bool, len(stale))
	for _, k := range stale {
		needed[k] = true
	}
	// A title whose text the provider refused waits, like an embedded one,
	// until its stored detail is due: until then its text is taken as the
	// refused one, so neither its detail nor its embedding is asked for.
	refused := e.externalRefusedEmbeds
	refused.useLock(lock)
	var work []ExternalTitleKey
	for _, k := range chosen {
		_, embedded := texts[k]
		switch {
		case needed[k]:
			work = append(work, k)
		case embedded:
		case refused.hasID(externalEmbedID(k)):
			r.counts.RefusedSkipped++
		default:
			work = append(work, k)
		}
	}
	if len(work) == 0 {
		return nil
	}
	language, err := e.repo.externalMetadataLanguage(ctx)
	if err != nil {
		return err
	}

	emb := &embedBackfill{
		client:  e.embClient,
		model:   lock.Model,
		held:    r.held,
		sink:    &externalEmbedSink{repo: e.repo, lock: *lock},
		refused: refused,
	}
	attempted := 0
	defer func() {
		r.counts.Embeddings = emb.counts
		r.counts.DetailsLeft = len(work) - attempted
	}()
	for start := 0; start < len(work); {
		if err := ctx.Err(); err != nil {
			return err
		}
		embedLeft := embedBudget - emb.counts.Embedded - emb.counts.Failed - emb.counts.Skipped
		if embedLeft <= 0 {
			r.counts.BudgetExhausted = true
			break
		}
		chunk := work[start:min(start+externalDetailChunk, start+embedLeft, len(work))]
		start += len(chunk)
		details, notFound, n, err := r.fetchDetails(ctx, chunk, language)
		attempted += n
		if err != nil {
			return err
		}
		owned, err := r.dropOwnedDetails(ctx, details)
		if err != nil {
			return err
		}
		// A title the library has under its IMDb or TVDB ID is forgotten
		// like one TMDB lacks: it is not stored, and the links naming it go
		// so later runs do not fetch it again until a source names it anew.
		if err := e.repo.forgetExternalTitles(ctx, append(notFound, owned...)); err != nil {
			return err
		}
		ids, chunkTexts, err := r.storeDetails(ctx, details, texts, refused)
		if err != nil {
			return err
		}
		if err := emb.embedBatch(ctx, ids, chunkTexts); err != nil {
			return err
		}
		if n < len(chunk) {
			break
		}
	}
	return emb.noneStoredError()
}

// externalEmbeddingLock returns the embedding lock outside vectors are stored
// under, or nil and why there is none to use: no embedding provider, no lock
// yet, or a lock for another model than the configured one.
func (e *Engine) externalEmbeddingLock(ctx context.Context) (*EmbeddingLock, string, error) {
	if !e.embeddingConfigured() || e.embClient == nil {
		return nil, ErrNoEmbeddingProvider.Error(), nil
	}
	lock, err := checkEmbeddingLockConfig(ctx, e.repo, e.cfg.EmbeddingModel)
	if err != nil {
		if lock != nil {
			return nil, err.Error(), nil
		}
		return nil, "", err
	}
	if lock == nil {
		return nil, errNoExternalLock.Error(), nil
	}
	return lock, "", nil
}

// fetchDetails fetches the details of keys in language, in parallel within
// the TMDB budget. details[i] is nil for a key not found, failed or left
// unfetched; notFound lists the keys TMDB does not know; n counts the keys
// fetched or tried, the rest being left by the budget.
func (r *externalRun) fetchDetails(ctx context.Context, keys []ExternalTitleKey, language string) (details []externalDetail, notFound []ExternalTitleKey, n int, err error) {
	details = make([]externalDetail, len(keys))
	tried := make([]bool, len(keys))
	err = forEachBounded(ctx, len(keys), func(ctx context.Context, i int) (bool, error) {
		if !r.takeRequest() {
			return true, nil
		}
		tried[i] = true
		d, err := r.tmdb.GetMediaDetailInLanguage(ctx, keys[i].MediaType, keys[i].TMDBID, language)
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		if stop := r.fetchOutcome(err); stop != nil {
			return true, stop
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		switch {
		case errors.Is(err, tmdb.ErrNotFound):
			r.counts.DetailsNotFound++
			notFound = append(notFound, keys[i])
		case err != nil || d == nil:
			r.counts.DetailsFailed++
		default:
			r.counts.DetailsFetched++
			details[i] = externalDetail{key: keys[i], detail: d}
		}
		return false, nil
	})
	for _, t := range tried {
		if t {
			n++
		}
	}
	return details, notFound, n, err
}

type externalDetail struct {
	key    ExternalTitleKey
	detail *tmdb.MediaDetail
}

// dropOwnedDetails checks the fetched titles against the library with every
// ID their detail gives (TMDB, TVDB, IMDb), clears the detail of each the
// library has, and returns their keys. The pool was chosen by TMDB ID alone,
// so this catches a library item matched only by another provider.
func (r *externalRun) dropOwnedDetails(ctx context.Context, details []externalDetail) ([]ExternalTitleKey, error) {
	var titles []externalTitleIDs
	for _, d := range details {
		if d.detail != nil {
			titles = append(titles, externalTitleIDs{key: d.key, imdbID: strings.TrimSpace(d.detail.IMDbID), tvdbID: d.detail.TVDBID})
		}
	}
	if len(titles) == 0 {
		return nil, nil
	}
	owned, err := r.ownedTitles(ctx, titles)
	if err != nil {
		return nil, err
	}
	var keys []ExternalTitleKey
	for i := range details {
		if details[i].detail != nil && owned[details[i].key] {
			details[i].detail = nil
			keys = append(keys, details[i].key)
		}
	}
	r.counts.DetailsOwned += len(keys)
	return keys, nil
}

// storeDetails upserts the fetched titles and returns the IDs and canonical
// texts of those to embed: every fetched title whose text differs from the
// one stored with its vector and from one the provider refused. A title
// whose text changed loses its old vector first, so a failed embedding leaves
// it without one and the next run embeds it, instead of ranking it by the
// old text until its detail is due again.
func (r *externalRun) storeDetails(ctx context.Context, details []externalDetail, storedTexts map[ExternalTitleKey]string, refused *refusedEmbedInputs) (ids, texts []string, err error) {
	titles := make([]ExternalTitle, 0, len(details))
	var changed []ExternalTitleKey
	for _, d := range details {
		if d.detail == nil {
			continue
		}
		titles = append(titles, externalTitleFromDetail(d.key, d.detail, r.now))
		text := embeddings.BuildEmbeddingText(externalDetailItem(d.key.MediaType, d.detail))
		stored, hasVector := storedTexts[d.key]
		if hasVector && stored == text {
			r.counts.TextUnchanged++
			continue
		}
		id := externalEmbedID(d.key)
		if refused.has(id, text) {
			r.counts.RefusedSkipped++
			continue
		}
		if hasVector {
			changed = append(changed, d.key)
		}
		ids = append(ids, id)
		texts = append(texts, text)
	}
	if err := r.e.repo.UpsertExternalTitles(ctx, titles); err != nil {
		return nil, nil, err
	}
	if err := r.e.repo.deleteExternalEmbeddings(ctx, changed); err != nil {
		return nil, nil, err
	}
	return ids, texts, nil
}

// externalTitleFromDetail is the card data stored for an outside title.
func externalTitleFromDetail(k ExternalTitleKey, d *tmdb.MediaDetail, fetchedAt time.Time) ExternalTitle {
	return ExternalTitle{
		Key:              k,
		Title:            d.Title,
		Overview:         d.Overview,
		PosterPath:       d.PosterPath,
		BackdropPath:     d.BackdropPath,
		ReleaseDate:      d.ReleaseDate,
		OriginalLanguage: lang.Canonical(d.OriginalLanguage),
		Popularity:       d.Popularity,
		VoteAverage:      d.VoteAverage,
		VoteCount:        d.VoteCount,
		USCertification:  d.USCertification,
		IMDbID:           strings.TrimSpace(d.IMDbID),
		TVDBID:           max(d.TVDBID, 0),
		DetailFetchedAt:  fetchedAt,
	}
}

// externalDetailItem fills the fields embeddings.BuildEmbeddingText reads the
// way a library title enriched by the TMDB metadata plugin stores them, so an
// outside title's text compares with library texts. Studios are the
// production companies for both media types, as the plugin stores them, and
// the rating is the US one the plugin picks (tmdb.MediaDetail.USContentRating).
func externalDetailItem(mediaType string, d *tmdb.MediaDetail) *models.MediaItem {
	item := &models.MediaItem{
		Type:     mediaType,
		Title:    d.Title,
		Year:     d.Year,
		Overview: d.Overview,
		Tagline:  d.Tagline,
		Genres:   d.Genres,
		// The host stores keywords once each, ignoring case, in TMDB's order.
		Keywords: uniqueFold(d.Keywords),
		// The host stores the language as lang.Canonical and countries as
		// ISO 3166-1 codes (metadata.metadataResultToItem).
		OriginalLanguage: lang.Canonical(d.OriginalLanguage),
		Countries:        lang.CanonicalCountries(d.OriginCountries),
		Networks:         d.Networks,
		Studios:          d.ProductionCompanies,
		ContentRating:    d.USContentRating,
	}
	// The plugin records actors with TMDB's billing order and crew with sort
	// order 0, so crew sort by name as library crew does.
	for _, c := range d.Cast {
		item.People = append(item.People, models.ItemPerson{
			Person: models.Person{Name: c.Name}, Kind: models.PersonKindActor, Character: c.Character, SortOrder: c.Order,
		})
	}
	for _, name := range d.Directors {
		item.People = append(item.People, models.ItemPerson{Person: models.Person{Name: name}, Kind: models.PersonKindDirector})
	}
	for _, name := range d.Writers {
		item.People = append(item.People, models.ItemPerson{Person: models.Person{Name: name}, Kind: models.PersonKindWriter})
	}
	return item
}

func uniqueFold(values []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		key := strings.ToLower(v)
		if _, dup := seen[key]; dup || v == "" {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, v)
	}
	return out
}

// externalEmbedID names an outside title in the shared embedding loop and its
// logs.
func externalEmbedID(k ExternalTitleKey) string {
	return "external:" + k.MediaType + ":" + strconv.Itoa(k.TMDBID)
}

func parseExternalEmbedID(id string) (ExternalTitleKey, bool) {
	rest, ok := strings.CutPrefix(id, "external:")
	if !ok {
		return ExternalTitleKey{}, false
	}
	mediaType, num, ok := strings.Cut(rest, ":")
	n, err := strconv.Atoi(num)
	if !ok || err != nil {
		return ExternalTitleKey{}, false
	}
	return ExternalTitleKey{MediaType: mediaType, TMDBID: n}, true
}

// externalEmbedSink stores outside vectors under an existing embedding lock.
// It never writes the lock.
type externalEmbedSink struct {
	repo *Repo
	lock EmbeddingLock
}

func (s *externalEmbedSink) checkVector(_ context.Context, vector []float32) error {
	if err := checkStorableVector(vector); err != nil {
		return err
	}
	return s.lock.Validate(s.lock.Model, len(vector))
}

func (s *externalEmbedSink) write(ctx context.Context, id string, vector []float32, canonicalText string) error {
	key, ok := parseExternalEmbedID(id)
	if !ok {
		return fmt.Errorf("outside title %q: bad id", id)
	}
	return s.repo.UpsertExternalEmbeddings(ctx, []ExternalEmbedding{{Key: key, Vector: vector, Model: s.lock.Model, CanonicalText: canonicalText}})
}

// externalMetadataLanguage is the language outside titles are fetched in: the
// metadata language of the enabled library holding the most movies and
// series, or English when there is none.
func (r *Repo) externalMetadataLanguage(ctx context.Context) (string, error) {
	var language string
	err := r.pool.QueryRow(ctx, `
		SELECT mf.metadata_language
		FROM   media_folders mf
		JOIN   media_item_libraries mil ON mil.media_folder_id = mf.id
		JOIN   media_items mi ON mi.content_id = mil.content_id
		WHERE  mf.enabled AND mi.type IN ('movie', 'series')
		GROUP  BY mf.id, mf.metadata_language
		ORDER  BY count(*) DESC, mf.id
		LIMIT  1`).Scan(&language)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && strings.TrimSpace(language) == "") {
		return "en", nil
	}
	if err != nil {
		return "", fmt.Errorf("read outside-title metadata language: %w", err)
	}
	return language, nil
}
