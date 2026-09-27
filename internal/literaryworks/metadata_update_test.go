package literaryworks

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type metadataUpdateLinker struct {
	service *Service
	calls   int
	err     error
}

func (l *metadataUpdateLinker) AutoLinkContent(ctx context.Context, contentID string) (string, bool, error) {
	l.calls++
	if l.err != nil {
		return "", false, l.err
	}
	return l.service.AutoLinkContent(ctx, contentID)
}

func TestMetadataUpdateLinksChangedBookTitles(t *testing.T) {
	for _, sourceType := range []string{FormatEbook, FormatAudiobook} {
		for _, decision := range []string{"none", "ignored", "ignored_reverse"} {
			t.Run(sourceType+"/"+decision, func(t *testing.T) {
				pool := newLiteraryWorksTestPool(t)
				repo := NewRepository(pool)
				service := NewService(repo)
				linker := &metadataUpdateLinker{service: service}
				items := catalog.NewItemRepository(pool)
				detail := catalog.NewDetailService(items, nil, nil, nil, nil)
				detail.SetLiteraryWorkLinker(linker)

				suffix := time.Now().UnixNano()
				sourceID := fmt.Sprintf("metadata-source-%d", suffix)
				targetID := fmt.Sprintf("metadata-target-%d", suffix)
				title := fmt.Sprintf("Corrected title %d", suffix)
				folderID := seedLiteraryFolder(t, pool)
				t.Cleanup(func() {
					ctx := context.Background()
					_, _ = pool.Exec(ctx, `DELETE FROM literary_works WHERE work_id IN (SELECT work_id FROM literary_work_items WHERE content_id = ANY($1))`, []string{sourceID, targetID})
					_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{sourceID, targetID})
					_, _ = pool.Exec(ctx, `DELETE FROM people WHERE id = $1`, suffix)
					_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
				})
				targetType := FormatAudiobook
				if sourceType == FormatAudiobook {
					targetType = FormatEbook
				}
				seedLiteraryMediaItem(t, pool, sourceID, sourceType, "Unmatched title", folderID)
				seedLiteraryMediaItem(t, pool, targetID, targetType, title, folderID)
				if _, err := pool.Exec(t.Context(), `INSERT INTO people (id, name) VALUES ($1, $2)`, suffix, fmt.Sprintf("Author %d", suffix)); err != nil {
					t.Fatal(err)
				}
				if _, err := pool.Exec(t.Context(), `INSERT INTO item_people (id, content_id, person_id, kind) VALUES ($3, $1, $3, 7), ($4, $2, $3, 7)`, sourceID, targetID, suffix, suffix+1); err != nil {
					t.Fatal(err)
				}
				if _, linked, err := service.AutoLinkContent(t.Context(), sourceID); err != nil || linked {
					t.Fatalf("before title correction: linked=%t, err=%v", linked, err)
				}
				if decision != "none" {
					from, to := sourceID, targetID
					if decision == "ignored_reverse" {
						from, to = to, from
					}
					if err := service.IgnoreMatch(t.Context(), from, to, 0); err != nil {
						t.Fatal(err)
					}
				}

				if err := detail.UpdateMediaItemMetadata(t.Context(), sourceID, &catalog.MetadataUpdate{Title: &title}); err != nil {
					t.Fatal(err)
				}
				var linkedItems, works int
				if err := pool.QueryRow(t.Context(), `SELECT COUNT(*), COUNT(DISTINCT work_id) FROM literary_work_items WHERE content_id = ANY($1)`, []string{sourceID, targetID}).Scan(&linkedItems, &works); err != nil {
					t.Fatal(err)
				}
				wantItems, wantWorks := 2, 1
				if decision != "none" {
					wantItems, wantWorks = 0, 0
				}
				if linkedItems != wantItems || works != wantWorks || linker.calls != 1 {
					t.Fatalf("after title correction: linked items=%d works=%d calls=%d, want %d/%d/1", linkedItems, works, linker.calls, wantItems, wantWorks)
				}
			})
		}
	}
}

func TestMetadataUpdateOnlyLinksExplicitBookTitleSaves(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mediaType string
		update    catalog.MetadataUpdate
	}{
		{name: "other metadata", mediaType: FormatAudiobook, update: catalog.MetadataUpdate{Overview: new("New overview")}},
		{name: "movie title", mediaType: "movie", update: catalog.MetadataUpdate{Title: new("New title")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := newLiteraryWorksTestPool(t)
			contentID := fmt.Sprintf("metadata-no-link-%d", time.Now().UnixNano())
			if _, err := pool.Exec(t.Context(), `INSERT INTO media_items (content_id, type, title) VALUES ($1, $2, 'Original title')`, contentID, tc.mediaType); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
			})
			linker := &metadataUpdateLinker{err: errors.New("unexpected literary matching")}
			detail := catalog.NewDetailService(catalog.NewItemRepository(pool), nil, nil, nil, nil)
			detail.SetLiteraryWorkLinker(linker)
			if err := detail.UpdateMediaItemMetadata(t.Context(), contentID, &tc.update); err != nil {
				t.Fatal(err)
			}
			if linker.calls != 0 {
				t.Fatalf("literary matcher called %d times for %s", linker.calls, tc.name)
			}
		})
	}
}

func TestMetadataTitleSaveRetriesLinkingAfterFailure(t *testing.T) {
	for _, sourceType := range []string{FormatEbook, FormatAudiobook} {
		t.Run(sourceType, func(t *testing.T) {
			f := newEditionLinkFixture(t)
			source := f.add("source", sourceType, f.prefix+"original title")
			targetType := FormatAudiobook
			if sourceType == FormatAudiobook {
				targetType = FormatEbook
			}
			target := f.add("target", targetType, "")
			items := catalog.NewItemRepository(f.pool)
			linker := &metadataUpdateLinker{service: f.service, err: errors.New("temporary literary matching failure")}
			detail := catalog.NewDetailService(items, nil, nil, nil, nil)
			detail.SetLiteraryWorkLinker(linker)
			if err := detail.UpdateMediaItemMetadata(t.Context(), source, &catalog.MetadataUpdate{Title: &f.title}); err != nil {
				t.Fatalf("title save failed because of linking: %v", err)
			}
			saved, err := items.GetByID(t.Context(), source)
			if err != nil || saved.Title != f.title || linker.calls != 1 {
				t.Fatalf("first save: item=%+v calls=%d err=%v", saved, linker.calls, err)
			}
			f.assertWork(source, "")
			f.assertWork(target, "")

			linker.err = nil
			if err := detail.UpdateMediaItemMetadata(t.Context(), source, &catalog.MetadataUpdate{Title: &f.title}); err != nil {
				t.Fatal(err)
			}
			work, err := f.repo.GetFirstWorkIDForContentIDs(t.Context(), []string{source})
			if err != nil || work == "" || linker.calls != 2 {
				t.Fatalf("same-title retry: work=%q calls=%d err=%v; want successful linking on second save", work, linker.calls, err)
			}
			f.assertWork(target, work)
		})
	}
}

func TestMetadataUpdateKeepsSavedTitleWhenLiteraryLinkingFails(t *testing.T) {
	pool := newLiteraryWorksTestPool(t)
	contentID := fmt.Sprintf("metadata-link-failure-%d", time.Now().UnixNano())
	if _, err := pool.Exec(t.Context(), `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'ebook', 'Original title')`, contentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	items := catalog.NewItemRepository(pool)
	linker := &metadataUpdateLinker{err: errors.New("synthetic literary matching failure")}
	detail := catalog.NewDetailService(items, nil, nil, nil, nil)
	detail.SetLiteraryWorkLinker(linker)
	title := "Corrected title"
	if err := detail.UpdateMediaItemMetadata(t.Context(), contentID, &catalog.MetadataUpdate{Title: &title}); err != nil {
		t.Fatalf("successful metadata save failed because of linking: %v", err)
	}
	item, err := items.GetByID(t.Context(), contentID)
	if err != nil || item.Title != title || linker.calls != 1 {
		t.Fatalf("saved item=%+v, linking calls=%d, err=%v", item, linker.calls, err)
	}
}
