package literaryworks

import (
	"context"
	"fmt"
	"testing"
	"time"

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
