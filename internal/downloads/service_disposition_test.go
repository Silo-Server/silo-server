package downloads

import "testing"

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
