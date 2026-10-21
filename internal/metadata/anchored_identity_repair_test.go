package metadata

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/contentid"
)

func TestParseAnchoredGroupIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		version        int
		key            string
		wantOK         bool
		wantTarget     string
		wantProvider   string
		wantProviderID string
	}{
		{
			name:           "series tvdb",
			version:        1,
			key:            "v1|series|anchor|tvdb-415239",
			wantOK:         true,
			wantTarget:     "series-tvdb-415239",
			wantProvider:   contentid.ProviderTVDB,
			wantProviderID: "415239",
		},
		{
			name:           "movie imdb normalizes case",
			version:        1,
			key:            "v1|movie|anchor|imdb-tt33763941",
			wantOK:         true,
			wantTarget:     "movie-imdb-tt33763941",
			wantProvider:   contentid.ProviderIMDB,
			wantProviderID: "tt33763941",
		},
		{name: "version mismatch", version: 2, key: "v1|series|anchor|tvdb-415239"},
		{name: "title group", version: 1, key: "v1|series|diplomat|2023"},
		{name: "unknown provider", version: 1, key: "v1|series|anchor|anidb-123"},
		{name: "invalid numeric id", version: 1, key: "v1|series|anchor|tvdb-nope"},
		{name: "wrong shape", version: 1, key: "v1|series|anchor"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseAnchoredGroupIdentity(tt.version, tt.key)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (identity = %+v)", ok, tt.wantOK, got)
			}
			if !tt.wantOK {
				return
			}
			if got.TargetContent != tt.wantTarget ||
				got.Provider != tt.wantProvider ||
				got.ProviderID != tt.wantProviderID {
				t.Fatalf(
					"identity = %+v, want target=%q provider=%q id=%q",
					got,
					tt.wantTarget,
					tt.wantProvider,
					tt.wantProviderID,
				)
			}
		})
	}
}
