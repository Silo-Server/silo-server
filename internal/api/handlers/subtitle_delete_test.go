package handlers

import (
	"context"
	"errors"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type viewerDeleteRepo struct {
	*handlerMockSubtitleRepo
	calls        int
	revision     *int64
	uncertain    bool
	beforeDelete func()
}

func (r *viewerDeleteRepo) DeleteDownloadedSubtitleWithRevision(_ context.Context, id int, revision *int64) (*subtitles.DownloadedSubtitle, error) {
	r.calls++
	r.revision = revision
	if r.beforeDelete != nil {
		r.beforeDelete()
	}
	row := r.subtitles[id]
	if row == nil {
		return nil, subtitles.ErrSubtitleNotFound
	}
	if revision == nil || row.Revision != *revision {
		return nil, &subtitles.SubtitleRevisionConflict{Current: row}
	}
	delete(r.subtitles, id)
	if r.uncertain {
		return nil, errors.New("PRIVATE lost successful SQL reply")
	}
	return row, nil
}

type viewerDeleteObjects struct {
	handlerMockBlobStore
	deleted int
}

func (s *viewerDeleteObjects) Delete(context.Context, string) error {
	s.deleted++
	return nil
}

func TestViewerSubtitleDeletionAuthorityAndCAS(t *testing.T) {
	for _, kind := range []string{"owner", "other", "unowned", "admin", "admin_denied_file", "denied_file", "no_claims", "changed_owner", "stale", "cas_race", "uncertain"} {
		t.Run(kind, func(t *testing.T) {
			repo := &viewerDeleteRepo{handlerMockSubtitleRepo: newMockSubtitleRepoForHandler()}
			repo.subtitles[9] = &subtitles.DownloadedSubtitle{ID: 9, MediaFileID: 42, DownloadedBy: new(1), Revision: 3, S3Key: "owned"}
			objects := new(viewerDeleteObjects)
			h := NewSubtitleSearchHandler(subtitles.NewManager(repo, objects), repo, nil)
			checker := stubItemAccessChecker{}
			if kind == "denied_file" || kind == "admin_denied_file" {
				checker.err = catalog.ErrItemNotFound
			}
			h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: checker}
			claims := &auth.Claims{UserID: 1, Role: "user"}
			if kind == "admin" || kind == "admin_denied_file" {
				claims.Role = "admin"
				repo.subtitles[9].DownloadedBy = new(2)
			}
			if kind == "other" {
				repo.subtitles[9].DownloadedBy = new(2)
			}
			if kind == "unowned" {
				repo.subtitles[9].DownloadedBy = nil
			}
			ctx := apimw.SetClaims(t.Context(), claims)
			if kind == "no_claims" {
				ctx = t.Context()
			}
			access := catalog.AccessFilter{UserID: 1}
			_, err := h.GetViewerSubtitleForDeletion(ctx, access, 9)
			denied := kind == "other" || kind == "unowned" || kind == "denied_file" || kind == "admin_denied_file" || kind == "no_claims"
			if denied {
				if err == nil {
					t.Fatal("unauthorized metadata")
				}
				if err = h.DeleteViewerSubtitle(ctx, access, 9, 3); err == nil || repo.calls != 0 || objects.deleted != 0 {
					t.Fatal("unauthorized deletion")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "changed_owner" {
				repo.subtitles[9].DownloadedBy = new(2)
			}
			if kind == "stale" {
				repo.subtitles[9].Revision++
			}
			if kind == "cas_race" {
				repo.beforeDelete = func() { repo.subtitles[9].Revision++ }
			}
			repo.uncertain = kind == "uncertain"
			err = h.DeleteViewerSubtitle(ctx, access, 9, 3)
			if kind == "owner" || kind == "admin" {
				if err != nil || objects.deleted != 1 || repo.subtitles[9] != nil {
					t.Fatal("delete failed", err)
				}
				return
			}
			if err == nil || objects.deleted != 0 {
				t.Fatal("refusal authorized cleanup")
			}
			if kind == "uncertain" {
				if repo.subtitles[9] != nil || repo.calls != 1 {
					t.Fatal("uncertainty fixture did not commit once")
				}
			} else if repo.subtitles[9] == nil {
				t.Fatal("refused deletion removed row")
			}
		})
	}
}
