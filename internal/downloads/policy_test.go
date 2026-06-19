package downloads

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestFormatPolicyResolverResolve(t *testing.T) {
	cases := []struct {
		name             string
		requested        string
		transcodeEnabled bool
		userTranscode    bool
		want             string
		wantErr          error
	}{
		{name: "empty defaults to original", requested: "", want: FormatOriginal},
		{name: "original allowed", requested: FormatOriginal, want: FormatOriginal},
		{name: "remux allowed", requested: FormatRemux, want: FormatRemux},
		{
			name:             "transcode allowed when server and user gates open",
			requested:        FormatTranscode,
			transcodeEnabled: true,
			userTranscode:    true,
			want:             FormatTranscode,
		},
		{
			name:             "transcode blocked by server gate",
			requested:        FormatTranscode,
			transcodeEnabled: false,
			userTranscode:    true,
			wantErr:          ErrTranscodeDisabled,
		},
		{
			name:             "transcode blocked by user flag",
			requested:        FormatTranscode,
			transcodeEnabled: true,
			userTranscode:    false,
			wantErr:          ErrDownloadNotAllowed,
		},
		{name: "unknown format rejected", requested: "webm", wantErr: ErrInvalidFormat},
	}

	var resolver FormatPolicyResolver
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			user := &models.User{DownloadAllowed: true, DownloadTranscodeAllowed: tc.userTranscode}
			cfg := config.DownloadConfig{Enabled: true, TranscodeEnabled: tc.transcodeEnabled}

			got, err := resolver.Resolve(tc.requested, user, cfg)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Resolve(%q) err = %v, want %v", tc.requested, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q) unexpected err: %v", tc.requested, err)
			}
			if got != tc.want {
				t.Fatalf("Resolve(%q) = %q, want %q", tc.requested, got, tc.want)
			}
		})
	}
}
