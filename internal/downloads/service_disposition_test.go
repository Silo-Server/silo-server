package downloads

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestAttachmentDisposition matches the proxy nodes' header: ASCII names stay
// in filename, non-ASCII names go out as RFC 2231 filename*.
func TestAttachmentDisposition(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"/media/movie.mkv", `attachment; filename=movie.mkv`},
		{"/media/Movie: Final.mp4", `attachment; filename="Movie_ Final.mp4"`},
		{"/media/Amélie (2001).mkv", `attachment; filename*=utf-8''Am%C3%A9lie%20%282001%29.mkv`},
		{"/media/千と千尋の神隠し.mkv", `attachment; filename*=utf-8''%E5%8D%83%E3%81%A8%E5%8D%83%E5%B0%8B%E3%81%AE%E7%A5%9E%E9%9A%A0%E3%81%97.mkv`},
	} {
		if got := attachmentDisposition(tc.path); got != tc.want {
			t.Errorf("attachmentDisposition(%q) = %s, want %s", tc.path, got, tc.want)
		}
	}
}

type namingItems map[string]*models.MediaItem

func (n namingItems) GetByID(_ context.Context, id string) (*models.MediaItem, error) {
	return n[id], nil
}

type namingEpisodes struct {
	EpisodeResolver
	episode *models.Episode
}

func (n namingEpisodes) GetByID(context.Context, string) (*models.Episode, error) {
	return n.episode, nil
}

// A caller who may not see file names saves a download under a name built from
// the title, not the file's own name, which can carry release details.
func TestAttachmentName(t *testing.T) {
	items := namingItems{
		"movie":  {Title: "Heat", Year: 1995},
		"series": {Title: "Severance"},
	}
	episodes := namingEpisodes{episode: &models.Episode{SeriesID: "series", SeasonNumber: 1, EpisodeNumber: 2, Title: "Half Loop"}}
	for _, tc := range []struct {
		name string
		file *models.MediaFile
		want string
	}{
		{"movie", &models.MediaFile{ContentID: "movie"}, "Heat (1995).mkv"},
		{"episode", &models.MediaFile{EpisodeID: "episode"}, "Severance - S01E02 - Half Loop.mkv"},
		{"local extra", &models.MediaFile{ExtraID: "extra"}, "Download.mkv"},
	} {
		svc := &Service{fileRepo: fakeFileResolver{file: tc.file}, itemRepo: items, episodeRepo: episodes}
		target := &FileTarget{Path: "/media/Heat.1995.2160p.REMUX-GROUP.MKV", MediaFileID: 7}
		if got := svc.AttachmentName(t.Context(), target); got != tc.want {
			t.Errorf("%s: AttachmentName = %q, want %q", tc.name, got, tc.want)
		}
		if got := svc.AttachmentName(WithFileNamesVisible(t.Context()), target); got != "Heat.1995.2160p.REMUX-GROUP.MKV" {
			t.Errorf("%s: curator AttachmentName = %q, want the file's own name", tc.name, got)
		}
	}
}
