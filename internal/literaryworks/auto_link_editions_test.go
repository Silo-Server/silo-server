package literaryworks

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type editionLinkFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	repo    *Repository
	service *Service
	prefix  string
	title   string
	person  int64
	ids     []string
}

func newEditionLinkFixture(t *testing.T) *editionLinkFixture {
	t.Helper()
	pool := newLiteraryWorksTestPool(t)
	suffix := time.Now().UnixNano()
	f := &editionLinkFixture{t: t, pool: pool, repo: NewRepository(pool), prefix: fmt.Sprintf("edition-%d-", suffix), person: suffix}
	f.service = NewService(f.repo)
	f.title = f.prefix + "title"
	if _, err := pool.Exec(t.Context(), `INSERT INTO people (id, name) VALUES ($1, $2)`, f.person, f.prefix+"author"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM literary_works WHERE work_id IN (SELECT work_id FROM literary_work_items WHERE content_id=ANY($1))`, f.ids)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id=ANY($1)`, f.ids)
		_, _ = pool.Exec(ctx, `DELETE FROM people WHERE id=$1`, f.person)
	})
	return f
}

func (f *editionLinkFixture) add(name, format, title string) string {
	f.t.Helper()
	id := f.prefix + name
	if title == "" {
		title = f.title
	}
	if _, err := f.pool.Exec(f.t.Context(), `INSERT INTO media_items (content_id, type, title) VALUES ($1,$2,$3)`, id, format, title); err != nil {
		f.t.Fatal(err)
	}
	f.ids = append(f.ids, id)
	if _, err := f.pool.Exec(f.t.Context(), `INSERT INTO item_people (id, content_id, person_id, kind) VALUES ($1,$2,$3,7)`, time.Now().UnixNano(), id, f.person); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *editionLinkFixture) link(ids ...string) string {
	f.t.Helper()
	// Explicit work IDs let the fixture create separate manually curated works
	// even when their titles and authors match.
	workID := f.prefix + "work-" + ids[0]
	if _, err := f.repo.CreateWork(f.t.Context(), CreateWorkParams{WorkID: workID, CanonicalTitle: f.title}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.service.LinkItems(f.t.Context(), workID, ids); err != nil {
		f.t.Fatal(err)
	}
	return workID
}

func (f *editionLinkFixture) assertWork(id, want string) {
	f.t.Helper()
	got, err := f.repo.GetFirstWorkIDForContentIDs(f.t.Context(), []string{id})
	if err != nil || got != want {
		f.t.Fatalf("item %s work=%q err=%v, want %q", id, got, err, want)
	}
}

func TestAutoLinkEditionsPreservesIgnoresAndExistingWorks(t *testing.T) {
	for _, scenario := range []string{"all editions", "ignored source", "ignored best", "ignored earlier candidate", "ignored work member", "source ignored work member", "other work"} {
		t.Run(scenario, func(t *testing.T) {
			f := newEditionLinkFixture(t)
			source := f.add("source", FormatEbook, "")
			best := f.add("a-best", FormatAudiobook, "")
			second := f.add("b-second", FormatAudiobook, "")
			third := f.add("c-third", FormatAudiobook, "")
			// Same-format work members are not part of the opposite-format
			// candidate query; ignores against them must still be respected.
			member := f.add("member", FormatEbook, f.prefix+"old title")
			work := f.link(best, member)
			wantSource, wantSecond, wantThird := work, work, work
			var from, to string
			switch scenario {
			case "ignored source":
				from, to, wantSecond = source, second, ""
			case "ignored best":
				from, to, wantSecond = second, best, ""
			case "ignored earlier candidate":
				from, to, wantThird = second, third, ""
			case "ignored work member":
				from, to, wantSecond = second, member, ""
			case "source ignored work member":
				from, to = member, source
				wantSource, wantSecond, wantThird = "", "", ""
			case "other work":
				wantThird = f.link(third)
			}
			if from != "" {
				if err := f.service.IgnoreMatch(t.Context(), from, to, 0); err != nil {
					t.Fatal(err)
				}
			}
			linkedWork, linked, err := f.service.AutoLinkContent(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "source ignored work member" {
				if !linked || linkedWork == "" || linkedWork == work {
					t.Fatalf("eligible editions did not form a separate work: work=%q linked=%t", linkedWork, linked)
				}
				wantSource, wantSecond, wantThird = linkedWork, linkedWork, linkedWork
			}
			f.assertWork(source, wantSource)
			f.assertWork(best, work)
			f.assertWork(second, wantSecond)
			f.assertWork(third, wantThird)
		})
	}
}

func TestMetadataTitleUpdateFindsEditionsForAlreadyLinkedBook(t *testing.T) {
	f := newEditionLinkFixture(t)
	source := f.add("source", FormatEbook, "")
	existing := f.add("existing", FormatAudiobook, "")
	work := f.link(source, existing)
	corrected := f.prefix + "corrected title"
	additional := f.add("additional", FormatAudiobook, corrected)
	detail := catalog.NewDetailService(catalog.NewItemRepository(f.pool), nil, nil, nil, nil)
	detail.SetLiteraryWorkLinker(f.service)
	if err := detail.UpdateMediaItemMetadata(t.Context(), source, &catalog.MetadataUpdate{Title: &corrected}); err != nil {
		t.Fatal(err)
	}
	f.assertWork(source, work)
	f.assertWork(existing, work)
	f.assertWork(additional, work)
}

func TestAutoLinkDoesNotBridgeIncompatibleCandidates(t *testing.T) {
	f := newEditionLinkFixture(t)
	source := f.add("source", FormatEbook, "")
	best := f.add("a-best", FormatAudiobook, f.prefix+"first title")
	other := f.add("b-other", FormatAudiobook, f.prefix+"second title")
	for i, target := range []string{best, other} {
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type) VALUES ($1,$3,$4,'ebook'), ($2,$3,$4,'audiobook')`, source, target, fmt.Sprintf("provider-%d", i), f.prefix+target); err != nil {
			t.Fatal(err)
		}
	}
	work, linked, err := f.service.AutoLinkContent(t.Context(), source)
	if err != nil || !linked {
		t.Fatalf("link source: linked=%t err=%v", linked, err)
	}
	f.assertWork(best, work)
	f.assertWork(other, "")
}

func TestAutomaticLinksDoNotMoveItemsLinkedAfterCandidateRead(t *testing.T) {
	for _, moved := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("candidate-%d", moved), func(t *testing.T) {
			f := newEditionLinkFixture(t)
			ids := []string{
				f.add("source", FormatEbook, ""),
				f.add("best", FormatAudiobook, ""),
				f.add("additional", FormatAudiobook, ""),
			}
			member := f.add("member", FormatAudiobook, "")
			work := f.link(member)
			// A manual link occurs after candidate hydration but before the
			// automatic transaction. Its stale list must not move that item.
			otherWork := f.link(ids[moved])
			items := []LinkItemParams{
				{ContentID: ids[0], FormatType: FormatEbook, LinkSource: LinkMetadataMatch, Confidence: 0.9},
				{ContentID: ids[1], FormatType: FormatAudiobook, LinkSource: LinkMetadataMatch, Confidence: 0.9},
				{ContentID: ids[2], FormatType: FormatAudiobook, LinkSource: LinkMetadataMatch, Confidence: 0.9},
			}
			linked, err := f.repo.autoLinkItems(t.Context(), work, items)
			if err != nil || linked != (moved == 2) {
				t.Fatalf("linked=%t err=%v, want linked=%t", linked, err, moved == 2)
			}
			for i, id := range ids {
				want := ""
				if moved == 2 {
					want = work
				}
				if i == moved {
					want = otherWork
				}
				f.assertWork(id, want)
			}
		})
	}
}

func TestAutoLinkEventLinksEditionsBeyondFirstCandidatePage(t *testing.T) {
	f := newEditionLinkFixture(t)
	source := f.add("source", FormatEbook, "")
	var last string
	for i := range 101 {
		last = f.add(fmt.Sprintf("target-%03d", i), FormatAudiobook, "")
	}
	matchSource, err := f.repo.GetMatchItem(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	for limit, want := range map[int]int{0: 20, 7: 7} {
		candidates, err := f.repo.ListMatchCandidates(t.Context(), matchSource.MatchItem, limit)
		if err != nil || len(candidates) != want {
			t.Fatalf("public candidate limit=%d: count=%d err=%v, want %d", limit, len(candidates), err, want)
		}
	}
	// The strongest candidate is on page two. Choose its existing work,
	// rather than creating one from the first page's lower-scoring matches.
	wantWork := f.link(last)
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type) VALUES ($1,'isbn',$3,'ebook'), ($2,'isbn',$3,'audiobook')`, source, last, f.prefix+"isbn"); err != nil {
		t.Fatal(err)
	}
	work, linked, err := f.service.AutoLinkContent(t.Context(), source)
	if err != nil || !linked || work != wantWork {
		t.Fatalf("link source: work=%q linked=%t err=%v, want work=%q", work, linked, err, wantWork)
	}
	var members int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM literary_work_items WHERE work_id=$1`, work).Scan(&members); err != nil || members != 102 {
		t.Fatalf("work members=%d err=%v, want source plus all 101 candidates", members, err)
	}
	f.assertWork(last, work)
}

func TestAutoLinkPagesKeepSourceWorkAndMatchExclusions(t *testing.T) {
	f := newEditionLinkFixture(t)
	source := f.add("source", FormatEbook, "")
	work := f.link(source)
	weak := make([]string, 0, 100)
	for i := range 99 {
		weak = append(weak, f.add(fmt.Sprintf("a-weak-%03d", i), FormatAudiobook, ""))
	}
	best := f.add("b-best", FormatAudiobook, "")
	second := f.add("c-second", FormatAudiobook, "")
	ignored := f.add("d-ignored", FormatAudiobook, "")
	other := f.add("e-other", FormatAudiobook, "")
	weak = append(weak, f.add("f-weak", FormatAudiobook, ""))
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM item_people WHERE content_id=ANY($1)`, weak); err != nil {
		t.Fatal(err)
	}
	otherWork := f.link(other)
	if err := f.service.IgnoreMatch(t.Context(), best, ignored, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.AutoLinkContent(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	f.assertWork(source, work)
	f.assertWork(best, work)
	f.assertWork(second, work)
	f.assertWork(ignored, "")
	f.assertWork(other, otherWork)
	for _, id := range weak {
		f.assertWork(id, "")
	}
}

func TestManualLinksStillOverrideIgnoredPairs(t *testing.T) {
	f := newEditionLinkFixture(t)
	first := f.add("first", FormatEbook, "")
	second := f.add("second", FormatAudiobook, "")
	if err := f.service.IgnoreMatch(t.Context(), first, second, 0); err != nil {
		t.Fatal(err)
	}
	work := f.link(first, second)
	f.assertWork(first, work)
	f.assertWork(second, work)
}

func TestMatchCandidatePagesFollowTitleAndContentIDOrder(t *testing.T) {
	f := newEditionLinkFixture(t)
	source := f.add("source", FormatEbook, "")
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO ebook_series (content_id, series_name, series_index) VALUES ($1,$2,1)`, source, f.prefix); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, title := range []string{"c", "a", "b", "a"} {
		id := f.add(fmt.Sprintf("target-%d", i), FormatAudiobook, f.prefix+title)
		ids = append(ids, id)
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO audiobook_series (content_id, series_name, series_index) VALUES ($1,$2,1)`, id, f.prefix); err != nil {
			t.Fatal(err)
		}
	}
	matchSource, err := f.repo.GetMatchItem(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	var after *matchCandidateCursor
	var got []string
	for range 3 {
		page, next, err := f.repo.listMatchCandidatesPage(t.Context(), matchSource.MatchItem, 2, after, nil)
		if err != nil || len(page) > 2 {
			t.Fatalf("candidate page len=%d err=%v", len(page), err)
		}
		for _, item := range page {
			got = append(got, item.ContentID)
		}
		if next == nil {
			break
		}
		after = next
	}
	if want := []string{ids[1], ids[3], ids[2], ids[0]}; !slices.Equal(got, want) {
		t.Fatalf("candidate order=%v, want %v", got, want)
	}
}

type cancelCandidatePageTracer struct {
	cancel context.CancelFunc
	pages  int
}

func (c *cancelCandidatePageTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT mi.content_id, mi.title") {
		c.pages++
		if c.pages == 2 {
			c.cancel()
		}
	}
	return ctx
}

func (*cancelCandidatePageTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestAutoLinkStopsWhenCandidatePagingIsCanceled(t *testing.T) {
	f := newEditionLinkFixture(t)
	source := f.add("source", FormatEbook, "")
	for i := range 101 {
		f.add(fmt.Sprintf("target-%03d", i), FormatAudiobook, "")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tracer := &cancelCandidatePageTracer{cancel: cancel}
	config := f.pool.Config()
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, linked, err := NewService(NewRepository(pool)).AutoLinkContent(ctx, source)
	if !errors.Is(err, context.Canceled) || linked || tracer.pages != 2 {
		t.Fatalf("canceled candidate paging: linked=%t pages=%d err=%v", linked, tracer.pages, err)
	}
	f.assertWork(source, "")
}

func TestAutoLinkSkipsAnchorsWithIgnoredWorkMembers(t *testing.T) {
	for _, sourceLinked := range []bool{false, true} {
		t.Run(fmt.Sprintf("source-linked-%t", sourceLinked), func(t *testing.T) {
			f := newEditionLinkFixture(t)
			source := f.add("source", FormatEbook, "")
			blocked := f.add("a-blocked", FormatAudiobook, "")
			eligible := f.add("b-eligible", FormatAudiobook, "")
			member := f.add("member", FormatEbook, f.prefix+"old title")
			var wantWork, blockedWork string
			from, to := source, member
			if sourceLinked {
				wantWork = f.link(source, member)
				from, to = blocked, member
			} else {
				blockedWork = f.link(blocked, member)
				wantWork = f.link(eligible)
			}
			if err := f.service.IgnoreMatch(t.Context(), from, to, 0); err != nil {
				t.Fatal(err)
			}
			work, linked, err := f.service.AutoLinkContent(t.Context(), source)
			if err != nil || !linked || work != wantWork {
				t.Fatalf("work=%q linked=%t err=%v, want eligible work=%q", work, linked, err, wantWork)
			}
			f.assertWork(source, wantWork)
			f.assertWork(eligible, wantWork)
			f.assertWork(blocked, blockedWork)
			// Candidate suggestions retain their existing pairwise semantics;
			// only automatic work selection applies the work-member exclusions.
			matchSource, err := f.repo.GetMatchItem(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			candidates, err := f.repo.ListMatchCandidates(t.Context(), matchSource.MatchItem, 10)
			if err != nil || len(candidates) != 2 {
				t.Fatalf("public candidates=%d err=%v, want both pairwise matches", len(candidates), err)
			}
		})
	}
}

func TestAutoLinkGeneratedWorkCollisionPreservesExistingWork(t *testing.T) {
	for _, ignoredAnchor := range []string{"source", "target", "none"} {
		t.Run(ignoredAnchor, func(t *testing.T) {
			f := newEditionLinkFixture(t)
			originalEbook := f.add("a-original-ebook", FormatEbook, "")
			originalAudio := f.add("a-original-audio", FormatAudiobook, "")
			originalWork, linked, err := f.service.AutoLinkContent(t.Context(), originalEbook)
			if err != nil || !linked {
				t.Fatalf("create original work: linked=%t err=%v", linked, err)
			}
			match, err := f.repo.GetMatchItem(t.Context(), originalEbook)
			if err != nil || originalWork != generatedWorkID(match.MatchItem) {
				t.Fatalf("ordinary work ID=%q err=%v, want unchanged title/author identity", originalWork, err)
			}
			if _, err := f.pool.Exec(t.Context(), `UPDATE literary_works SET canonical_title='Curated title', description='Preserved description', publisher='Curated publisher', genres=ARRAY['Curated genre'] WHERE work_id=$1`, originalWork); err != nil {
				t.Fatal(err)
			}
			before, err := f.repo.GetWork(t.Context(), originalWork)
			if err != nil {
				t.Fatal(err)
			}
			source := f.add("new-source", FormatEbook, "")
			target := f.add("new-target", FormatAudiobook, "")
			if ignoredAnchor != "source" {
				// Select the new unlinked pair while leaving the original work's
				// title/author-derived ID in use by its existing editions.
				if _, err := f.pool.Exec(t.Context(), `UPDATE media_items SET title=$2 WHERE content_id=$1`, originalAudio, f.prefix+"old edition title"); err != nil {
					t.Fatal(err)
				}
			}
			if ignoredAnchor != "none" {
				ignored := source
				if ignoredAnchor == "target" {
					ignored = target
				}
				if err := f.service.IgnoreMatch(t.Context(), ignored, originalEbook, 0); err != nil {
					t.Fatal(err)
				}
			}
			work, linked, err := f.service.AutoLinkContent(t.Context(), source)
			if err != nil || !linked || work == "" {
				t.Fatalf("link eligible pair: work=%q linked=%t err=%v", work, linked, err)
			}
			if (work == originalWork) != (ignoredAnchor == "none") {
				t.Fatalf("ignored anchor=%s work=%q original=%q: only ignored work conflicts should change the generated ID", ignoredAnchor, work, originalWork)
			}
			f.assertWork(source, work)
			f.assertWork(target, work)
			f.assertWork(originalEbook, originalWork)
			f.assertWork(originalAudio, originalWork)
			after, err := f.repo.GetWork(t.Context(), originalWork)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("existing work changed: before=%+v after=%+v err=%v", before, after, err)
			}
			if ignoredAnchor == "none" {
				return
			}
			// A formerly valid pair-specific work may later acquire another
			// member the source ignores. Do not reuse that blocked fallback.
			member := f.add("fallback-member", FormatEbook, f.prefix+"another title")
			if _, err := f.service.LinkItems(t.Context(), work, []string{member}); err != nil {
				t.Fatal(err)
			}
			if err := f.service.IgnoreMatch(t.Context(), source, member, 0); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{source, target} {
				if err := f.service.UnlinkItem(t.Context(), work, id); err != nil {
					t.Fatal(err)
				}
			}
			fallbackBefore, err := f.repo.GetWork(t.Context(), work)
			if err != nil {
				t.Fatal(err)
			}
			nextWork, linked, err := f.service.AutoLinkContent(t.Context(), source)
			if err != nil || !linked || nextWork == "" || nextWork == work || nextWork == originalWork {
				t.Fatalf("relink eligible pair: work=%q linked=%t err=%v", nextWork, linked, err)
			}
			f.assertWork(source, nextWork)
			f.assertWork(target, nextWork)
			f.assertWork(member, work)
			fallbackAfter, err := f.repo.GetWork(t.Context(), work)
			if err != nil || !reflect.DeepEqual(fallbackBefore, fallbackAfter) {
				t.Fatalf("blocked fallback work changed: before=%+v after=%+v err=%v", fallbackBefore, fallbackAfter, err)
			}
		})
	}
}

func TestAutoLinkPrefersExistingWorkOnScoreTie(t *testing.T) {
	for _, tc := range []struct {
		name               string
		unlinkedCount      int
		unlinkedIsStronger bool
	}{
		{name: "same page", unlinkedCount: 1},
		{name: "later page", unlinkedCount: 100},
		{name: "stronger unlinked candidate", unlinkedCount: 1, unlinkedIsStronger: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEditionLinkFixture(t)
			source := f.add("source", FormatEbook, "")
			var unlinked []string
			for i := range tc.unlinkedCount {
				unlinked = append(unlinked, f.add(fmt.Sprintf("a-unlinked-%03d", i), FormatAudiobook, ""))
			}
			existing := f.add("z-linked", FormatAudiobook, "")
			existingWork := f.link(existing)
			if tc.unlinkedIsStronger {
				if _, err := f.pool.Exec(t.Context(), `INSERT INTO media_item_provider_ids (content_id, provider, provider_id, item_type) VALUES ($1,'isbn',$3,'ebook'), ($2,'isbn',$3,'audiobook')`, source, unlinked[0], f.prefix+"isbn"); err != nil {
					t.Fatal(err)
				}
			}
			work, linked, err := f.service.AutoLinkContent(t.Context(), source)
			if err != nil || !linked || work == "" {
				t.Fatalf("link source: work=%q linked=%t err=%v", work, linked, err)
			}
			if (work == existingWork) == tc.unlinkedIsStronger {
				t.Fatalf("work=%q existing=%q stronger unlinked=%t; existing work should win only on equal score", work, existingWork, tc.unlinkedIsStronger)
			}
			f.assertWork(source, work)
			f.assertWork(existing, existingWork)
			for _, id := range unlinked {
				f.assertWork(id, work)
			}
		})
	}
}
